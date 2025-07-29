package main

import (
	"context"
	"fmt"
	"log"
	"maps"
	"strings"
	"sync"
)

type ClusterIndex struct {
	portAlloc PortAllocater

	m sync.RWMutex
	// clusterTree is ray clusters indexed by context name then UUID.
	clusterTree map[string]map[string]RayClusterHandle
	// forwardStopFns stores stop functions for port forwarding by cluster uid.
	forwardStopFns map[string]func()
}

func (c *ClusterIndex) Insert(cluster RayClusterHandle) {
	// Double check the cluster is not already allocated and mapped.
	if c.clusterTree[cluster.ContextName] != nil {
		if _, ok := c.clusterTree[cluster.ContextName][cluster.UID]; ok {
			log.Printf("attempted double insert of %s/%s/%s", cluster.ContextName, cluster.Namespace, cluster.RayClusterName)
			return
		}
	}

	c.m.Lock()
	defer c.m.Unlock()

	port := c.portAlloc.Allocate(cluster.UID)
	cluster.Port = &port
	c.storeCluster(cluster)

	// Start a port forwarder.
	ctx, cancel := context.WithCancel(context.Background())
	go PortForward(ctx, port, cluster)
	c.forwardStopFns[cluster.UID] = cancel
}

func (c *ClusterIndex) Delete(contextName string, uid string) {
	c.m.Lock()
	defer c.m.Unlock()

	c.portAlloc.Deallocate(uid)
	c.forwardStopFns[uid]()
	delete(c.forwardStopFns, uid)

	if _, ok := c.clusterTree[contextName]; !ok {
		// The context map does not exist, nothing to delete.
		return
	}
	delete(c.clusterTree[contextName], uid)
}

func (c *ClusterIndex) DeleteContext(name string) {
	for uid := range c.clusterTree[name] {
		c.Delete(name, uid)
	}
}

func (c *ClusterIndex) List() map[string]map[string]RayClusterHandle {
	c.m.RLock()
	defer c.m.RUnlock()

	result := make(map[string]map[string]RayClusterHandle)
	for name, submap := range c.clusterTree {
		result[name] = maps.Clone(submap)
	}
	return result
}

func (c *ClusterIndex) FuzzyMatch(in string) (uid string, err error) {
	c.m.RLock()
	defer c.m.RUnlock()

	result := ""
	for context := range c.clusterTree {
		for uid, cluster := range c.clusterTree[context] {
			if strings.HasPrefix(cluster.RayClusterName, in) {
				if result == "" {
					result = uid
				} else {
					return "", fmt.Errorf("ambiguous cluster; at least two prefix matches (%q and %q)", result, cluster.RayClusterName)
				}
			}
		}
	}
	return uid, nil
}

func (c *ClusterIndex) storeCluster(handle RayClusterHandle) {
	if _, ok := c.clusterTree[handle.ContextName]; !ok {
		c.clusterTree[handle.ContextName] = make(map[string]RayClusterHandle)
	}
	c.clusterTree[handle.ContextName][handle.UID] = handle
}
