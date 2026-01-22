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

// errorTolerantWriter wraps a pipe writer and continues writing even if errors occur
// It tracks errors separately so we can detect which pipes failed
type errorTolerantWriter struct {
	w      *io.PipeWriter
	err    error
	mu     sync.Mutex
	name   string // for debugging
	closed bool
}

func (ew *errorTolerantWriter) Write(p []byte) (int, error) {
	ew.mu.Lock()
	defer ew.mu.Unlock()

	if ew.err != nil || ew.closed {
		// Already errored or closed, skip writes but return success to keep MultiWriter happy
		// This ensures io.Copy continues even if one pipe fails
		return len(p), nil
	}

	// Attempt to write to the pipe
	n, err := ew.w.Write(p)
	if err != nil {
		// If write fails (e.g., pipe closed), mark as errored and close
		ew.err = err
		ew.closed = true
		// Close the writer to signal EOF to the reader, but don't propagate the error
		// to io.MultiWriter so it continues writing to other pipes
		if ew.w != nil {
			ew.w.CloseWithError(err)
		}
		// Return success with bytes written (even if it was partial) to keep MultiWriter happy
		// This allows io.Copy to continue reading and writing to other pipes
		return len(p), nil
	}
	// Success - return number of bytes written and no error
	return n, nil
}

func (ew *errorTolerantWriter) GetError() error {
	ew.mu.Lock()
	defer ew.mu.Unlock()
	return ew.err
}

func (ew *errorTolerantWriter) Close() error {
	ew.mu.Lock()
	defer ew.mu.Unlock()
	if !ew.closed && ew.w != nil {
		ew.closed = true
		return ew.w.Close()
	}
	return nil
}

// Manager manages upstream Blossom servers
type Manager struct {
	clients            []*client.Client // HTTP clients with no timeout (timeouts controlled via context)
	serverURLs         []string
	serverPriorities   []int                // Priority for each server (indexed same as clients/serverURLs)
	serverCapabilities []serverCapabilities // Capabilities for each server (indexed same as clients/serverURLs)
	minUploadServers   int
	redirectStrategy   string
	roundRobinIndex    int
	roundRobinMutex    sync.Mutex
	verbose            bool
	getTotalFailures   func(string) int64 // Function to get total failures for a server (for health_based strategy)
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
	serverPriorities := make([]int, 0, len(cfg.UpstreamServers))
	capabilities := make([]serverCapabilities, 0, len(cfg.UpstreamServers))

	for _, server := range cfg.UpstreamServers {
		// Create clients with no timeout - timeouts are controlled via context in each request
		// This allows connection reuse and better performance
		// Use alternative_address for connections if provided, otherwise use the official URL
		cl := client.New(server.URL, server.AlternativeAddress, 0, verbose)
		clients = append(clients, cl)

		serverURLs = append(serverURLs, server.URL)
		serverPriorities = append(serverPriorities, server.Priority)

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
			altAddr := cfg.UpstreamServers[i].AlternativeAddress
			if altAddr != "" {
				log.Printf("[DEBUG]   Upstream server %d: %s (connect via %s, priority=%d, mirror=%t, upload_head=%t)",
					i+1, url, altAddr, serverPriorities[i], capabilities[i].SupportsMirror, capabilities[i].SupportsUploadHead)
			} else {
				log.Printf("[DEBUG]   Upstream server %d: %s (priority=%d, mirror=%t, upload_head=%t)",
					i+1, url, serverPriorities[i], capabilities[i].SupportsMirror, capabilities[i].SupportsUploadHead)
			}
		}
	}

	return &Manager{
		clients:            clients,
		serverURLs:         serverURLs,
		serverPriorities:   serverPriorities,
		serverCapabilities: capabilities,
		minUploadServers:   cfg.Server.MinUploadServers,
		redirectStrategy:   cfg.Server.RedirectStrategy,
		verbose:            verbose,
		getTotalFailures:   nil, // Will be set via SetFailureGetter if needed
	}, nil
}

// SetFailureGetter sets the function to get total failures for health_based strategy
func (m *Manager) SetFailureGetter(getter func(string) int64) {
	m.getTotalFailures = getter
}

// UploadResultWithResponse contains a successful server URL and its response body
type UploadResultWithResponse struct {
	ServerURL    string
	ResponseBody []byte
}

// UploadParallel uploads a blob to multiple upstream servers in parallel
// timeout specifies the timeout for the upload context (typically calculated from expiration timestamp)
// Returns the list of successful servers with their response bodies and an error if fewer than minUploadServers succeeded
func (m *Manager) UploadParallel(ctx context.Context, body io.Reader, contentType string, headers map[string]string, timeout time.Duration) ([]UploadResultWithResponse, error) {
	if m.verbose {
		log.Printf("[DEBUG] UploadParallel: starting parallel upload to %d servers", len(m.clients))
		log.Printf("[DEBUG] UploadParallel: content-type=%s, headers=%v, timeout=%v", contentType, headers, timeout)
	}

	// Create a context with upload timeout (calculated from expiration timestamp if available)
	uploadCtx, cancel := context.WithTimeout(ctx, timeout)
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
			responseBody, err := c.Upload(uploadCtx, reader, contentType, int64(len(bodyBytes)), headers)
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
		return successfulServers, fmt.Errorf("%s", errMsg)
	}

	if m.verbose {
		log.Printf("[DEBUG] UploadParallel: upload successful, minimum requirement met (%d >= %d)", len(successfulServers), m.minUploadServers)
	}

	return successfulServers, nil
}

// UploadParallelStreaming streams a blob to multiple upstream servers in parallel
// Unlike UploadParallel, this method streams the body directly without buffering it first
// This allows uploads to start immediately, preventing auth header expiration on large files
// contentLength should be set if known (>= 0), otherwise -1 to use chunked encoding
// timeout specifies the timeout for the upload context (typically calculated from expiration timestamp)
// Returns the list of successful servers with their response bodies and an error if fewer than minUploadServers succeeded
func (m *Manager) UploadParallelStreaming(ctx context.Context, body io.Reader, contentType string, contentLength int64, headers map[string]string, timeout time.Duration) ([]UploadResultWithResponse, error) {
	if m.verbose {
		log.Printf("[DEBUG] UploadParallelStreaming: starting streaming parallel upload to %d servers", len(m.clients))
		log.Printf("[DEBUG] UploadParallelStreaming: content-type=%s, headers=%v, timeout=%v", contentType, headers, timeout)
	}

	// Create a context with upload timeout (calculated from expiration timestamp if available)
	uploadCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Create pipes for each upstream server
	type pipeData struct {
		reader *io.PipeReader
		writer *io.PipeWriter
	}
	pipes := make([]pipeData, len(m.clients))
	for i := range pipes {
		pipes[i].reader, pipes[i].writer = io.Pipe()
	}

	// Channel to collect results
	resultChan := make(chan UploadResult, len(m.clients))

	// Launch parallel uploads - each one reads from its pipe
	var wg sync.WaitGroup
	for i, cl := range m.clients {
		wg.Add(1)
		go func(idx int, c *client.Client, url string, pipeReader *io.PipeReader) {
			defer wg.Done()
			defer pipeReader.Close()

			if m.verbose {
				log.Printf("[DEBUG] UploadParallelStreaming: starting upload to server %d: %s", idx+1, url)
			}

			uploadStart := time.Now()
			responseBody, err := c.Upload(uploadCtx, pipeReader, contentType, contentLength, headers)
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
					log.Printf("[DEBUG] UploadParallelStreaming: server %d (%s) succeeded in %v", idx+1, url, uploadDuration)
				} else {
					log.Printf("[DEBUG] UploadParallelStreaming: server %d (%s) failed in %v: %v", idx+1, url, uploadDuration, err)
				}
			}

			resultChan <- result
		}(i, cl, m.serverURLs[i], pipes[i].reader)
	}

	// Stream data from body to all pipes using MultiWriter with error-tolerant writers
	// This allows writing to continue even if one pipe fails
	streamErr := make(chan error, 1)
	errorTolerantWriters := make([]*errorTolerantWriter, len(pipes))
	go func() {
		defer func() {
			// Note: Individual writers are closed by errorTolerantWriter.Close()
			// Only close pipes that weren't handled by errorTolerantWriter
			for i, p := range pipes {
				if p.writer != nil && (errorTolerantWriters[i] == nil || errorTolerantWriters[i].GetError() == nil) {
					// Close any pipes not already closed by errorTolerantWriter
					if err := p.writer.Close(); err != nil {
						if m.verbose {
							log.Printf("[DEBUG] UploadParallelStreaming: error closing pipe writer %d: %v", i+1, err)
						}
					}
				}
			}
		}()

		// Create error-tolerant writers for each pipe
		writers := make([]io.Writer, 0, len(pipes))
		for i, p := range pipes {
			if p.writer != nil {
				etw := &errorTolerantWriter{
					w:    p.writer,
					name: fmt.Sprintf("pipe-%d", i+1),
				}
				errorTolerantWriters[i] = etw
				writers = append(writers, etw)
			}
		}
		multiWriter := io.MultiWriter(writers...)

		// Copy from body to all pipes simultaneously
		// Even if one pipe fails, we continue writing to others
		// IMPORTANT: io.Copy must read ALL data from body to ensure complete hash calculation
		// The body is a teeReader that writes to hashWriter as it reads from r.Body
		copied, err := io.Copy(multiWriter, body)
		if m.verbose {
			log.Printf("[DEBUG] UploadParallelStreaming: copied %d bytes from body to pipes (hash should be complete after this)", copied)
		}

		// Close all writers after copying (even if some had errors)
		for i, etw := range errorTolerantWriters {
			if etw != nil {
				pipeErr := etw.GetError()
				if pipeErr != nil {
					if m.verbose {
						log.Printf("[DEBUG] UploadParallelStreaming: pipe writer %d (%s) had error during streaming: %v", i+1, etw.name, pipeErr)
					}
				} else {
					// Only close if no error occurred (Close() will handle closed state)
					etw.Close()
				}
			}
		}

		if err != nil {
			streamErr <- fmt.Errorf("failed to stream body to pipes: %w", err)
			return
		}

		streamErr <- nil
	}()

	// Wait for all uploads to complete
	wg.Wait()
	close(resultChan)

	// Check for streaming errors
	select {
	case err := <-streamErr:
		if err != nil {
			if m.verbose {
				log.Printf("[DEBUG] UploadParallelStreaming: streaming error: %v", err)
			}
			// Continue to process results even if streaming had errors
		}
	default:
		// No error
	}

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
		log.Printf("[DEBUG] UploadParallelStreaming: completed - %d succeeded, %d failed", len(successfulServers), len(errorDetails))
		if len(successfulServers) > 0 {
			log.Printf("[DEBUG] UploadParallelStreaming: successful servers: %v", successfulServers)
		}
		if len(errorDetails) > 0 {
			log.Printf("[DEBUG] UploadParallelStreaming: failed servers: %v", errorDetails)
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
				log.Printf("[DEBUG] UploadParallelStreaming: using lowest upstream status code %d (from %v)", minStatusCode, allStatusCodes)
			}
			return successfulServers, &UploadError{
				StatusCode: minStatusCode,
				Message:    errMsg,
			}
		}

		// No status codes available - return 500
		return successfulServers, fmt.Errorf("%s", errMsg)
	}

	if m.verbose {
		log.Printf("[DEBUG] UploadParallelStreaming: upload successful, minimum requirement met (%d >= %d)", len(successfulServers), m.minUploadServers)
	}

	return successfulServers, nil
}

// MirrorParallel sends mirror requests to multiple upstream servers in parallel (BUD-04)
// Only sends to servers that support mirror capability
// timeout specifies the timeout for the mirror context
// Returns the list of successful servers with their response bodies and an error if fewer than minUploadServers succeeded
func (m *Manager) MirrorParallel(ctx context.Context, body io.Reader, contentType string, headers map[string]string, timeout time.Duration) ([]UploadResultWithResponse, error) {
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
		log.Printf("[DEBUG] MirrorParallel: content-type=%s, headers=%v, timeout=%v", contentType, headers, timeout)
	}

	// Channel to collect results
	resultChan := make(chan UploadResult, len(mirrorCapableIndices))

	// Read body into memory so we can reuse it for multiple mirror requests
	// Do this BEFORE creating the timeout context so the timeout only applies to HTTP requests
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}

	if m.verbose {
		log.Printf("[DEBUG] MirrorParallel: read %d bytes from request body", len(bodyBytes))
	}

	// Create a context with timeout AFTER reading the body
	// This ensures the timeout only applies to the actual HTTP requests, not body reading
	mirrorCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

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
	case "priority":
		selected = m.selectPriorityWithResponse(availableServers)
	case "health_based":
		selected = m.selectHealthBasedWithResponse(availableServers)
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

// selectHealthBasedWithResponse selects servers with the lowest total failures, then uses round-robin within that group
func (m *Manager) selectHealthBasedWithResponse(availableServers []UploadResultWithResponse) *UploadResultWithResponse {
	if len(availableServers) == 0 {
		return nil
	}

	// If no failure getter is set, fall back to round-robin
	if m.getTotalFailures == nil {
		return m.selectRoundRobinWithResponse(availableServers)
	}

	// Group servers by total failures
	serversByFailures := make(map[int64][]*UploadResultWithResponse)
	var minFailures int64 = -1

	for i := range availableServers {
		serverURL := availableServers[i].ServerURL
		totalFailures := m.getTotalFailures(serverURL)
		serversByFailures[totalFailures] = append(serversByFailures[totalFailures], &availableServers[i])

		// Track minimum failures
		if minFailures == -1 || totalFailures < minFailures {
			minFailures = totalFailures
		}
	}

	// Get servers with minimum failures
	bestServers := serversByFailures[minFailures]
	if len(bestServers) == 0 {
		// Shouldn't happen, but fallback
		return &availableServers[0]
	}

	if m.verbose {
		serverURLs := make([]string, len(bestServers))
		for i, srv := range bestServers {
			serverURLs[i] = srv.ServerURL
		}
		log.Printf("[DEBUG] selectHealthBasedWithResponse: %d servers with minimum failures (%d): %v", len(bestServers), minFailures, serverURLs)
	}

	// Use round-robin within the best servers group
	// Convert to slice of UploadResultWithResponse for round-robin
	bestServersSlice := make([]UploadResultWithResponse, len(bestServers))
	for i, srv := range bestServers {
		bestServersSlice[i] = *srv
	}
	return m.selectRoundRobinWithResponse(bestServersSlice)
}

// selectPriorityWithResponse selects the server with the lowest priority number (lower is better)
// If multiple servers have the same lowest priority, returns the first one found
func (m *Manager) selectPriorityWithResponse(availableServers []UploadResultWithResponse) *UploadResultWithResponse {
	if len(availableServers) == 0 {
		return nil
	}

	// Find the priority for each available server
	var bestServer *UploadResultWithResponse
	bestPriority := int(^uint(0) >> 1) // Max int value

	for i := range availableServers {
		serverURL := availableServers[i].ServerURL
		// Find the priority for this URL
		for j, url := range m.serverURLs {
			if url == serverURL {
				if m.serverPriorities[j] < bestPriority {
					bestPriority = m.serverPriorities[j]
					bestServer = &availableServers[i]
				}
				break
			}
		}
	}

	// If we didn't find a match (shouldn't happen), fall back to first available
	if bestServer == nil {
		bestServer = &availableServers[0]
	}

	return bestServer
}

// SelectServer selects a server URL for redirect based on the configured strategy (legacy method for download)
func (m *Manager) SelectServerURL(availableServers []string) (string, error) {
	return m.SelectServerURLWithStrategy(availableServers, m.redirectStrategy)
}

// SelectServerURLWithStrategy selects a server URL using the specified strategy
func (m *Manager) SelectServerURLWithStrategy(availableServers []string, strategy string) (string, error) {
	if len(availableServers) == 0 {
		return "", fmt.Errorf("no available servers")
	}

	var selected string
	switch strategy {
	case "round_robin":
		selected = m.selectRoundRobin(availableServers)
	case "random":
		selected = m.selectRandom(availableServers)
	case "priority":
		selected = m.selectPriority(availableServers)
	case "local":
		// For download redirects, "local" strategy uses round-robin to select upstream server
		// "Local" only affects response URLs in upload/mirror/list endpoints
		selected = m.selectRoundRobin(availableServers)
	case "health_based":
		selected = m.selectHealthBased(availableServers)
	default:
		// Default to round-robin
		selected = m.selectRoundRobin(availableServers)
	}

	if m.verbose {
		log.Printf("[DEBUG] SelectServerURL: strategy=%s, available=%d servers, selected=%s", strategy, len(availableServers), selected)
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

// selectHealthBased selects servers with the lowest total failures, then uses round-robin within that group
// Total failures = sum of upload, mirror, delete, and list failures
func (m *Manager) selectHealthBased(availableServers []string) string {
	if len(availableServers) == 0 {
		return ""
	}

	// If no failure getter is set, fall back to round-robin
	if m.getTotalFailures == nil {
		return m.selectRoundRobin(availableServers)
	}

	// Group servers by total failures
	serversByFailures := make(map[int64][]string)
	var minFailures int64 = -1

	for _, serverURL := range availableServers {
		totalFailures := m.getTotalFailures(serverURL)
		serversByFailures[totalFailures] = append(serversByFailures[totalFailures], serverURL)

		// Track minimum failures
		if minFailures == -1 || totalFailures < minFailures {
			minFailures = totalFailures
		}
	}

	// Get servers with minimum failures
	bestServers := serversByFailures[minFailures]
	if len(bestServers) == 0 {
		// Shouldn't happen, but fallback
		return availableServers[0]
	}

	if m.verbose {
		log.Printf("[DEBUG] selectHealthBased: %d servers with minimum failures (%d): %v", len(bestServers), minFailures, bestServers)
	}

	// Use round-robin within the best servers group
	return m.selectRoundRobin(bestServers)
}

// selectPriority selects the server with the lowest priority number (lower is better)
// If multiple servers have the same lowest priority, returns the first one found
func (m *Manager) selectPriority(availableServers []string) string {
	if len(availableServers) == 0 {
		return ""
	}

	// Create a map of available server URLs to their priorities
	availablePriorities := make(map[string]int)
	for _, availableURL := range availableServers {
		// Find the priority for this URL
		for i, url := range m.serverURLs {
			if url == availableURL {
				availablePriorities[availableURL] = m.serverPriorities[i]
				break
			}
		}
	}

	// Find the server with the lowest priority
	var bestServer string
	bestPriority := int(^uint(0) >> 1) // Max int value

	for _, url := range availableServers {
		if priority, ok := availablePriorities[url]; ok {
			if priority < bestPriority {
				bestPriority = priority
				bestServer = url
			}
		}
	}

	// If we didn't find a match (shouldn't happen), fall back to first available
	if bestServer == "" {
		bestServer = availableServers[0]
	}

	return bestServer
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

// GetMirrorCapableServers returns a list of server URLs that support mirroring
func (m *Manager) GetMirrorCapableServers() []string {
	mirrorCapableServers := make([]string, 0)
	for i, cap := range m.serverCapabilities {
		if cap.SupportsMirror {
			mirrorCapableServers = append(mirrorCapableServers, m.serverURLs[i])
		}
	}
	return mirrorCapableServers
}

// CheckPathOnServersResult contains the result of checking servers for a path
type CheckPathOnServersResult struct {
	Servers []string               // List of server URLs that have the blob
	Headers map[string]http.Header // Map of server URL to response headers (only for servers with blob)
}

// CheckPathOnServers checks all upstream servers in parallel to see which ones have the blob at the given path
// Returns list of server URLs that have the blob and their response headers
func (m *Manager) CheckPathOnServers(ctx context.Context, path string, timeout time.Duration) CheckPathOnServersResult {
	if m.verbose {
		log.Printf("[DEBUG] CheckPathOnServers: checking path %s on %d servers, timeout=%v", path, len(m.clients), timeout)
	}

	// Create a context with timeout
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Channel to collect results
	resultChan := make(chan struct {
		ServerURL string
		HasBlob   bool
		Headers   http.Header
	}, len(m.clients))

	// Launch parallel HEAD requests
	var wg sync.WaitGroup
	for i, cl := range m.clients {
		wg.Add(1)
		go func(idx int, c *client.Client, url string) {
			defer wg.Done()

			if m.verbose {
				log.Printf("[DEBUG] CheckPathOnServers: checking server %d: %s", idx+1, url)
			}

			// Use Head() to get headers, passing the full path (may include extension)
			headResp, err := c.Head(checkCtx, path)
			hasBlob := err == nil && headResp != nil && headResp.StatusCode == http.StatusOK

			var headers http.Header
			if hasBlob && headResp != nil {
				headers = headResp.Header
				headResp.Body.Close()
			}

			resultChan <- struct {
				ServerURL string
				HasBlob   bool
				Headers   http.Header
			}{
				ServerURL: url,
				HasBlob:   hasBlob,
				Headers:   headers,
			}

			if m.verbose {
				if hasBlob {
					log.Printf("[DEBUG] CheckPathOnServers: server %d (%s) has the blob", idx+1, url)
				} else {
					log.Printf("[DEBUG] CheckPathOnServers: server %d (%s) does not have the blob", idx+1, url)
				}
			}
		}(i, cl, m.serverURLs[i])
	}

	// Wait for all checks to complete
	wg.Wait()
	close(resultChan)

	// Collect servers that have the blob and their headers
	serversWithBlob := make([]string, 0)
	headersMap := make(map[string]http.Header)
	for result := range resultChan {
		if result.HasBlob {
			serversWithBlob = append(serversWithBlob, result.ServerURL)
			if result.Headers != nil {
				headersMap[result.ServerURL] = result.Headers
			}
		}
	}

	if m.verbose {
		log.Printf("[DEBUG] CheckPathOnServers: path found on %d servers: %v", len(serversWithBlob), serversWithBlob)
	}

	return CheckPathOnServersResult{
		Servers: serversWithBlob,
		Headers: headersMap,
	}
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
// timeout specifies the timeout for the preflight context
// Returns the list of servers that would accept the upload
func (m *Manager) UploadPreflightParallel(ctx context.Context, headers map[string]string, timeout time.Duration) ([]UploadPreflightResult, error) {
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
		log.Printf("[DEBUG] UploadPreflightParallel: headers=%v, timeout=%v", headers, timeout)
	}

	// Create a context with timeout
	preflightCtx, cancel := context.WithTimeout(ctx, timeout)
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

// listParallelInternal is the internal implementation that queries all upstream servers
// and returns both merged results and per-server results
func (m *Manager) listParallelInternal(ctx context.Context, pubkey string, timeout time.Duration) ([]map[string]interface{}, []ListResult, error) {
	if m.verbose {
		log.Printf("[DEBUG] ListParallel: starting parallel list query to %d servers for pubkey %s, timeout=%v", len(m.clients), pubkey, timeout)
	}

	// Create a context with timeout
	listCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

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

			response, err := c.List(listCtx, pubkey)
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
	allResults := make([]ListResult, 0)
	for result := range resultChan {
		allResults = append(allResults, ListResult{
			ServerURL: result.ServerURL,
			Data:      result.Data,
			Error:     result.Error,
		})
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
				Item:     item,
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

		// Collect all URLs from all servers for this sha256 and add as BUD-08 tags
		// Also add NIP-94 tags: ["x", "<hash>"] and ["m", "<mime-type>"]

		// Get existing nip94 tags from selected item or create new tags array
		var tags []interface{}
		if existingTags, ok := selected["nip94"].([]interface{}); ok {
			// Make a deep copy to avoid modifying the original
			tags = make([]interface{}, 0, len(existingTags))
			for _, tag := range existingTags {
				if tagArray, ok := tag.([]interface{}); ok && len(tagArray) > 0 {
					tags = append(tags, tagArray)
				}
			}
		} else {
			tags = make([]interface{}, 0)
		}

		// Helper function to check if a tag already exists (by type and value)
		hasTag := func(tagType string, tagValue string) bool {
			for _, tag := range tags {
				if tagArray, ok := tag.([]interface{}); ok && len(tagArray) >= 2 {
					if typeVal, ok := tagArray[0].(string); ok && typeVal == tagType {
						if valueVal, ok := tagArray[1].(string); ok && valueVal == tagValue {
							return true
						}
					}
				}
			}
			return false
		}

		// Helper function to check if a tag type already exists (ignoring value)
		hasTagType := func(tagType string) bool {
			for _, tag := range tags {
				if tagArray, ok := tag.([]interface{}); ok && len(tagArray) > 0 {
					if typeVal, ok := tagArray[0].(string); ok && typeVal == tagType {
						return true
					}
				}
			}
			return false
		}

		// Add NIP-94 hash tag ["x", "<hash>"] if not present
		if sha256Val != "" && !hasTag("x", sha256Val) && !hasTagType("x") {
			tags = append(tags, []interface{}{"x", sha256Val})
		}

		// Add NIP-94 mime type tag ["m", "<mime-type>"] if not present
		var mimeType string
		if typeVal, ok := selected["type"].(string); ok && typeVal != "" {
			mimeType = typeVal
		}
		if mimeType != "" && !hasTagType("m") {
			tags = append(tags, []interface{}{"m", mimeType})
		}

		// Collect URLs from all servers for this sha256
		for _, item := range items {
			if urlVal, ok := item.Item["url"].(string); ok && urlVal != "" {
				// Add URL tag if not already present (check exact duplicate)
				if !hasTag("url", urlVal) {
					tags = append(tags, []interface{}{"url", urlVal})
				}
			}
		}

		// Make a copy of the selected item to avoid modifying the original
		resultItem := make(map[string]interface{})
		for k, v := range selected {
			resultItem[k] = v
		}

		// Update nip94 in result item (BUD-08 + NIP-94)
		resultItem["nip94"] = tags

		if m.verbose {
			// Count url tags for logging
			urlTagCount := 0
			for _, tag := range tags {
				if tagArray, ok := tag.([]interface{}); ok && len(tagArray) > 0 {
					if typeVal, ok := tagArray[0].(string); ok && typeVal == "url" {
						urlTagCount++
					}
				}
			}
			log.Printf("[DEBUG] ListParallel: sha256 %s - added tags - %d url tags (BUD-08), NIP-94 tags for hash and mime type", sha256Val, urlTagCount)
		}

		merged = append(merged, resultItem)
	}

	if m.verbose {
		log.Printf("[DEBUG] ListParallel: merged %d unique items from all servers", len(merged))
	}

	return merged, allResults, nil
}

// ListParallel queries all upstream servers in parallel for a list of blobs
// timeout specifies the timeout for the list context
func (m *Manager) ListParallel(ctx context.Context, pubkey string, timeout time.Duration) ([]map[string]interface{}, error) {
	merged, _, err := m.listParallelInternal(ctx, pubkey, timeout)
	return merged, err
}

// ListResult represents a single server's list query result
type ListResult struct {
	ServerURL string
	Data      []map[string]interface{}
	Error     error
}

// ListParallelWithResults queries all upstream servers and returns both merged results and per-server results
// This is a wrapper around listParallelInternal that returns individual server results for stats tracking
func (m *Manager) ListParallelWithResults(ctx context.Context, pubkey string, timeout time.Duration) ([]map[string]interface{}, []ListResult, error) {
	return m.listParallelInternal(ctx, pubkey, timeout)
}
