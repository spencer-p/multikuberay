package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"time"
)

func PortForward(ctx context.Context, port int, handle RayClusterHandle) {
	kubectlArgs := []string{
		"--context", handle.ContextName,
		"port-forward",
		"-n", handle.Namespace,
		"service/" + handle.Service,
		fmt.Sprintf("%d:8265", port),
	}

	const initBackoff = 10 * time.Millisecond
	const maxBackoff = 30 * time.Second
	backoff := initBackoff
	lastErr := time.Now()

	for ctx.Err() == nil {
		portforwardCmd := exec.CommandContext(ctx, "kubectl", kubectlArgs...)
		out, err := portforwardCmd.CombinedOutput()
		if err == nil {
			continue
		}

		log.Printf("port forward %s/%s/%s failed: %v", handle.ContextName, handle.Namespace, handle.RayClusterName, err)
		log.Printf("port forward output: %s", out)
		if ctx.Err() != nil {
			return
		}

		now := time.Now()
		if now.Sub(lastErr) > 1*time.Minute {
			backoff = initBackoff
		} else {
			backoff = min(2*backoff, maxBackoff)
		}
		lastErr = now

		log.Printf("try connect to %s/%s/%s in %s", handle.ContextName, handle.Namespace, handle.RayClusterName, backoff)
		<-time.After(backoff)
	}
}
