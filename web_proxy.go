package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func handleProxy(w http.ResponseWriter, r *http.Request) {
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
		preHead, postHead, ok := bytes.Cut(body, []byte("<head>"))
		if !ok {
			// Weird, we already checked the content type is HTML.
			// So this is HTML with no head? I guess just pass it through.
			resp.Body = io.NopCloser(bytes.NewBuffer(body))
			return nil
		}

		newBody := &bytes.Buffer{}
		newBody.Write(preHead)
		newBody.Write(snippet)
		newBody.Write(postHead)
		resp.Body = io.NopCloser(newBody)
		resp.Header["Content-Length"] = []string{fmt.Sprint(newBody.Len())}
		return nil
	}
}
