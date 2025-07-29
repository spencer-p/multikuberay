package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func handleProxy(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if uid == "" {
		http.Error(w, "uid not specified", http.StatusBadRequest)
		return
	}

	port, err := portMapper.Mapping(uid)
	if err != nil {
		http.Error(w, "uid not found", http.StatusBadRequest)
		return
	}

	// Create the target URL for the reverse proxy
	targetURL := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("localhost:%d", port),
	}

	// Create the reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.ModifyResponse = addBase(uid)

	// Update the request host to the target's host
	r.Host = targetURL.Host
	r.URL.Host = targetURL.Host
	r.URL.Scheme = targetURL.Scheme
	r.URL.Path = strings.TrimPrefix(r.URL.Path, "/proxy/"+uid)

	log.Printf("Proxy %s to %d", r.URL.Path, port)

	// Serve the request using the proxy
	proxy.ServeHTTP(w, r)
}

func addBase(slug string) func(resp *http.Response) error {
	snippet := []byte(fmt.Sprintf(`<head><base href="/proxy/%s/"/>`, slug))

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
