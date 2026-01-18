package stats

import (
	"sync"
	"time"
)

// ServerStats tracks statistics for a single upstream server
type ServerStats struct {
	URL string `json:"url"`

	// Operation counts
	UploadsSuccess int64 `json:"uploads_success"`
	UploadsFailure int64 `json:"uploads_failure"`
	Downloads      int64 `json:"downloads"`
	MirrorsSuccess int64 `json:"mirrors_success"`
	MirrorsFailure int64 `json:"mirrors_failure"`
	DeletesSuccess int64 `json:"deletes_success"`
	DeletesFailure int64 `json:"deletes_failure"`
	ListsSuccess   int64 `json:"lists_success"`
	ListsFailure   int64 `json:"lists_failure"`

	// Health tracking
	ConsecutiveFailures int `json:"consecutive_failures"`
	IsHealthy           bool `json:"is_healthy"`
	LastFailureTime     *time.Time `json:"last_failure_time,omitempty"`
	LastSuccessTime     *time.Time `json:"last_success_time,omitempty"`
}

// Stats tracks all statistics
type Stats struct {
	mu          sync.RWMutex
	serverStats map[string]*ServerStats // keyed by server URL
	maxFailures int
}

// New creates a new Stats tracker
func New(maxFailures int) *Stats {
	return &Stats{
		serverStats: make(map[string]*ServerStats),
		maxFailures: maxFailures,
	}
}

// GetOrCreate gets stats for a server or creates if not exists
func (s *Stats) GetOrCreate(serverURL string) *ServerStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	if stats, exists := s.serverStats[serverURL]; exists {
		return stats
	}

	stats := &ServerStats{
		URL:       serverURL,
		IsHealthy: true,
	}
	s.serverStats[serverURL] = stats
	return stats
}

// RecordSuccess records a successful operation for a server
func (s *Stats) RecordSuccess(serverURL string, opType string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stats := s.GetOrCreateLocked(serverURL)
	now := time.Now()
	stats.LastSuccessTime = &now
	stats.ConsecutiveFailures = 0 // Reset consecutive failures on success
	stats.IsHealthy = true

	switch opType {
	case "upload":
		stats.UploadsSuccess++
	case "download":
		stats.Downloads++
	case "mirror":
		stats.MirrorsSuccess++
	case "delete":
		stats.DeletesSuccess++
	case "list":
		stats.ListsSuccess++
	}
}

// RecordFailure records a failed operation for a server
func (s *Stats) RecordFailure(serverURL string, opType string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stats := s.GetOrCreateLocked(serverURL)
	now := time.Now()
	stats.LastFailureTime = &now
	stats.ConsecutiveFailures++

	// Mark unhealthy if consecutive failures exceed threshold
	if stats.ConsecutiveFailures >= s.maxFailures {
		stats.IsHealthy = false
	}

	switch opType {
	case "upload":
		stats.UploadsFailure++
	case "mirror":
		stats.MirrorsFailure++
	case "delete":
		stats.DeletesFailure++
	case "list":
		stats.ListsFailure++
	}
}

// GetOrCreateLocked gets or creates stats (must be called with lock held)
func (s *Stats) GetOrCreateLocked(serverURL string) *ServerStats {
	if stats, exists := s.serverStats[serverURL]; exists {
		return stats
	}

	stats := &ServerStats{
		URL:       serverURL,
		IsHealthy: true,
	}
	s.serverStats[serverURL] = stats
	return stats
}

// GetAll returns a copy of all server statistics
func (s *Stats) GetAll() map[string]*ServerStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]*ServerStats, len(s.serverStats))
	for url, stats := range s.serverStats {
		// Create a copy to avoid race conditions
		statsCopy := *stats
		result[url] = &statsCopy
	}
	return result
}

// GetHealthyCount returns the number of healthy servers
func (s *Stats) GetHealthyCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, stats := range s.serverStats {
		if stats.IsHealthy {
			count++
		}
	}
	return count
}

// GetHealthStatus returns health status for all servers
func (s *Stats) GetHealthStatus() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]bool, len(s.serverStats))
	for url, stats := range s.serverStats {
		result[url] = stats.IsHealthy
	}
	return result
}

// InitializeServers initializes stats for all given server URLs, marking them as healthy
// This should be called at startup to ensure all servers start as healthy
func (s *Stats) InitializeServers(serverURLs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, url := range serverURLs {
		// Only initialize if not already present (don't overwrite existing stats)
		if _, exists := s.serverStats[url]; !exists {
			s.serverStats[url] = &ServerStats{
				URL:       url,
				IsHealthy: true,
			}
		}
	}
}
