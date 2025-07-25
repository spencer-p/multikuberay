package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// PageData holds the dynamic data for the template.
type PageData struct {
	Host string
}

type RayClusterHandle struct {
	kc             *kubernetes.Clientset
	RayClusterName string
	Namespace      string
	ContextName    string
	UID            string
}

func main() {
	// We'll fetch the list of clusters/contexts.
	// In each cluster, we'll fetch all the ray clusters.
	// Clusters are ID'd by cluster name, then namespace, then ray cluster.
	// Alternately, the UUID of the cluster.
	clients, err := discoverKubeconfigs()
	if err != nil {
		log.Fatalf("failed to get contexts: %v", err)
	}
	handles := make(map[string]RayClusterHandle)
	for name, kc := range clients {
		log.Println(name)
		svcs, err := discoverRayClusters(kc)
		if err != nil {
			log.Printf("failed to get heads: %v", err)
			continue
		}
		for _, svc := range svcs {
			handle := makeHandle(name, kc, svc)
			handles[handle.UID] = handle
		}
	}

	// TODO: Start a port forward for every cluster.

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/d/{cluster...}", handleDashboard)
	http.HandleFunc("/proxy/{port}/", handleProxy)

	fmt.Println("Server listening on port 8080")
	http.ListenAndServe(":8080", nil)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	cluster := "foo"
	http.Redirect(w, r, "/d/"+cluster, http.StatusFound)
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {

	// Parse the template file
	tmpl, err := template.ParseFiles("index.html")
	if err != nil {
		http.Error(w, "Could not parse template: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Create the data object
	data := PageData{}

	// Execute the template with the data
	w.Header().Set("Content-Type", "text/html")
	err = tmpl.Execute(w, data)
	if err != nil {
		http.Error(w, "Could not execute template: "+err.Error(), http.StatusInternalServerError)
	}
}

func makeHandle(contextName string, kc *kubernetes.Clientset, svc corev1.Service) RayClusterHandle {
	return RayClusterHandle{
		kc:             kc,
		RayClusterName: svc.GetLabels()["ray.io/cluster"],
		Namespace:      svc.GetNamespace(),
		UID:            svc.GetUID(),
		ContextName:    contextName,
	}
}
