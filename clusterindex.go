package main

import (
	"context"
	"log"
	"maps"
	"strings"
	"sync"
)

type ClusterIndexer struct {
	portAlloc *PortAllocater

	m sync.RWMutex
	// clusterTree is ray clusters indexed by context name then UUID.
	clusterTree map[string]map[string]RayClusterHandle
	// forwardStopFns stores stop functions for port forwarding by cluster uid.
	forwardStopFns map[string]func()
}

func NewClusterIndexer(portAllocater *PortAllocater) *ClusterIndexer {
	return &ClusterIndexer{
		portAlloc:      portAllocater,
		clusterTree:    make(map[string]map[string]RayClusterHandle),
		forwardStopFns: make(map[string]func()),
	}
}

func (c *ClusterIndexer) Insert(ctx context.Context, cluster RayClusterHandle) {
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
	ctx, cancel := context.WithCancel(ctx)
	log.Printf("Forwarding %s on port %d", cluster.RayClusterName, port)
	go PortForward(ctx, port, cluster)
	c.forwardStopFns[cluster.UID] = cancel
}

func (c *ClusterIndexer) Delete(contextName string, uid string) {
	c.m.Lock()
	defer c.m.Unlock()

	log.Printf("Deallocate %s from %s", uid, contextName)
	c.portAlloc.Deallocate(uid)
	c.forwardStopFns[uid]()
	delete(c.forwardStopFns, uid)

	if _, ok := c.clusterTree[contextName]; !ok {
		// The context map does not exist, nothing to delete.
		return
	}
	delete(c.clusterTree[contextName], uid)
}

func (c *ClusterIndexer) DeleteContext(name string) {
	// Use the cluster specific delete func to make sure we deallocate ports and
	// clean up the port forwarding go routines.
	for uid := range c.clusterTree[name] {
		c.Delete(name, uid)
	}

	// Then (safely) remove the empty map.
	c.m.Lock()
	defer c.m.Unlock()
	delete(c.clusterTree, name)
}

func (c *ClusterIndexer) List() map[string]map[string]RayClusterHandle {
	c.m.RLock()
	defer c.m.RUnlock()

	result := make(map[string]map[string]RayClusterHandle)
	for name, submap := range c.clusterTree {
		result[name] = maps.Clone(submap)
	}
	return result
}

func (c *ClusterIndexer) FuzzyMatch(in string) []RayClusterHandle {
	c.m.RLock()
	defer c.m.RUnlock()

	var result []RayClusterHandle
	for context := range c.clusterTree {
		for _, cluster := range c.clusterTree[context] {
			if strings.HasPrefix(cluster.RayClusterName, in) {
				result = append(result, cluster)
			}
		}
	}
	return result
}

func (c *ClusterIndexer) storeCluster(handle RayClusterHandle) {
	if _, ok := c.clusterTree[handle.ContextName]; !ok {
		c.clusterTree[handle.ContextName] = make(map[string]RayClusterHandle)
	}
	c.clusterTree[handle.ContextName][handle.UID] = handle
}
