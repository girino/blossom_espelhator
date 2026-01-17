package cache

import (
	"sync"
)

// Cache stores hash-to-server mappings in memory
type Cache struct {
	mu    sync.RWMutex
	items map[string][]string
}

// New creates a new cache instance
func New() *Cache {
	return &Cache{
		items: make(map[string][]string),
	}
}

// Add adds or updates a hash-to-servers mapping
func (c *Cache) Add(hash string, servers []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[hash] = servers
}

// Get retrieves the list of servers for a given hash
func (c *Cache) Get(hash string) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	servers, exists := c.items[hash]
	return servers, exists
}

// Remove removes a hash from the cache
func (c *Cache) Remove(hash string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, hash)
}

// AddServer adds a server to the list for a given hash if it doesn't already exist
func (c *Cache) AddServer(hash string, server string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	servers := c.items[hash]
	// Check if server already exists
	for _, s := range servers {
		if s == server {
			return
		}
	}
	// Add server
	c.items[hash] = append(servers, server)
}

// RemoveServer removes a server from the list for a given hash
func (c *Cache) RemoveServer(hash string, server string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	servers := c.items[hash]
	newServers := make([]string, 0, len(servers))
	for _, s := range servers {
		if s != server {
			newServers = append(newServers, s)
		}
	}
	
	if len(newServers) == 0 {
		delete(c.items, hash)
	} else {
		c.items[hash] = newServers
	}
}
