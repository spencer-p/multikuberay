package main

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// PageData holds the dynamic data for the template.
type PageData struct {
	Host string
}

func main() {
	http.HandleFunc("/proxy/{port}/", func(w http.ResponseWriter, r *http.Request) {
		port := r.PathValue("port")
		if port == "" {
			http.Error(w, "Port not specified", http.StatusBadRequest)
			return
		}

		// Create the target URL for the reverse proxy
		targetURL := &url.URL{
			Scheme: "http",
			Host:   "localhost:" + port,
		}

		// Create the reverse proxy
		proxy := httputil.NewSingleHostReverseProxy(targetURL)
		proxy.ModifyResponse = addBase(port)

		// Update the request host to the target's host
		r.Host = targetURL.Host
		r.URL.Host = targetURL.Host
		r.URL.Scheme = targetURL.Scheme
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/proxy/"+port)

		fmt.Println("proxy to", r.URL.Path)

		// Serve the request using the proxy
		proxy.ServeHTTP(w, r)
	})

	// Handler for the main page
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		host := "spencerjp.c.googlers.com"

		// Parse the template file
		tmpl, err := template.ParseFiles("index.html")
		if err != nil {
			http.Error(w, "Could not parse template: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Create the data object
		data := PageData{
			Host: host,
		}

		// Execute the template with the data
		w.Header().Set("Content-Type", "text/html")
		err = tmpl.Execute(w, data)
		if err != nil {
			http.Error(w, "Could not execute template: "+err.Error(), http.StatusInternalServerError)
		}
	})

	fmt.Println("Server listening on port 8080")
	http.ListenAndServe(":8080", nil)
}

// gfeHost extracts the original host from the GFE frontline info header.
func gfeHost(h http.Header) (string, error) {
	raw, ok := h["X-Google-Gfe-Frontline-Info"]
	if !ok {
		return "", errors.New("no gfe frontline info")
	}
	// The header can contain multiple comma-separated key-value pairs.
	// We are looking for "original_host=...".
	for _, part := range strings.Split(raw[0], ",") {
		trimmedPart := strings.TrimSpace(part)
		if key, value, ok := strings.Cut(trimmedPart, "="); ok && key == "original_host" {
			return value, nil
		}
	}
	return "", errors.New("did not find gfe frontline info original_host")
}

func addBase(port string) func(resp *http.Response) error {
	snippet := []byte(fmt.Sprintf(`<head><base href="/proxy/%s/"/>`, port))

	return func(resp *http.Response) error {
		if typ := resp.Header.Get("Content-Type"); !strings.HasPrefix(typ, "text/html") {
			// Skip modifying non-HTML content.
			return nil
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		resp.Body.Close()
		newBody := &bytes.Buffer{}
		preHead, postHead, ok := bytes.Cut(body, []byte("<head>"))
		if !ok {
			// Weird, we already checked the content type is HTML.
			// So this is HTML with no head? I guess just pass it through.
			resp.Body = io.NopCloser(bytes.NewBuffer(body))
			return nil
		}

		newBody.Write(preHead)
		newBody.Write(snippet)
		newBody.Write(postHead)
		resp.Body = io.NopCloser(newBody)
		resp.Header["Content-Length"] = []string{fmt.Sprint(newBody.Len())}
		return nil
	}
}
