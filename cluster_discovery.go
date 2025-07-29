package main

import (
	"context"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"sync"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// discoverKubeconfigs fetches the clusters/contexts the user has configured and returns
// a map from context name to a ready-to-use kubernetes clientset.
func discoverKubeconfigs() (map[string]*kubernetes.Clientset, error) {
	// Find the default kubeconfig path
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not get user home directory: %w", err)
	}
	kubeconfigPath := filepath.Join(home, ".kube", "config")

	// Load the kubeconfig file
	config, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("could not load kubeconfig from %s: %w", kubeconfigPath, err)
	}

	clientsets := make(map[string]*kubernetes.Clientset)

	// Iterate over all the contexts in the kubeconfig
	for contextName := range config.Contexts {
		// Create a client config for the specific context
		clientConfig := clientcmd.NewNonInteractiveClientConfig(*config, contextName, &clientcmd.ConfigOverrides{}, nil)
		restConfig, err := clientConfig.ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("could not create client config for context %s: %w", contextName, err)
		}

		// Create a clientset from the config
		clientset, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			return nil, fmt.Errorf("could not create clientset for context %s: %w", contextName, err)
		}
		clientsets[contextName] = clientset
	}

	return clientsets, nil
}

// discoverRayClusters accepts a kubernetes client as an argument and finds
// all services with the label "ray.io/node-type=head". It returns a slice of
// v1.Service objects.
func discoverRayClusters(clientset *kubernetes.Clientset) ([]v1.Service, error) {
	// Find all services with the ray head node label across all namespaces
	services, err := clientset.CoreV1().Services("").List(context.Background(), metav1.ListOptions{
		LabelSelector: "ray.io/node-type=head",
	})
	if err != nil {
		return nil, fmt.Errorf("could not list services: %w", err)
	}

	return services.Items, nil
}

func watchRayClusters(ctx context.Context, clientset *kubernetes.Clientset) <-chan []v1.Service {
	serviceChan := make(chan []v1.Service)

	go func() {
		defer close(serviceChan)

		var resourceVersion string
		serviceCache := make(map[string]v1.Service)

		// Initial list to populate the cache
		initialList, err := clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{
			LabelSelector: "ray.io/node-type=head",
		})
		if err != nil {
			log.Printf("Error getting initial service list: %v", err)
			return
		}

		resourceVersion = initialList.ResourceVersion
		for _, service := range initialList.Items {
			serviceCache[string(service.UID)] = service
		}
		serviceChan <- flattenServices(serviceCache)

		for {
			watcher, err := clientset.CoreV1().Services("").Watch(ctx, metav1.ListOptions{
				LabelSelector:   "ray.io/node-type=head",
				ResourceVersion: resourceVersion,
			})
			if err != nil {
				log.Printf("Error creating watcher: %v. Retrying...", err)
				continue
			}

			for {
				select {
				case event, ok := <-watcher.ResultChan():
					if !ok {
						log.Println("Watcher channel closed, restarting watch.")
						goto EndWatch
					}

					service, ok := event.Object.(*v1.Service)
					if !ok {
						continue
					}

					resourceVersion = service.ResourceVersion

					switch event.Type {
					case watch.Modified:
						continue
					case watch.Added:
						serviceCache[string(service.UID)] = *service
					case watch.Deleted:
						delete(serviceCache, string(service.UID))
					}
					serviceChan <- flattenServices(serviceCache)

				case <-ctx.Done():
					log.Println("Context cancelled, stopping watcher.")
					watcher.Stop()
					return
				}
			}
		EndWatch:
			watcher.Stop()
		}
	}()

	return serviceChan
}

func flattenServices(cache map[string]v1.Service) []v1.Service {
	services := make([]v1.Service, 0, len(cache))
	for _, service := range cache {
		services = append(services, service)
	}
	return services
}

type ServiceWatcher struct {
	m sync.Mutex
	// clusterTree is ray clusters indexed by context name then UUID.
	clusterTree map[string]map[string]RayClusterHandle
}

func (w *ServiceWatcher) RunAll(ctx context.Context, clients map[string]*kubernetes.Clientset) {
	w.clusterTree = make(map[string]map[string]RayClusterHandle)
	for contextName, kc := range clients {
		go func() {
			for ctx.Err() == nil {
				log.Println("start (or restart) watch for", contextName)
				w.watchContext(ctx, contextName, kc)
			}
		}()
	}
	<-ctx.Done()
}

func (w *ServiceWatcher) watchContext(ctx context.Context, contextName string, kc *kubernetes.Clientset) {
	svcChan := watchRayClusters(ctx, kc)
	for {
		select {
		case <-ctx.Done():
			return
		case svcs, ok := <-svcChan:
			if !ok {
				return
			}
			w.storeServices(contextName, kc, svcs)
		}
	}
}

func (w *ServiceWatcher) storeServices(contextName string, kc *kubernetes.Clientset, svcs []v1.Service) {
	w.m.Lock()
	defer w.m.Unlock()
	if _, ok := w.clusterTree[contextName]; !ok {
		w.clusterTree[contextName] = make(map[string]RayClusterHandle)
	}
	for _, svc := range svcs {
		handle := makeHandle(contextName, kc, svc)
		w.clusterTree[contextName][handle.UID] = handle
	}
}

// Clusteres returns a copy of the current state of watched clusters.
func (w *ServiceWatcher) Clusters() map[string]map[string]RayClusterHandle {
	w.m.Lock()
	defer w.m.Unlock()

	result := make(map[string]map[string]RayClusterHandle)
	for name, submap := range w.clusterTree {
		result[name] = maps.Clone(submap)
	}
	return result
}

func makeHandle(contextName string, kc *kubernetes.Clientset, svc corev1.Service) RayClusterHandle {
	return RayClusterHandle{
		kc:             kc,
		RayClusterName: svc.GetLabels()["ray.io/cluster"],
		Namespace:      svc.GetNamespace(),
		Service:        svc.GetName(),
		UID:            string(svc.GetUID()),
		ContextName:    contextName,
	}
}
