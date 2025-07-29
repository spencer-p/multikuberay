package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"strings"

	"k8s.io/client-go/kubernetes"
)

// PageData holds the dynamic data for the template.
type PageData struct {
	ClusterTree map[string]map[string]map[string]map[string]RayClusterHandle
	TargetUID   string
	TargetName  string
}

var (
	portMapper *PortAllocater
	indexer    *ClusterIndexer
)

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
	clients, err := discoverKubeconfigs()
	if err != nil {
		log.Fatalf("failed to get contexts: %v", err)
	}

	ctx := context.Background()
	portMapper = NewPortAllocater(8270)
	indexer = NewClusterIndexer(portMapper)
	go WatchAll(ctx, clients, indexer)

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/dash/{uid}", handleDashboard)
	http.HandleFunc("/proxy/{uid}/", handleProxy)
	http.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "ray.svg")
	})

	log.Println("Server listening on port 8080")
	err = http.ListenAndServe(":8080", nil)
	log.Printf("ListenAndServe: %v", err)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	all := indexer.FuzzyMatch("")
	if len(all) == 0 {
		http.Error(w, "you have no rayclusters", http.StatusNotFound)
		return
	}
	uid := all[0].UID
	http.Redirect(w, r, "/dash/"+uid, http.StatusFound)
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if uid == "" {
		http.Error(w, "no uid", http.StatusBadRequest)
		return
	}

	// Parse the template file
	tmpl, err := template.ParseFiles("index.html")
	if err != nil {
		http.Error(w, "Could not parse template: "+err.Error(), http.StatusInternalServerError)
		return
	}

	targetClusterName := "unknown"

	// Need to generate a tree by project > location > cluster name > raycluster
	clusterTree := indexer.List()
	newTree := make(map[string]map[string]map[string]map[string]RayClusterHandle)
	for contextName, clusters := range clusterTree {
		parts := strings.Split(contextName, "_")
		project, location, clusterName := parts[1], parts[2], parts[3]
		if _, ok := newTree[project]; !ok {
			newTree[project] = make(map[string]map[string]map[string]RayClusterHandle)
		}
		if _, ok := newTree[project][location]; !ok {
			newTree[project][location] = make(map[string]map[string]RayClusterHandle)
		}
		if _, ok := newTree[project][location][clusterName]; !ok {
			newTree[project][location][clusterName] = make(map[string]RayClusterHandle)
		}
		newTree[project][location][clusterName] = clusters

		for _, cluster := range clusters {
			if cluster.UID == uid {
				targetClusterName = cluster.RayClusterName
			}
		}
	}

	// Create the data object.
	data := PageData{
		ClusterTree: newTree,
		TargetUID:   uid,
		TargetName:  targetClusterName,
	}

	// Execute the template with the data
	w.Header().Set("Content-Type", "text/html")
	err = tmpl.Execute(w, data)
	if err != nil {
		log.Printf("Could not execute template: %v", err.Error())
	}
}
