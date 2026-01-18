package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/girino/blossom_espelhator/internal/client"
	"github.com/girino/blossom_espelhator/internal/config"
)

// Manager manages upstream Blossom servers
type Manager struct {
	clients            []*client.Client
	serverURLs         []string
	serverCapabilities []serverCapabilities // Capabilities for each server (indexed same as clients/serverURLs)
	minUploadServers   int
	redirectStrategy   string
	roundRobinIndex    int
	roundRobinMutex    sync.Mutex
	timeout            time.Duration
	verbose            bool
}

// serverCapabilities stores which endpoints a server supports
type serverCapabilities struct {
	SupportsMirror     bool
	SupportsUploadHead bool
}

// UploadResult represents the result of an upload to a single server
type UploadResult struct {
	ServerURL    string
	Success      bool
	Error        error
	StatusCode   int    // HTTP status code if error occurred (0 if success)
	ResponseBody []byte // Response body from upstream server (if success)
}

// UploadError represents an upload error with HTTP status code
type UploadError struct {
	StatusCode int
	Message    string
}

func (e *UploadError) Error() string {
	return e.Message
}

// New creates a new upstream manager
func New(cfg *config.Config, verbose bool) (*Manager, error) {
	if len(cfg.UpstreamServers) == 0 {
		return nil, fmt.Errorf("no upstream servers configured")
	}

	clients := make([]*client.Client, 0, len(cfg.UpstreamServers))
	serverURLs := make([]string, 0, len(cfg.UpstreamServers))
	capabilities := make([]serverCapabilities, 0, len(cfg.UpstreamServers))

	for _, server := range cfg.UpstreamServers {
		cl := client.New(server.URL, cfg.Server.Timeout, verbose)
		clients = append(clients, cl)
		serverURLs = append(serverURLs, server.URL)

		// Store capabilities (pointers default to nil if not set, but we set defaults in config.Load())
		cap := serverCapabilities{
			SupportsMirror:     server.SupportsMirror != nil && *server.SupportsMirror,
			SupportsUploadHead: server.SupportsUploadHead != nil && *server.SupportsUploadHead,
		}
		capabilities = append(capabilities, cap)
	}

	if verbose {
		log.Printf("[DEBUG] Upstream manager initialized with %d servers, min_upload_servers=%d, strategy=%s",
			len(serverURLs), cfg.Server.MinUploadServers, cfg.Server.RedirectStrategy)
		for i, url := range serverURLs {
			log.Printf("[DEBUG]   Upstream server %d: %s (mirror=%t, upload_head=%t)",
				i+1, url, capabilities[i].SupportsMirror, capabilities[i].SupportsUploadHead)
		}
	}

	return &Manager{
		clients:            clients,
		serverURLs:         serverURLs,
		serverCapabilities: capabilities,
		minUploadServers:   cfg.Server.MinUploadServers,
		redirectStrategy:   cfg.Server.RedirectStrategy,
		timeout:            cfg.Server.Timeout,
		verbose:            verbose,
	}, nil
}

// UploadResultWithResponse contains a successful server URL and its response body
type UploadResultWithResponse struct {
	ServerURL    string
	ResponseBody []byte
}

// UploadParallel uploads a blob to multiple upstream servers in parallel
// Returns the list of successful servers with their response bodies and an error if fewer than minUploadServers succeeded
func (m *Manager) UploadParallel(ctx context.Context, body io.Reader, contentType string, headers map[string]string) ([]UploadResultWithResponse, error) {
	if m.verbose {
		log.Printf("[DEBUG] UploadParallel: starting parallel upload to %d servers", len(m.clients))
		log.Printf("[DEBUG] UploadParallel: content-type=%s, headers=%v", contentType, headers)
	}

	// Create a context with timeout
	uploadCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	// Channel to collect results
	resultChan := make(chan UploadResult, len(m.clients))

	// Read body into memory so we can reuse it for multiple uploads
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}

	if m.verbose {
		log.Printf("[DEBUG] UploadParallel: read %d bytes from request body", len(bodyBytes))
	}

	// Launch parallel uploads
	var wg sync.WaitGroup
	for i, cl := range m.clients {
		wg.Add(1)
		go func(idx int, c *client.Client, url string) {
			defer wg.Done()

			if m.verbose {
				log.Printf("[DEBUG] UploadParallel: starting upload to server %d: %s", idx+1, url)
			}

			// Create a new reader for each upload
			reader := bytes.NewReader(bodyBytes)

			uploadStart := time.Now()
			responseBody, err := c.Upload(uploadCtx, reader, contentType, headers)
			uploadDuration := time.Since(uploadStart)

			statusCode := 0
			if err != nil {
				if httpErr, ok := err.(*client.HTTPError); ok {
					statusCode = httpErr.StatusCode
				}
			}

			result := UploadResult{
				ServerURL:    url,
				Success:      err == nil,
				Error:        err,
				StatusCode:   statusCode,
				ResponseBody: responseBody,
			}

			if m.verbose {
				if err == nil {
					log.Printf("[DEBUG] UploadParallel: server %d (%s) succeeded in %v", idx+1, url, uploadDuration)
				} else {
					log.Printf("[DEBUG] UploadParallel: server %d (%s) failed in %v: %v", idx+1, url, uploadDuration, err)
				}
			}

			resultChan <- result
		}(i, cl, m.serverURLs[i])
	}

	// Wait for all uploads to complete
	wg.Wait()
	close(resultChan)

	// Collect successful uploads and errors
	successfulServers := make([]UploadResultWithResponse, 0)
	errorDetails := make([]string, 0)
	allStatusCodes := make([]int, 0)

	for result := range resultChan {
		if result.Success {
			successfulServers = append(successfulServers, UploadResultWithResponse{
				ServerURL:    result.ServerURL,
				ResponseBody: result.ResponseBody,
			})
		} else if result.Error != nil {
			errorDetails = append(errorDetails, fmt.Sprintf("%s: %v", result.ServerURL, result.Error))

			// Track all status codes from errors
			if result.StatusCode > 0 {
				allStatusCodes = append(allStatusCodes, result.StatusCode)
			}
		}
	}

	// Check if we have enough successful uploads
	if m.verbose {
		log.Printf("[DEBUG] UploadParallel: completed - %d succeeded, %d failed", len(successfulServers), len(errorDetails))
		if len(successfulServers) > 0 {
			log.Printf("[DEBUG] UploadParallel: successful servers: %v", successfulServers)
		}
		if len(errorDetails) > 0 {
			log.Printf("[DEBUG] UploadParallel: failed servers: %v", errorDetails)
		}
	}

	if len(successfulServers) < m.minUploadServers {
		errMsg := fmt.Sprintf("only %d servers succeeded, need at least %d", len(successfulServers), m.minUploadServers)
		if len(errorDetails) > 0 {
			errMsg += fmt.Sprintf(". Errors: %v", errorDetails)
		}

		// If we have status codes from upstream errors, use the lowest one
		if len(allStatusCodes) > 0 {
			// Find the lowest status code
			minStatusCode := allStatusCodes[0]
			for _, code := range allStatusCodes[1:] {
				if code < minStatusCode {
					minStatusCode = code
				}
			}

			if m.verbose {
				log.Printf("[DEBUG] UploadParallel: using lowest upstream status code %d (from %v)", minStatusCode, allStatusCodes)
			}
			return successfulServers, &UploadError{
				StatusCode: minStatusCode,
				Message:    errMsg,
			}
		}

		// No status codes available - return 500
		return successfulServers, fmt.Errorf(errMsg)
	}

	if m.verbose {
		log.Printf("[DEBUG] UploadParallel: upload successful, minimum requirement met (%d >= %d)", len(successfulServers), m.minUploadServers)
	}

	return successfulServers, nil
}

// MirrorParallel sends mirror requests to multiple upstream servers in parallel (BUD-04)
// Only sends to servers that support mirror capability
// Returns the list of successful servers with their response bodies and an error if fewer than minUploadServers succeeded
func (m *Manager) MirrorParallel(ctx context.Context, body io.Reader, contentType string, headers map[string]string) ([]UploadResultWithResponse, error) {
	// Filter servers by mirror capability
	mirrorCapableIndices := make([]int, 0)
	for i, cap := range m.serverCapabilities {
		if cap.SupportsMirror {
			mirrorCapableIndices = append(mirrorCapableIndices, i)
		}
	}

	if len(mirrorCapableIndices) == 0 {
		return nil, fmt.Errorf("no upstream servers support mirror endpoint")
	}

	if m.verbose {
		log.Printf("[DEBUG] MirrorParallel: starting parallel mirror requests to %d/%d servers (filtered by capability)",
			len(mirrorCapableIndices), len(m.clients))
		log.Printf("[DEBUG] MirrorParallel: content-type=%s, headers=%v", contentType, headers)
	}

	// Create a context with timeout
	mirrorCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	// Channel to collect results
	resultChan := make(chan UploadResult, len(mirrorCapableIndices))

	// Read body into memory so we can reuse it for multiple mirror requests
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}

	if m.verbose {
		log.Printf("[DEBUG] MirrorParallel: read %d bytes from request body", len(bodyBytes))
	}

	// Launch parallel mirror requests (only to capable servers)
	var wg sync.WaitGroup
	for _, idx := range mirrorCapableIndices {
		wg.Add(1)
		cl := m.clients[idx]
		url := m.serverURLs[idx]
		go func(serverIdx int, c *client.Client, serverURL string) {
			defer wg.Done()

			if m.verbose {
				log.Printf("[DEBUG] MirrorParallel: starting mirror request to server: %s", serverURL)
			}

			// Create a new reader for each mirror request
			reader := bytes.NewReader(bodyBytes)

			mirrorStart := time.Now()
			responseBody, err := c.Mirror(mirrorCtx, reader, contentType, headers)
			mirrorDuration := time.Since(mirrorStart)

			statusCode := 0
			if err != nil {
				if httpErr, ok := err.(*client.HTTPError); ok {
					statusCode = httpErr.StatusCode
				}
			}

			result := UploadResult{
				ServerURL:    url,
				Success:      err == nil,
				Error:        err,
				StatusCode:   statusCode,
				ResponseBody: responseBody,
			}

			if m.verbose {
				if err == nil {
					log.Printf("[DEBUG] MirrorParallel: server %s succeeded in %v", serverURL, mirrorDuration)
				} else {
					log.Printf("[DEBUG] MirrorParallel: server %s failed in %v: %v", serverURL, mirrorDuration, err)
				}
			}

			resultChan <- result
		}(idx, cl, url)
	}

	// Wait for all mirror requests to complete
	wg.Wait()
	close(resultChan)

	// Collect results
	successfulServers := make([]UploadResultWithResponse, 0)
	errorDetails := make([]string, 0)
	allStatusCodes := make([]int, 0)

	for result := range resultChan {
		if result.Success {
			successfulServers = append(successfulServers, UploadResultWithResponse{
				ServerURL:    result.ServerURL,
				ResponseBody: result.ResponseBody,
			})
		} else {
			errorDetails = append(errorDetails, fmt.Sprintf("%s: %v", result.ServerURL, result.Error))
			if result.StatusCode > 0 {
				allStatusCodes = append(allStatusCodes, result.StatusCode)
			}
		}
	}

	if m.verbose {
		attemptedCount := len(mirrorCapableIndices)
		log.Printf("[DEBUG] MirrorParallel: successful servers: %d/%d (attempted %d out of %d total)",
			len(successfulServers), attemptedCount, attemptedCount, len(m.clients))
		if len(errorDetails) > 0 {
			log.Printf("[DEBUG] MirrorParallel: failed servers: %v", errorDetails)
		}
		if len(successfulServers) > 0 {
			serverURLs := make([]string, 0, len(successfulServers))
			for _, srv := range successfulServers {
				serverURLs = append(serverURLs, srv.ServerURL)
			}
			log.Printf("[DEBUG] MirrorParallel: succeeded on servers: %v", serverURLs)
		}
	}

	// Check if we have enough successful servers
	if len(successfulServers) < m.minUploadServers {
		errMsg := fmt.Sprintf("only %d servers succeeded, need at least %d", len(successfulServers), m.minUploadServers)
		if len(errorDetails) > 0 {
			errMsg += fmt.Sprintf(". Errors: %v", errorDetails)
		}

		// If we have status codes from upstream errors, use the lowest one
		if len(allStatusCodes) > 0 {
			minStatusCode := allStatusCodes[0]
			for _, code := range allStatusCodes[1:] {
				if code < minStatusCode {
					minStatusCode = code
				}
			}

			if m.verbose {
				log.Printf("[DEBUG] MirrorParallel: using lowest upstream status code %d (from %v)", minStatusCode, allStatusCodes)
			}
			return successfulServers, &UploadError{
				StatusCode: minStatusCode,
				Message:    errMsg,
			}
		}

		// No status codes available - return 500
		return successfulServers, fmt.Errorf("%s", errMsg)
	}

	return successfulServers, nil
}

// SelectServer selects a server from successful uploads based on the configured strategy
func (m *Manager) SelectServer(availableServers []UploadResultWithResponse) (*UploadResultWithResponse, error) {
	if len(availableServers) == 0 {
		return nil, fmt.Errorf("no available servers")
	}

	var selected *UploadResultWithResponse
	switch m.redirectStrategy {
	case "round_robin":
		selected = m.selectRoundRobinWithResponse(availableServers)
	case "random":
		selected = m.selectRandomWithResponse(availableServers)
	case "health_based":
		// For now, fall back to round-robin
		// Health checking can be added later
		selected = m.selectRoundRobinWithResponse(availableServers)
	default:
		// Default to round-robin
		selected = m.selectRoundRobinWithResponse(availableServers)
	}

	if m.verbose {
		log.Printf("[DEBUG] SelectServer: strategy=%s, available=%d servers, selected=%s", m.redirectStrategy, len(availableServers), selected.ServerURL)
	}

	return selected, nil
}

// selectRoundRobinWithResponse selects a server using round-robin strategy
func (m *Manager) selectRoundRobinWithResponse(availableServers []UploadResultWithResponse) *UploadResultWithResponse {
	m.roundRobinMutex.Lock()
	defer m.roundRobinMutex.Unlock()

	selected := &availableServers[m.roundRobinIndex%len(availableServers)]
	m.roundRobinIndex++
	return selected
}

// selectRandomWithResponse selects a random server
func (m *Manager) selectRandomWithResponse(availableServers []UploadResultWithResponse) *UploadResultWithResponse {
	selected := &availableServers[rand.Intn(len(availableServers))]
	return selected
}

// SelectServer selects a server URL for redirect based on the configured strategy (legacy method for download)
func (m *Manager) SelectServerURL(availableServers []string) (string, error) {
	if len(availableServers) == 0 {
		return "", fmt.Errorf("no available servers")
	}

	var selected string
	switch m.redirectStrategy {
	case "round_robin":
		selected = m.selectRoundRobin(availableServers)
	case "random":
		selected = m.selectRandom(availableServers)
	case "health_based":
		// For now, fall back to round-robin
		// Health checking can be added later
		selected = m.selectRoundRobin(availableServers)
	default:
		// Default to round-robin
		selected = m.selectRoundRobin(availableServers)
	}

	if m.verbose {
		log.Printf("[DEBUG] SelectServerURL: strategy=%s, available=%d servers, selected=%s", m.redirectStrategy, len(availableServers), selected)
	}

	return selected, nil
}

// selectRoundRobin selects a server using round-robin strategy (legacy for downloads)
func (m *Manager) selectRoundRobin(availableServers []string) string {
	m.roundRobinMutex.Lock()
	defer m.roundRobinMutex.Unlock()

	server := availableServers[m.roundRobinIndex%len(availableServers)]
	m.roundRobinIndex++
	return server
}

// selectRandom selects a random server (legacy for downloads)
func (m *Manager) selectRandom(availableServers []string) string {
	return availableServers[rand.Intn(len(availableServers))]
}

// GetClient returns a client for a specific server URL
func (m *Manager) GetClient(serverURL string) (*client.Client, error) {
	for i, url := range m.serverURLs {
		if url == serverURL {
			return m.clients[i], nil
		}
	}
	return nil, fmt.Errorf("server not found: %s", serverURL)
}

// GetAllClients returns all clients
func (m *Manager) GetAllClients() []*client.Client {
	return m.clients
}

// GetServerURLs returns all server URLs
func (m *Manager) GetServerURLs() []string {
	return m.serverURLs
}

// CheckHashOnServers checks all upstream servers in parallel to see which ones have the blob
// Returns list of server URLs that have the blob
func (m *Manager) CheckHashOnServers(ctx context.Context, hash string) []string {
	if m.verbose {
		log.Printf("[DEBUG] CheckHashOnServers: checking hash %s on %d servers", hash, len(m.clients))
	}

	// Channel to collect results
	resultChan := make(chan struct {
		ServerURL string
		HasBlob   bool
	}, len(m.clients))

	// Launch parallel HEAD requests
	var wg sync.WaitGroup
	for i, cl := range m.clients {
		wg.Add(1)
		go func(idx int, c *client.Client, url string) {
			defer wg.Done()

			if m.verbose {
				log.Printf("[DEBUG] CheckHashOnServers: checking server %d: %s", idx+1, url)
			}

			_, err := c.Download(ctx, hash)
			hasBlob := err == nil

			resultChan <- struct {
				ServerURL string
				HasBlob   bool
			}{
				ServerURL: url,
				HasBlob:   hasBlob,
			}

			if m.verbose {
				if hasBlob {
					log.Printf("[DEBUG] CheckHashOnServers: server %d (%s) has the blob", idx+1, url)
				} else {
					log.Printf("[DEBUG] CheckHashOnServers: server %d (%s) does not have the blob", idx+1, url)
				}
			}
		}(i, cl, m.serverURLs[i])
	}

	// Wait for all checks to complete
	wg.Wait()
	close(resultChan)

	// Collect servers that have the blob
	serversWithBlob := make([]string, 0)
	for result := range resultChan {
		if result.HasBlob {
			serversWithBlob = append(serversWithBlob, result.ServerURL)
		}
	}

	if m.verbose {
		log.Printf("[DEBUG] CheckHashOnServers: hash found on %d servers: %v", len(serversWithBlob), serversWithBlob)
	}

	return serversWithBlob
}

// UploadPreflightResult represents the result of an upload preflight check
type UploadPreflightResult struct {
	ServerURL  string
	Accepted   bool
	StatusCode int
	XReason    string // X-Reason header if rejected
	Error      error
}

// UploadPreflightParallel performs HEAD /upload on all upstream servers in parallel to check upload requirements (BUD-06)
// Only sends to servers that support HEAD /upload capability
// Returns the list of servers that would accept the upload
func (m *Manager) UploadPreflightParallel(ctx context.Context, headers map[string]string) ([]UploadPreflightResult, error) {
	// Filter servers by upload_head capability
	uploadHeadCapableIndices := make([]int, 0)
	for i, cap := range m.serverCapabilities {
		if cap.SupportsUploadHead {
			uploadHeadCapableIndices = append(uploadHeadCapableIndices, i)
		}
	}

	if len(uploadHeadCapableIndices) == 0 {
		return nil, fmt.Errorf("no upstream servers support HEAD /upload endpoint")
	}

	if m.verbose {
		log.Printf("[DEBUG] UploadPreflightParallel: checking upload requirements on %d/%d servers (filtered by capability)",
			len(uploadHeadCapableIndices), len(m.clients))
		log.Printf("[DEBUG] UploadPreflightParallel: headers=%v", headers)
	}

	// Create a context with timeout
	preflightCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	// Channel to collect results
	resultChan := make(chan UploadPreflightResult, len(uploadHeadCapableIndices))

	// Launch parallel HEAD /upload requests (only to capable servers)
	var wg sync.WaitGroup
	for _, idx := range uploadHeadCapableIndices {
		wg.Add(1)
		cl := m.clients[idx]
		url := m.serverURLs[idx]
		go func(serverIdx int, c *client.Client, serverURL string) {
			defer wg.Done()

			if m.verbose {
				log.Printf("[DEBUG] UploadPreflightParallel: checking server: %s", serverURL)
			}

			resp, err := c.HeadUpload(preflightCtx, headers)
			if err != nil {
				if m.verbose {
					log.Printf("[DEBUG] UploadPreflightParallel: server %s failed: %v", serverURL, err)
				}
				resultChan <- UploadPreflightResult{
					ServerURL:  serverURL,
					Accepted:   false,
					StatusCode: 0,
					Error:      err,
				}
				return
			}
			defer resp.Body.Close()

			accepted := resp.StatusCode == http.StatusOK
			xReason := resp.Header.Get("X-Reason")

			if m.verbose {
				if accepted {
					log.Printf("[DEBUG] UploadPreflightParallel: server %s accepted (status=%d)", serverURL, resp.StatusCode)
				} else {
					log.Printf("[DEBUG] UploadPreflightParallel: server %s rejected (status=%d, X-Reason=%s)", serverURL, resp.StatusCode, xReason)
				}
			}

			resultChan <- UploadPreflightResult{
				ServerURL:  serverURL,
				Accepted:   accepted,
				StatusCode: resp.StatusCode,
				XReason:    xReason,
				Error:      nil,
			}
		}(idx, cl, url)
	}

	wg.Wait()
	close(resultChan)

	// Collect all results
	results := make([]UploadPreflightResult, 0, len(m.clients))
	acceptedCount := 0
	for result := range resultChan {
		results = append(results, result)
		if result.Accepted {
			acceptedCount++
		}
	}

	if m.verbose {
		log.Printf("[DEBUG] UploadPreflightParallel: %d/%d servers accepted the upload", acceptedCount, len(results))
	}

	// Check if we have enough servers that would accept
	if acceptedCount < m.minUploadServers {
		errMsg := fmt.Sprintf("only %d servers would accept the upload, need at least %d", acceptedCount, m.minUploadServers)

		// Find the lowest status code from rejected servers
		lowestStatusCode := 0
		allReasons := make([]string, 0)
		for _, result := range results {
			if !result.Accepted && result.StatusCode > 0 {
				if lowestStatusCode == 0 || result.StatusCode < lowestStatusCode {
					lowestStatusCode = result.StatusCode
				}
				if result.XReason != "" {
					allReasons = append(allReasons, result.XReason)
				}
			}
		}

		// If no status codes from rejected servers, use 400 (Bad Request)
		if lowestStatusCode == 0 {
			lowestStatusCode = http.StatusBadRequest
		}

		if m.verbose {
			log.Printf("[DEBUG] UploadPreflightParallel: upload would fail - using status code %d", lowestStatusCode)
		}

		return results, &UploadError{
			StatusCode: lowestStatusCode,
			Message:    errMsg,
		}
	}

	return results, nil
}

// ListParallel queries all upstream servers in parallel for a list of blobs
func (m *Manager) ListParallel(ctx context.Context, pubkey string) ([]map[string]interface{}, error) {
	if m.verbose {
		log.Printf("[DEBUG] ListParallel: starting parallel list query to %d servers for pubkey %s", len(m.clients), pubkey)
	}

	// Channel to collect results
	resultChan := make(chan struct {
		ServerURL string
		Data      []map[string]interface{}
		Error     error
	}, len(m.clients))

	// Launch parallel list queries
	var wg sync.WaitGroup
	for i, cl := range m.clients {
		wg.Add(1)
		go func(idx int, c *client.Client, url string) {
			defer wg.Done()

			if m.verbose {
				log.Printf("[DEBUG] ListParallel: querying server %d: %s", idx+1, url)
			}

			response, err := c.List(ctx, pubkey)
			if err != nil {
				if m.verbose {
					log.Printf("[DEBUG] ListParallel: server %d (%s) failed: %v", idx+1, url, err)
				}
				resultChan <- struct {
					ServerURL string
					Data      []map[string]interface{}
					Error     error
				}{
					ServerURL: url,
					Data:      nil,
					Error:     err,
				}
				return
			}

			// Parse JSON response
			var data []map[string]interface{}
			if err := json.Unmarshal(response, &data); err != nil {
				if m.verbose {
					log.Printf("[DEBUG] ListParallel: server %d (%s) failed to parse JSON: %v", idx+1, url, err)
				}
				resultChan <- struct {
					ServerURL string
					Data      []map[string]interface{}
					Error     error
				}{
					ServerURL: url,
					Data:      nil,
					Error:     fmt.Errorf("failed to parse JSON: %w", err),
				}
				return
			}

			if m.verbose {
				log.Printf("[DEBUG] ListParallel: server %d (%s) returned %d items", idx+1, url, len(data))
			}

			resultChan <- struct {
				ServerURL string
				Data      []map[string]interface{}
				Error     error
			}{
				ServerURL: url,
				Data:      data,
				Error:     nil,
			}
		}(i, cl, m.serverURLs[i])
	}

	// Wait for all queries to complete
	wg.Wait()
	close(resultChan)

	// Collect results
	allResults := make([]struct {
		ServerURL string
		Data      []map[string]interface{}
		Error     error
	}, 0)
	for result := range resultChan {
		allResults = append(allResults, result)
	}

	if m.verbose {
		successCount := 0
		for _, r := range allResults {
			if r.Error == nil {
				successCount++
			}
		}
		log.Printf("[DEBUG] ListParallel: completed - %d succeeded, %d failed", successCount, len(allResults)-successCount)
	}

	// Merge and deduplicate results based on sha256
	// Track all items by sha256, along with their server URLs
	type itemWithServer struct {
		Item      map[string]interface{}
		ServerURL string
	}
	itemsByHash := make(map[string][]itemWithServer)

	// Collect all items, grouping by sha256
	for _, result := range allResults {
		if result.Error != nil {
			continue // Skip failed servers
		}

		for _, item := range result.Data {
			// Extract sha256 field
			sha256Val, ok := item["sha256"].(string)
			if !ok || sha256Val == "" {
				// Skip items without sha256
				continue
			}

			itemsByHash[sha256Val] = append(itemsByHash[sha256Val], itemWithServer{
				Item:      item,
				ServerURL: result.ServerURL,
			})
		}
	}

	// For each sha256, use selection strategy to pick one item and collect all URLs
	merged := make([]map[string]interface{}, 0, len(itemsByHash))
	for sha256Val, items := range itemsByHash {
		var selected map[string]interface{}
		var selectedServerURL string

		if len(items) == 1 {
			// Only one server has this item
			selected = items[0].Item
			selectedServerURL = items[0].ServerURL
		} else {
			// Multiple servers have this item - use selection strategy
			serverURLs := make([]string, len(items))
			for i, item := range items {
				serverURLs[i] = item.ServerURL
			}

			// Select a server using the configured strategy
			var err error
			selectedServerURL, err = m.SelectServerURL(serverURLs)
			if err != nil {
				// Fallback to first item if selection fails
				selected = items[0].Item
				selectedServerURL = items[0].ServerURL
			} else {
				// Find the item from the selected server
				for _, item := range items {
					if item.ServerURL == selectedServerURL {
						selected = item.Item
						break
					}
				}
				// If somehow not found, use first
				if selected == nil {
					selected = items[0].Item
					selectedServerURL = items[0].ServerURL
				}
			}

			if m.verbose && len(items) > 1 {
				log.Printf("[DEBUG] ListParallel: sha256 %s found on %d servers, selected %s", sha256Val, len(items), selectedServerURL)
			}
		}

		// Collect all URLs from all servers for this sha256
		altURLs := make([]string, 0)
		for _, item := range items {
			if urlVal, ok := item.Item["url"].(string); ok && urlVal != "" {
				// Add URL if not already in altURLs
				found := false
				for _, altURL := range altURLs {
					if altURL == urlVal {
						found = true
						break
					}
				}
				if !found {
					altURLs = append(altURLs, urlVal)
				}
			}
		}

		// Make a copy of the selected item to avoid modifying the original
		resultItem := make(map[string]interface{})
		for k, v := range selected {
			resultItem[k] = v
		}

		// Always add alturls field (even if empty or single URL)
		resultItem["alturls"] = altURLs

		if m.verbose {
			log.Printf("[DEBUG] ListParallel: sha256 %s - added alturls with %d URLs: %v", sha256Val, len(altURLs), altURLs)
		}

		merged = append(merged, resultItem)
	}

	if m.verbose {
		log.Printf("[DEBUG] ListParallel: merged %d unique items from all servers", len(merged))
	}

	return merged, nil
}
