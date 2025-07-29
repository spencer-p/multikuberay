package main

import "sync"

type PortAllocater struct {
	allocStart int

	m         sync.Mutex
	allocated map[int]struct{}
	mapping   map[string]int
}

func (p *PortAllocater) Allocate(uid string) int {
	p.m.Lock()
	defer p.m.Unlock()
	if port, ok := p.mapping[uid]; ok {
		return port
	}

	for port := p.allocStart; ; port++ {
		if _, taken := p.allocated[port]; !taken {
			p.allocated[port] = struct{}{}
			p.mapping[uid] = port
			return port
		}
	}
}

func (p *PortAllocater) Deallocate(uid string) {
	p.m.Lock()
	defer p.m.Unlock()
	port, ok := p.mapping[uid]
	if !ok {
		return
	}
	delete(p.allocated, port)
	delete(p.mapping, uid)
}
