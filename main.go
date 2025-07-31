package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"k8s.io/client-go/kubernetes"
)

// PageData holds the dynamic data for the template.
type PageData struct {
	ClusterTree map[string]map[string]map[string]map[string]RayClusterHandle
	TargetUID   string
	TargetName  string
	IframePath  string
}

var (
	portMapper    *PortAllocater
	indexer       *ClusterIndexer
	indexTemplate *template.Template
	//go:embed index.html
	indexBytes string
	//go:embed ray.svg
	faviconBytes []byte
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

func init() {
	var err error
	indexTemplate, err = template.New("index").Parse(indexBytes)
	if err != nil {
		log.Fatalf("failed to parse index.html: %v", err)
	}
}

func main() {
	if len(os.Args) == 1 {
		serveMain()
		return
	}
	switch os.Args[1] {
	case "serve":
		serveMain()
	case "run":
		runMain()
	}
}

func serveMain() {
	ctx := context.Background()
	portMapper = NewPortAllocater(8270)
	indexer = NewClusterIndexer(portMapper)
	go WatchAllContexts(ctx, indexer)

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/dash/{uid}", handleDashboard)
	http.HandleFunc("/proxy/{uid}/", handleProxy)
	http.HandleFunc("/api/v1/match", handleMatch)
	http.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(faviconBytes)
	})

	log.Println("Server listening on port 8080")
	err := http.ListenAndServe(":8080", nil)
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
		IframePath:  findIframePath(r),
	}

	// Execute the template with the data
	w.Header().Set("Content-Type", "text/html")
	err := indexTemplate.Execute(w, data)
	if err != nil {
		log.Printf("Could not execute template: %v", err.Error())
	}
}

func findIframePath(r *http.Request) string {
	cookie, err := r.Cookie("last_known_iframe_location")
	if err != nil {
		return "/"
	}
	path, err := url.PathUnescape(cookie.Value)
	if err != nil {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		log.Printf("last iframe path %q does not seem right", path)
		return "/"
	}
	return path
}

func handleMatch(w http.ResponseWriter, r *http.Request) {
	clusterName := r.URL.Query().Get("prefix")
	matches := indexer.FuzzyMatch(clusterName)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(matches); err != nil {
		http.Error(w, "Failed to encode matches to json", http.StatusInternalServerError)
	}
}

func runMain() {
	// The command is something like "run my-cluster -- python3 myfile.py"
	// The cluster name is optional.
	// Find the cluster name argument (if present) and then store the rest of
	// the command after -- in another var.
	var prefix string
	var command []string
	if len(os.Args) > 2 {
		if os.Args[2] != "--" {
			prefix = os.Args[2]
		}
	}

	for i, arg := range os.Args {
		if arg == "--" {
			command = os.Args[i+1:]
			break
		}
	}

	if len(command) == 0 {
		fmt.Fprintf(os.Stderr, "no command specified\n")
		os.Exit(1)
	}

	// Fetch the match handler with the given prefix on localhost port 8080.
	resp, err := http.Get("http://localhost:8080/api/v1/match?prefix=" + prefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to multikuberay server: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var matches []RayClusterHandle
	if err := json.NewDecoder(resp.Body).Decode(&matches); err != nil {
		fmt.Fprintf(os.Stderr, "failed to decode response: %v\n", err)
		os.Exit(1)
	}

	// If there are multiple matches, print out the multiple matches and exit
	// with an error.
	if len(matches) == 0 {
		fmt.Fprintf(os.Stderr, "no clusters found\n")
		os.Exit(1)
	}
	if len(matches) > 1 {
		fmt.Fprintf(os.Stderr, "multiple clusters found:\n")
		for _, match := range matches {
			fmt.Fprintf(os.Stderr, "  %s\n", match.RayClusterName)
		}
		os.Exit(1)
	}

	// If there is one match, identify its target port.
	match := matches[0]
	if match.Port == nil {
		fmt.Fprintf(os.Stderr, "cluster has no port assigned\n")
		os.Exit(1)
	}
	port := *match.Port

	// Set up a command to run the user's command.
	// Set the env var RAY_ADDRESS=http://localhost:TARGET_PORT.
	// Then exec the command.
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("RAY_ADDRESS=http://localhost:%d", port))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	err = cmd.Run()
	exitErr := &exec.ExitError{}
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "command failed: %v\n", err)
		os.Exit(1)
	}
}
