package cache

import (
	"sync"
	"time"
)

// cacheEntry stores the servers list and when it was created
type cacheEntry struct {
	servers   []string
	createdAt time.Time
	lastAccess time.Time // For LRU eviction
}

// Cache stores hash-to-server mappings in memory with TTL and size limits
// The cache accepts paths (which may include extensions) and extracts the hash (first 64 chars) internally
type Cache struct {
	mu       sync.RWMutex
	items    map[string]*cacheEntry
	ttl      time.Duration
	maxSize  int
}

// New creates a new cache instance with TTL and max size
func New(ttl time.Duration, maxSize int) *Cache {
	return &Cache{
		items:   make(map[string]*cacheEntry),
		ttl:     ttl,
		maxSize: maxSize,
	}
}

// extractHash extracts the hash (first 64 characters) from a path
// If the path is shorter than 64 characters, it returns the path as-is
func extractHash(path string) string {
	if len(path) >= 64 {
		return path[:64]
	}
	return path
}

// evictOldest removes expired entries first, then the oldest entry (LRU) if needed
func (c *Cache) evictOldest() {
	if len(c.items) < c.maxSize {
		return
	}

	now := time.Now()
	
	// First, evict all expired entries
	expiredHashes := make([]string, 0)
	for hash, entry := range c.items {
		if c.ttl > 0 && now.Sub(entry.createdAt) > c.ttl {
			expiredHashes = append(expiredHashes, hash)
		}
	}
	
	// Delete all expired entries
	for _, hash := range expiredHashes {
		delete(c.items, hash)
	}
	
	// If we're still at max size after removing expired entries, evict the oldest (LRU)
	if len(c.items) >= c.maxSize {
		// Find the entry with the oldest lastAccess time
		var oldestHash string
		var oldestTime time.Time
		first := true

		for hash, entry := range c.items {
			if first || entry.lastAccess.Before(oldestTime) {
				oldestHash = hash
				oldestTime = entry.lastAccess
				first = false
			}
		}

		if oldestHash != "" {
			delete(c.items, oldestHash)
		}
	}
}

// Add adds or updates a path-to-servers mapping
// The path may include an extension (e.g., "hash.mp4"), but only the hash (first 64 chars) is stored
func (c *Cache) Add(path string, servers []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	hash := extractHash(path)
	now := time.Now()
	
	// If adding a new entry and we're at max size, evict oldest
	if _, exists := c.items[hash]; !exists && len(c.items) >= c.maxSize {
		c.evictOldest()
	}
	
	c.items[hash] = &cacheEntry{
		servers:    servers,
		createdAt:  now,
		lastAccess: now,
	}
}

// Get retrieves the list of servers for a given path
// The path may include an extension, but only the hash (first 64 chars) is used for lookup
// Returns false if the entry doesn't exist or has expired
func (c *Cache) Get(path string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	hash := extractHash(path)
	entry, exists := c.items[hash]
	if !exists {
		return nil, false
	}
	
	// Check if entry has expired
	if c.ttl > 0 && time.Since(entry.createdAt) > c.ttl {
		delete(c.items, hash)
		return nil, false
	}
	
	// Update lastAccess for LRU
	entry.lastAccess = time.Now()
	return entry.servers, true
}

// Remove removes a path from the cache
// The path may include an extension, but only the hash (first 64 chars) is used for removal
func (c *Cache) Remove(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hash := extractHash(path)
	delete(c.items, hash)
}

// AddServer adds a server to the list for a given path if it doesn't already exist
// The path may include an extension, but only the hash (first 64 chars) is used
func (c *Cache) AddServer(path string, server string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	hash := extractHash(path)
	entry, exists := c.items[hash]
	if !exists {
		// Create new entry if it doesn't exist
		now := time.Now()
		if len(c.items) >= c.maxSize {
			c.evictOldest()
		}
		entry = &cacheEntry{
			servers:    []string{server},
			createdAt:  now,
			lastAccess: now,
		}
		c.items[hash] = entry
		return
	}
	
	// Check if entry has expired
	if c.ttl > 0 && time.Since(entry.createdAt) > c.ttl {
		// Entry expired, create new one
		now := time.Now()
		entry = &cacheEntry{
			servers:    []string{server},
			createdAt:  now,
			lastAccess: now,
		}
		c.items[hash] = entry
		return
	}
	
	// Check if server already exists
	for _, s := range entry.servers {
		if s == server {
			entry.lastAccess = time.Now()
			return
		}
	}
	// Add server
	entry.servers = append(entry.servers, server)
	entry.lastAccess = time.Now()
}

// RemoveServer removes a server from the list for a given path
// The path may include an extension, but only the hash (first 64 chars) is used
func (c *Cache) RemoveServer(path string, server string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	hash := extractHash(path)
	entry, exists := c.items[hash]
	if !exists {
		return
	}
	
	// Check if entry has expired
	if c.ttl > 0 && time.Since(entry.createdAt) > c.ttl {
		delete(c.items, hash)
		return
	}
	
	newServers := make([]string, 0, len(entry.servers))
	for _, s := range entry.servers {
		if s != server {
			newServers = append(newServers, s)
		}
	}
	
	if len(newServers) == 0 {
		delete(c.items, hash)
	} else {
		entry.servers = newServers
		entry.lastAccess = time.Now()
	}
}
