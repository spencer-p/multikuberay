package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
