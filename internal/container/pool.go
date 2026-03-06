package container

import (
	"fmt"
	"sync"
)

// PortPool manages a pool of available ports for container port mapping.
type PortPool struct {
	mu        sync.Mutex
	available map[int]bool
	allocated map[int]bool
	start     int
	end       int
}

// NewPortPool creates a new port pool with the given range.
func NewPortPool(start, end int) *PortPool {
	available := make(map[int]bool, end-start+1)
	for i := start; i <= end; i++ {
		available[i] = true
	}
	return &PortPool{
		available: available,
		allocated: make(map[int]bool),
		start:     start,
		end:       end,
	}
}

// Allocate reserves and returns an available port.
func (p *PortPool) Allocate() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for port := range p.available {
		delete(p.available, port)
		p.allocated[port] = true
		return port, nil
	}

	return 0, fmt.Errorf("no available ports in range %d-%d", p.start, p.end)
}

// Release returns a port back to the available pool.
func (p *PortPool) Release(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.allocated[port]; ok {
		delete(p.allocated, port)
		p.available[port] = true
	}
}

// AvailableCount returns the number of available ports.
func (p *PortPool) AvailableCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.available)
}

// AllocatedCount returns the number of allocated ports.
func (p *PortPool) AllocatedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.allocated)
}

// MarkAllocated marks a specific port as allocated (used when recovering state on restart).
func (p *PortPool) MarkAllocated(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if port >= p.start && port <= p.end {
		delete(p.available, port)
		p.allocated[port] = true
	}
}
