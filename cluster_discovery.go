package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

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

type ClientEvent struct {
	contextName string
	kc          *kubernetes.Clientset
}

func watchClients(ctx context.Context) (AddedChan <-chan ClientEvent, DeletedChan <-chan ClientEvent) {
	added := make(chan ClientEvent)
	deleted := make(chan ClientEvent)

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		defer close(added)
		defer close(deleted)

		// Send initial list of contexts.
		clients, err := discoverKubeconfigs()
		if err != nil {
			log.Printf("Failed initial list of kube configs: %v", err)
		}
		for name, kc := range clients {
			added <- ClientEvent{contextName: name, kc: kc}
		}

		// Loop and send added or removed contexts.
		prevClients := clients
		for {
			select {
			case <-ticker.C:
				clients, err := discoverKubeconfigs()
				if err != nil {
					log.Printf("Failed to find kube configs: %v", err)
				}
				for name, kc := range clients {
					if _, ok := prevClients[name]; !ok {
						// New client, not in prev clients.
						added <- ClientEvent{contextName: name, kc: kc}
					}
				}
				for name, kc := range prevClients {
					if _, ok := clients[name]; !ok {
						// Client removed, in old but not new.
						deleted <- ClientEvent{contextName: name, kc: kc}
					}
				}
				prevClients = clients
			case <-ctx.Done():
				return
			}
		}
	}()
	return added, deleted
}

// discoverRayClusters accepts a kubernetes client as an argument and finds
// all services with the label "ray.io/node-type=head". It returns a slice of
// v1.Service objects.
func discoverRayClusters(clientset *kubernetes.Clientset) ([]v1.Service, error) {
	// Find all services with the ray head node label across all namespaces
	services, err := clientset.CoreV1().Services(v1.NamespaceAll).List(context.Background(), metav1.ListOptions{
		LabelSelector: "ray.io/node-type=head",
	})
	if err != nil {
		return nil, fmt.Errorf("could not list services: %w", err)
	}

	return services.Items, nil
}

func watchRayClusters(ctx context.Context, clusterContext string, kc *kubernetes.Clientset, indexer *ClusterIndexer) {
	initialList, err := kc.CoreV1().Services(v1.NamespaceAll).List(ctx, metav1.ListOptions{
		ResourceVersion: "0",
		LabelSelector:   "ray.io/node-type=head",
	})
	if err != nil {
		log.Printf("Error getting initial service list: %v", err)
		return
	}

	resourceVersion := initialList.ResourceVersion
	for _, service := range initialList.Items {
		log.Printf("Discovered %s from %s", service.GetName(), clusterContext)
		indexer.Insert(makeHandle(clusterContext, kc, service))
	}

	for ctx.Err() == nil {
		watcher, err := kc.CoreV1().Services(v1.NamespaceAll).Watch(ctx, metav1.ListOptions{
			LabelSelector:       "ray.io/node-type=head",
			ResourceVersion:     resourceVersion,
			AllowWatchBookmarks: true,
		})
		if err != nil {
			log.Printf("Error creating watcher for %s: %v. Retrying...", clusterContext, err)
			<-time.After(5 * time.Second)
			continue
		}

		for event := range watcher.ResultChan() {
			service, ok := event.Object.(*v1.Service)
			if !ok {
				continue
			}

			resourceVersion = service.ResourceVersion

			switch event.Type {
			case watch.Added:
				indexer.Insert(makeHandle(clusterContext, kc, *service))
			case watch.Deleted:
				indexer.Delete(clusterContext, string(service.UID))
			default:
				// Other events are a no-op. We assume nothing of value is
				// changing.
			}
		}
		log.Printf("Watch channel for %s closed, restarting watch.", clusterContext)
	}
}

func WatchAllContexts(ctx context.Context, indexer *ClusterIndexer) {
	clientsAdded, clientsDeleted := watchClients(ctx)
	watchClusterStopFns := make(map[string]func())
	for {
		select {
		case ev := <-clientsAdded:
			log.Printf("Discovered kube context %s", ev.contextName)
			watchCtx, cancel := context.WithCancel(ctx)
			watchClusterStopFns[ev.contextName] = cancel
			go func() {
				go watchRayClusters(watchCtx, ev.contextName, ev.kc, indexer)
				<-ctx.Done()
				indexer.DeleteContext(ev.contextName)
			}()
		case ev := <-clientsDeleted:
			log.Printf("Deleted kube context %s", ev.contextName)
			watchClusterStopFns[ev.contextName]()
		case <-ctx.Done():
			return
		}
	}
}

func WatchAll(ctx context.Context, clients map[string]*kubernetes.Clientset, indexer *ClusterIndexer) {
	// In this iteration, all watches share the same context.
	// When we are watching clustercontexts, we will have separate lifetimes.
	for contextName, kc := range clients {
		go func() {
			go watchRayClusters(ctx, contextName, kc, indexer)
			<-ctx.Done()
			indexer.DeleteContext(contextName)
		}()
	}
	<-ctx.Done()
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
