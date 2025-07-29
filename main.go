package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"

	"k8s.io/client-go/kubernetes"
)

// PageData holds the dynamic data for the template.
type PageData struct {
	ClusterTree map[string]map[string]RayClusterHandle
}

type RayClusterHandle struct {
	kc             *kubernetes.Clientset
	RayClusterName string
	Namespace      string
	Service        string
	ContextName    string
	UID            string
	Port           *int
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

	ctx := context.Background()
	watcher := ServiceWatcher{}
	go watcher.RunAll(ctx, clients)

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
