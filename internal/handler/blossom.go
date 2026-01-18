package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"strings"

	"github.com/girino/blossom_espelhator/internal/cache"
	"github.com/girino/blossom_espelhator/internal/config"
	"github.com/girino/blossom_espelhator/internal/stats"
	"github.com/girino/blossom_espelhator/internal/upstream"
)

// BlossomHandler handles Blossom protocol requests
type BlossomHandler struct {
	upstreamManager *upstream.Manager
	cache           *cache.Cache
	stats           *stats.Stats
	config          *config.Config
	verbose         bool
}

// New creates a new Blossom handler
func New(upstreamManager *upstream.Manager, cache *cache.Cache, statsTracker *stats.Stats, cfg *config.Config, verbose bool) *BlossomHandler {
	return &BlossomHandler{
		upstreamManager: upstreamManager,
		cache:           cache,
		stats:           statsTracker,
		config:          cfg,
		verbose:         verbose,
	}
}

// HandleUpload handles PUT /upload and HEAD /upload requests
// HEAD /upload implements BUD-06: Upload requirements (preflight check)
func (h *BlossomHandler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	if h.verbose {
		log.Printf("[DEBUG] HandleUpload: received %s request from %s", r.Method, r.RemoteAddr)
		log.Printf("[DEBUG] HandleUpload: path=%s, content-type=%s, content-length=%s", r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Content-Length"))
		log.Printf("[DEBUG] HandleUpload: headers=%v", r.Header)
	}

	// Handle HEAD /upload (BUD-06: Upload requirements preflight check)
	if r.Method == http.MethodHead {
		h.handleUploadPreflight(w, r)
		return
	}

	if r.Method != http.MethodPut {
		if h.verbose {
			log.Printf("[DEBUG] HandleUpload: method not allowed: %s", r.Method)
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read the request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleUpload: failed to read body: %v", err)
		}
		http.Error(w, fmt.Sprintf("Failed to read request body: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if h.verbose {
		log.Printf("[DEBUG] HandleUpload: read %d bytes from request body", len(bodyBytes))
	}

	// Calculate SHA256 hash
	hash := sha256.Sum256(bodyBytes)
	hashStr := hex.EncodeToString(hash[:])

	if h.verbose {
		log.Printf("[DEBUG] HandleUpload: calculated hash: %s", hashStr)
	}

	// Copy headers from original request (for Nostr event, etc.)
	headers := make(map[string]string)
	for k, v := range r.Header {
		// Skip certain headers that shouldn't be forwarded
		if strings.ToLower(k) == "host" || strings.ToLower(k) == "content-length" {
			continue
		}
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	if h.verbose {
		log.Printf("[DEBUG] HandleUpload: forwarding headers: %v", headers)
	}

	// Forward upload to upstream servers
	bodyReader := bytes.NewReader(bodyBytes)
	successfulServers, err := h.upstreamManager.UploadParallel(r.Context(), bodyReader, r.Header.Get("Content-Type"), headers)

	// Track stats for all attempted servers (successful and failed)
	// Get all upstream server URLs to track failures
	allServerURLs := h.upstreamManager.GetServerURLs()
	successfulURLs := make(map[string]bool)
	for _, srv := range successfulServers {
		successfulURLs[srv.ServerURL] = true
		h.stats.RecordSuccess(srv.ServerURL, "upload")
	}
	// Track failures for servers that didn't succeed
	for _, serverURL := range allServerURLs {
		if !successfulURLs[serverURL] {
			h.stats.RecordFailure(serverURL, "upload")
		}
	}

	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleUpload: upload failed: %v", err)
		}

		// Check if error has an HTTP status code to pass through
		if uploadErr, ok := err.(*upstream.UploadError); ok {
			if h.verbose {
				log.Printf("[DEBUG] HandleUpload: passing through upstream status code %d", uploadErr.StatusCode)
			}
			w.Header().Set("Content-Type", "text/plain")
			http.Error(w, uploadErr.Error(), uploadErr.StatusCode)
			return
		}

		// Default to 500 for other errors
		w.Header().Set("Content-Type", "text/plain")
		http.Error(w, fmt.Sprintf("Upload failed: %v", err), http.StatusInternalServerError)
		return
	}

	if h.verbose {
		log.Printf("[DEBUG] HandleUpload: upload successful to %d servers", len(successfulServers))
	}

	// Extract server URLs for cache
	serverURLs := make([]string, 0, len(successfulServers))
	for _, srv := range successfulServers {
		serverURLs = append(serverURLs, srv.ServerURL)
	}

	// Update cache with successful servers
	h.cache.Add(hashStr, serverURLs)

	if h.verbose {
		log.Printf("[DEBUG] HandleUpload: added hash %s to cache with %d servers", hashStr, len(serverURLs))
	}

	// Select a server to return in the response
	selectedServer, err := h.upstreamManager.SelectServer(successfulServers)
	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleUpload: failed to select server: %v", err)
		}
		http.Error(w, fmt.Sprintf("Failed to select server: %v", err), http.StatusInternalServerError)
		return
	}

	if h.verbose {
		log.Printf("[DEBUG] HandleUpload: selected server for response: %s", selectedServer.ServerURL)
		log.Printf("[DEBUG] HandleUpload: using response body from upstream: %s", string(selectedServer.ResponseBody))
	}

	// Parse the selected server's response
	var responseData map[string]interface{}
	if err := json.Unmarshal(selectedServer.ResponseBody, &responseData); err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleUpload: failed to parse selected server response: %v", err)
		}
		// If parsing fails, return original response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(selectedServer.ResponseBody)
		return
	}

	// Collect all URLs from all successful servers and add as BUD-08 tags
	// Also add NIP-94 tags: ["x", "<hash>"] and ["m", "<mime-type>"]

	// Get existing nip94 tags or create new tags array
	var tags []interface{}
	if existingTags, ok := responseData["nip94"].([]interface{}); ok {
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
	if hashStr != "" && !hasTag("x", hashStr) && !hasTagType("x") {
		tags = append(tags, []interface{}{"x", hashStr})
	}

	// Add NIP-94 mime type tag ["m", "<mime-type>"] if not present
	var contentType string
	if r.Header.Get("Content-Type") != "" {
		contentType = r.Header.Get("Content-Type")
	} else if typeVal, ok := responseData["type"].(string); ok && typeVal != "" {
		contentType = typeVal
	}
	if contentType != "" && !hasTagType("m") {
		tags = append(tags, []interface{}{"m", contentType})
	}

	// Collect URLs from all successful servers
	for _, srv := range successfulServers {
		var srvData map[string]interface{}
		if err := json.Unmarshal(srv.ResponseBody, &srvData); err != nil {
			if h.verbose {
				log.Printf("[DEBUG] HandleUpload: failed to parse server response from %s: %v", srv.ServerURL, err)
			}
			continue
		}
		if urlVal, ok := srvData["url"].(string); ok && urlVal != "" {
			// Add URL tag if not already present (check exact duplicate)
			if !hasTag("url", urlVal) {
				tags = append(tags, []interface{}{"url", urlVal})
			}
		}
	}

	// Update nip94 in response
	responseData["nip94"] = tags

	if h.verbose {
		// Count url tags for logging
		urlTagCount := 0
		for _, tag := range tags {
			if tagArray, ok := tag.([]interface{}); ok && len(tagArray) > 0 {
				if typeVal, ok := tagArray[0].(string); ok && typeVal == "url" {
					urlTagCount++
				}
			}
		}
		log.Printf("[DEBUG] HandleUpload: added tags - %d url tags (BUD-08), NIP-94 tags for hash and mime type", urlTagCount)
	}

	// Marshal and return the modified response
	responseJSON, err := json.Marshal(responseData)
	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleUpload: failed to marshal response: %v", err)
		}
		// Fallback to original response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(selectedServer.ResponseBody)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(responseJSON)
}

// HandleMirror handles PUT /mirror requests (BUD-04: Mirroring blobs)
func (h *BlossomHandler) HandleMirror(w http.ResponseWriter, r *http.Request) {
	if h.verbose {
		log.Printf("[DEBUG] HandleMirror: received %s request from %s", r.Method, r.RemoteAddr)
		log.Printf("[DEBUG] HandleMirror: path=%s, content-type=%s, content-length=%s", r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Content-Length"))
		log.Printf("[DEBUG] HandleMirror: headers=%v", r.Header)
	}

	if r.Method != http.MethodPut {
		if h.verbose {
			log.Printf("[DEBUG] HandleMirror: method not allowed: %s", r.Method)
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read the request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleMirror: failed to read body: %v", err)
		}
		http.Error(w, fmt.Sprintf("Failed to read request body: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if h.verbose {
		log.Printf("[DEBUG] HandleMirror: read %d bytes from request body", len(bodyBytes))
	}

	// Copy headers from original request (for Nostr event, etc.)
	headers := make(map[string]string)
	for k, v := range r.Header {
		// Skip certain headers that shouldn't be forwarded
		if strings.ToLower(k) == "host" || strings.ToLower(k) == "content-length" {
			continue
		}
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	if h.verbose {
		log.Printf("[DEBUG] HandleMirror: forwarding headers: %v", headers)
	}

	// Forward mirror request to upstream servers
	bodyReader := bytes.NewReader(bodyBytes)
	successfulServers, err := h.upstreamManager.MirrorParallel(r.Context(), bodyReader, r.Header.Get("Content-Type"), headers)

	// Track stats for mirror operations
	successfulURLs := make(map[string]bool)
	for _, srv := range successfulServers {
		successfulURLs[srv.ServerURL] = true
		h.stats.RecordSuccess(srv.ServerURL, "mirror")
	}
	// Track failures for mirror-capable servers that didn't succeed
	// Note: Only track failures for servers that actually attempted the mirror
	// MirrorParallel only attempts servers with mirror capability, so we can't track all servers here

	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleMirror: mirror request failed: %v", err)
		}

		// Check if error has an HTTP status code to pass through
		if uploadErr, ok := err.(*upstream.UploadError); ok {
			if h.verbose {
				log.Printf("[DEBUG] HandleMirror: passing through upstream status code %d", uploadErr.StatusCode)
			}
			w.Header().Set("Content-Type", "text/plain")
			http.Error(w, uploadErr.Error(), uploadErr.StatusCode)
			return
		}

		// Default to 500 for other errors
		w.Header().Set("Content-Type", "text/plain")
		http.Error(w, fmt.Sprintf("Mirror request failed: %v", err), http.StatusInternalServerError)
		return
	}

	if h.verbose {
		log.Printf("[DEBUG] HandleMirror: mirror request successful to %d servers", len(successfulServers))
	}

	// Select a server to return in the response
	selectedServer, err := h.upstreamManager.SelectServer(successfulServers)
	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleMirror: failed to select server: %v", err)
		}
		http.Error(w, fmt.Sprintf("Failed to select server: %v", err), http.StatusInternalServerError)
		return
	}

	if h.verbose {
		log.Printf("[DEBUG] HandleMirror: selected server for response: %s", selectedServer.ServerURL)
		log.Printf("[DEBUG] HandleMirror: using response body from upstream: %s", string(selectedServer.ResponseBody))
	}

	// Parse the selected server's response
	var responseData map[string]interface{}
	if err := json.Unmarshal(selectedServer.ResponseBody, &responseData); err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleMirror: failed to parse selected server response: %v", err)
		}
		// If parsing fails, return original response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(selectedServer.ResponseBody)
		return
	}

	// Collect all URLs from all successful servers and add as BUD-08 tags
	// Also add NIP-94 tags: ["x", "<hash>"] and ["m", "<mime-type>"]

	// Get existing nip94 tags or create new tags array
	var tags []interface{}
	if existingTags, ok := responseData["nip94"].([]interface{}); ok {
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
	var hashVal string
	if hashStr, ok := responseData["hash"].(string); ok && hashStr != "" {
		hashVal = hashStr
	} else if sha256Val, ok := responseData["sha256"].(string); ok && sha256Val != "" {
		hashVal = sha256Val
	}
	if hashVal != "" && !hasTag("x", hashVal) && !hasTagType("x") {
		tags = append(tags, []interface{}{"x", hashVal})
	}

	// Add NIP-94 mime type tag ["m", "<mime-type>"] if not present
	var mimeType string
	if typeVal, ok := responseData["type"].(string); ok && typeVal != "" {
		mimeType = typeVal
	}
	if mimeType != "" && !hasTagType("m") {
		tags = append(tags, []interface{}{"m", mimeType})
	}

	// Collect URLs from all successful servers
	for _, srv := range successfulServers {
		var srvData map[string]interface{}
		if err := json.Unmarshal(srv.ResponseBody, &srvData); err != nil {
			if h.verbose {
				log.Printf("[DEBUG] HandleMirror: failed to parse server response from %s: %v", srv.ServerURL, err)
			}
			continue
		}
		if urlVal, ok := srvData["url"].(string); ok && urlVal != "" {
			// Add URL tag if not already present (check exact duplicate)
			if !hasTag("url", urlVal) {
				tags = append(tags, []interface{}{"url", urlVal})
			}
		}
	}

	// Update nip94 in response
	responseData["nip94"] = tags

	if h.verbose {
		// Count url tags for logging
		urlTagCount := 0
		for _, tag := range tags {
			if tagArray, ok := tag.([]interface{}); ok && len(tagArray) > 0 {
				if typeVal, ok := tagArray[0].(string); ok && typeVal == "url" {
					urlTagCount++
				}
			}
		}
		log.Printf("[DEBUG] HandleMirror: added tags - %d url tags (BUD-08), NIP-94 tags for hash and mime type", urlTagCount)
	}

	// Marshal and return the modified response
	responseJSON, err := json.Marshal(responseData)
	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleMirror: failed to marshal response: %v", err)
		}
		// Fallback to original response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(selectedServer.ResponseBody)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(responseJSON)
}

// handleUploadPreflight handles HEAD /upload requests (BUD-06: Upload requirements preflight check)
// The request should include headers: X-SHA-256, X-Content-Length, X-Content-Type
// Returns 200 OK if acceptable, or 4xx with X-Reason header if not
func (h *BlossomHandler) handleUploadPreflight(w http.ResponseWriter, r *http.Request) {
	if h.verbose {
		log.Printf("[DEBUG] handleUploadPreflight: received HEAD /upload request from %s", r.RemoteAddr)
		log.Printf("[DEBUG] handleUploadPreflight: headers=%v", r.Header)
	}

	// Extract preflight headers (X-SHA-256, X-Content-Length, X-Content-Type)
	preflightHeaders := make(map[string]string)
	for k, v := range r.Header {
		// Skip certain headers that shouldn't be forwarded
		if strings.ToLower(k) == "host" {
			continue
		}
		if len(v) > 0 {
			preflightHeaders[k] = v[0]
		}
	}

	if h.verbose {
		log.Printf("[DEBUG] handleUploadPreflight: forwarding preflight headers: %v", preflightHeaders)
	}

	// Check upload requirements on all upstream servers
	results, err := h.upstreamManager.UploadPreflightParallel(r.Context(), preflightHeaders)
	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] handleUploadPreflight: preflight check failed: %v", err)
		}

		// Check if error has an HTTP status code to pass through
		if uploadErr, ok := err.(*upstream.UploadError); ok {
			if h.verbose {
				log.Printf("[DEBUG] handleUploadPreflight: passing through upstream status code %d", uploadErr.StatusCode)
			}

			// Collect X-Reason headers from rejected servers
			reasons := make([]string, 0)
			for _, result := range results {
				if !result.Accepted && result.XReason != "" {
					reasons = append(reasons, result.XReason)
				}
			}

			// If we have reasons, use the first one; otherwise use error message
			reason := uploadErr.Error()
			if len(reasons) > 0 {
				reason = reasons[0]
			}

			w.Header().Set("X-Reason", reason)
			w.WriteHeader(uploadErr.StatusCode)
			return
		}

		// Default to 500 for other errors
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Count accepted servers
	acceptedCount := 0
	for _, result := range results {
		if result.Accepted {
			acceptedCount++
		}
	}

	if h.verbose {
		log.Printf("[DEBUG] handleUploadPreflight: preflight check passed - %d/%d servers would accept", acceptedCount, len(results))
	}

	// Return 200 OK if at least minUploadServers would accept
	w.WriteHeader(http.StatusOK)
}

// HandleDownload handles GET /<sha256> requests
func (h *BlossomHandler) HandleDownload(w http.ResponseWriter, r *http.Request) {
	if h.verbose {
		log.Printf("[DEBUG] HandleDownload: received %s request from %s", r.Method, r.RemoteAddr)
		log.Printf("[DEBUG] HandleDownload: path=%s", r.URL.Path)
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract hash from path (remove leading slash)
	hash := strings.TrimPrefix(r.URL.Path, "/")

	if h.verbose {
		log.Printf("[DEBUG] HandleDownload: extracted hash: %s", hash)
	}

	// Validate hash format (should be 64 hex characters for SHA256)
	if len(hash) != 64 {
		if h.verbose {
			log.Printf("[DEBUG] HandleDownload: invalid hash length: %d", len(hash))
		}
		http.Error(w, "Invalid hash format", http.StatusBadRequest)
		return
	}

	// Check if hash is valid hex
	if _, err := hex.DecodeString(hash); err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleDownload: invalid hash format (not hex): %v", err)
		}
		http.Error(w, "Invalid hash format", http.StatusBadRequest)
		return
	}

	// Look up hash in cache
	servers, exists := h.cache.Get(hash)
	if !exists || len(servers) == 0 {
		if h.verbose {
			log.Printf("[DEBUG] HandleDownload: hash %s not found in cache, checking upstream servers", hash)
		}
		// Hash not in cache, check upstream servers using HEAD requests
		servers = h.upstreamManager.CheckHashOnServers(r.Context(), hash)
		if len(servers) == 0 {
			if h.verbose {
				log.Printf("[DEBUG] HandleDownload: hash %s not found on any upstream server", hash)
			}
			http.Error(w, "Blob not found", http.StatusNotFound)
			return
		}
		// Update cache with found servers
		h.cache.Add(hash, servers)
		if h.verbose {
			log.Printf("[DEBUG] HandleDownload: hash %s found on %d upstream servers, added to cache", hash, len(servers))
		}
	}

	if h.verbose {
		log.Printf("[DEBUG] HandleDownload: hash found in cache with %d servers: %v", len(servers), servers)
	}

	// Select a server for redirect
	selectedServer, err := h.upstreamManager.SelectServerURL(servers)
	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleDownload: failed to select server: %v", err)
		}
		http.Error(w, fmt.Sprintf("Failed to select server: %v", err), http.StatusInternalServerError)
		return
	}

	// Track download success for the selected server
	h.stats.RecordSuccess(selectedServer, "download")

	// Redirect to the selected server
	redirectURL := fmt.Sprintf("%s/%s", selectedServer, hash)

	if h.verbose {
		log.Printf("[DEBUG] HandleDownload: selected server: %s", selectedServer)
		log.Printf("[DEBUG] HandleDownload: redirecting to: %s", redirectURL)
	}

	http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
}

// HandleHead handles HEAD /<sha256> requests
func (h *BlossomHandler) HandleHead(w http.ResponseWriter, r *http.Request) {
	if h.verbose {
		log.Printf("[DEBUG] HandleHead: received %s request from %s", r.Method, r.RemoteAddr)
		log.Printf("[DEBUG] HandleHead: path=%s", r.URL.Path)
	}

	if r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract hash from path (remove leading slash)
	hash := strings.TrimPrefix(r.URL.Path, "/")

	if h.verbose {
		log.Printf("[DEBUG] HandleHead: extracted hash: %s", hash)
	}

	// Validate hash format (should be 64 hex characters for SHA256)
	if len(hash) != 64 {
		if h.verbose {
			log.Printf("[DEBUG] HandleHead: invalid hash length: %d", len(hash))
		}
		http.Error(w, "Invalid hash format", http.StatusBadRequest)
		return
	}

	// Check if hash is valid hex
	if _, err := hex.DecodeString(hash); err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleHead: invalid hash format (not hex): %v", err)
		}
		http.Error(w, "Invalid hash format", http.StatusBadRequest)
		return
	}

	// Look up hash in cache
	servers, exists := h.cache.Get(hash)
	if !exists || len(servers) == 0 {
		if h.verbose {
			log.Printf("[DEBUG] HandleHead: hash %s not found in cache, checking upstream servers", hash)
		}
		// Hash not in cache, check upstream servers using HEAD requests
		servers = h.upstreamManager.CheckHashOnServers(r.Context(), hash)
		if len(servers) == 0 {
			if h.verbose {
				log.Printf("[DEBUG] HandleHead: hash %s not found on any upstream server", hash)
			}
			http.Error(w, "Blob not found", http.StatusNotFound)
			return
		}
		// Update cache with found servers
		h.cache.Add(hash, servers)
		if h.verbose {
			log.Printf("[DEBUG] HandleHead: hash %s found on %d upstream servers, added to cache", hash, len(servers))
		}
	}

	if h.verbose {
		log.Printf("[DEBUG] HandleHead: hash found with %d servers: %v", len(servers), servers)
	}

	// Select the first server that has the blob
	selectedServer, err := h.upstreamManager.SelectServerURL(servers)
	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleHead: failed to select server: %v", err)
		}
		http.Error(w, fmt.Sprintf("Failed to select server: %v", err), http.StatusInternalServerError)
		return
	}

	if h.verbose {
		log.Printf("[DEBUG] HandleHead: selected server: %s", selectedServer)
	}

	// Make HEAD request to the first upstream server that has the blob
	cl, err := h.upstreamManager.GetClient(selectedServer)
	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleHead: failed to get client for %s: %v", selectedServer, err)
		}
		http.Error(w, fmt.Sprintf("Failed to get client: %v", err), http.StatusInternalServerError)
		return
	}

	// Perform HEAD request using client
	resp, err := cl.Head(r.Context(), hash)
	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleHead: HEAD request failed: %v", err)
		}
		http.Error(w, fmt.Sprintf("Request failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// Copy headers from upstream response
	for k, v := range resp.Header {
		for _, val := range v {
			w.Header().Add(k, val)
		}
	}

	// Return the status code from upstream
	w.WriteHeader(resp.StatusCode)

	if h.verbose {
		log.Printf("[DEBUG] HandleHead: proxied HEAD response with status %d from %s", resp.StatusCode, selectedServer)
	}
}

// HandleList handles GET /list/<pubkey> requests
func (h *BlossomHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if h.verbose {
		log.Printf("[DEBUG] HandleList: received %s request from %s", r.Method, r.RemoteAddr)
		log.Printf("[DEBUG] HandleList: path=%s", r.URL.Path)
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract pubkey from path (format: /list/<pubkey>)
	path := strings.TrimPrefix(r.URL.Path, "/list/")
	if path == "" {
		if h.verbose {
			log.Printf("[DEBUG] HandleList: pubkey missing from path")
		}
		http.Error(w, "Pubkey required", http.StatusBadRequest)
		return
	}

	if h.verbose {
		log.Printf("[DEBUG] HandleList: extracted pubkey: %s", path)
	}

	// Query all upstream servers in parallel and merge results
	mergedResults, listResults, err := h.upstreamManager.ListParallelWithResults(r.Context(), path)
	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleList: list request failed: %v", err)
		}
		// Track failures for all servers if operation failed completely
		if listResults != nil {
			for _, result := range listResults {
				if result.Error != nil {
					h.stats.RecordFailure(result.ServerURL, "list")
				} else {
					h.stats.RecordSuccess(result.ServerURL, "list")
				}
			}
		}
		http.Error(w, fmt.Sprintf("List request failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Track stats for all servers based on their individual results
	if listResults != nil {
		for _, result := range listResults {
			if result.Error != nil {
				h.stats.RecordFailure(result.ServerURL, "list")
			} else {
				h.stats.RecordSuccess(result.ServerURL, "list")
			}
		}
	}

	if h.verbose {
		log.Printf("[DEBUG] HandleList: merged %d items from all servers", len(mergedResults))
	}

	// Marshal the merged results to JSON
	responseJSON, err := json.Marshal(mergedResults)
	if err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleList: failed to marshal merged results: %v", err)
		}
		http.Error(w, fmt.Sprintf("Failed to marshal response: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(responseJSON)
}

// HandleDelete handles DELETE /<sha256> requests
func (h *BlossomHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	if h.verbose {
		log.Printf("[DEBUG] HandleDelete: received %s request from %s", r.Method, r.RemoteAddr)
		log.Printf("[DEBUG] HandleDelete: path=%s", r.URL.Path)
	}

	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract hash from path
	hash := strings.TrimPrefix(r.URL.Path, "/")

	if h.verbose {
		log.Printf("[DEBUG] HandleDelete: extracted hash: %s", hash)
	}

	// Validate hash format
	if len(hash) != 64 {
		if h.verbose {
			log.Printf("[DEBUG] HandleDelete: invalid hash length: %d", len(hash))
		}
		http.Error(w, "Invalid hash format", http.StatusBadRequest)
		return
	}

	// Check if hash is valid hex
	if _, err := hex.DecodeString(hash); err != nil {
		if h.verbose {
			log.Printf("[DEBUG] HandleDelete: invalid hash format (not hex): %v", err)
		}
		http.Error(w, "Invalid hash format", http.StatusBadRequest)
		return
	}

	// Get servers that have this blob
	servers, exists := h.cache.Get(hash)
	if !exists {
		if h.verbose {
			log.Printf("[DEBUG] HandleDelete: hash not in cache, using all upstream servers")
		}
		// If not in cache, try all upstream servers
		servers = h.upstreamManager.GetServerURLs()
	} else {
		if h.verbose {
			log.Printf("[DEBUG] HandleDelete: hash found in cache with %d servers: %v", len(servers), servers)
		}
	}

	// Copy headers for authentication
	headers := make(map[string]string)
	for k, v := range r.Header {
		if strings.ToLower(k) == "host" || strings.ToLower(k) == "content-length" {
			continue
		}
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	if h.verbose {
		log.Printf("[DEBUG] HandleDelete: forwarding delete to %d servers", len(servers))
	}

	// Forward delete to all servers that have the blob
	successCount := 0
	for _, serverURL := range servers {
		cl, err := h.upstreamManager.GetClient(serverURL)
		if err != nil {
			if h.verbose {
				log.Printf("[DEBUG] HandleDelete: failed to get client for %s: %v", serverURL, err)
			}
			continue
		}

		err = cl.Delete(r.Context(), hash, headers)
		if err == nil {
			successCount++
			h.stats.RecordSuccess(serverURL, "delete")
			if h.verbose {
				log.Printf("[DEBUG] HandleDelete: successfully deleted from %s", serverURL)
			}
		} else {
			h.stats.RecordFailure(serverURL, "delete")
			if h.verbose {
				log.Printf("[DEBUG] HandleDelete: failed to delete from %s: %v", serverURL, err)
			}
		}
	}

	if h.verbose {
		log.Printf("[DEBUG] HandleDelete: deleted from %d/%d servers", successCount, len(servers))
	}

	// Remove from cache if at least one delete succeeded
	if successCount > 0 {
		h.cache.Remove(hash)
		if h.verbose {
			log.Printf("[DEBUG] HandleDelete: removed hash %s from cache", hash)
		}
		w.WriteHeader(http.StatusNoContent)
	} else {
		if h.verbose {
			log.Printf("[DEBUG] HandleDelete: delete failed on all servers")
		}
		http.Error(w, "Delete failed on all servers", http.StatusInternalServerError)
	}
}

// HandleHealth handles GET /health requests
// Returns 200 OK if system is healthy, 503 Service Unavailable if unhealthy
func (h *BlossomHandler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	healthyCount := h.stats.GetHealthyCount()
	minUploadServers := h.config.Server.MinUploadServers

	allStats := h.stats.GetAll()

	// Get system metrics
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	memoryBytes := int64(m.Alloc)
	goroutines := runtime.NumGoroutine()

	// Check if system metrics are healthy
	memoryHealthy := memoryBytes < h.config.Server.MaxMemoryBytes
	goroutinesHealthy := goroutines < h.config.Server.MaxGoroutines
	serversHealthy := healthyCount >= minUploadServers

	// System is healthy if all checks pass
	systemHealthy := memoryHealthy && goroutinesHealthy && serversHealthy

	response := map[string]interface{}{
		"healthy":            systemHealthy,
		"healthy_count":      healthyCount,
		"min_upload_servers": minUploadServers,
		"memory": map[string]interface{}{
			"bytes":   memoryBytes,
			"max":     h.config.Server.MaxMemoryBytes,
			"healthy": memoryHealthy,
		},
		"goroutines": map[string]interface{}{
			"count":   goroutines,
			"max":     h.config.Server.MaxGoroutines,
			"healthy": goroutinesHealthy,
		},
		"servers": make(map[string]interface{}),
	}

	// Add server health details
	serversMap := response["servers"].(map[string]interface{})
	for url, stats := range allStats {
		serversMap[url] = map[string]interface{}{
			"healthy":              stats.IsHealthy,
			"consecutive_failures": stats.ConsecutiveFailures,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if systemHealthy {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	json.NewEncoder(w).Encode(response)
}

// HandleStats handles GET /stats requests
// Returns detailed statistics for all operations aggregated by upstream server
func (h *BlossomHandler) HandleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	allStats := h.stats.GetAll()

	// Get system metrics
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	memoryBytes := int64(m.Alloc)
	goroutines := runtime.NumGoroutine()

	response := map[string]interface{}{
		"servers": allStats,
		"memory": map[string]interface{}{
			"bytes": memoryBytes,
			"max":   h.config.Server.MaxMemoryBytes,
		},
		"goroutines": map[string]interface{}{
			"count": goroutines,
			"max":   h.config.Server.MaxGoroutines,
		},
	}

	// Calculate totals
	var totalUploadsSuccess, totalUploadsFailure int64
	var totalDownloads int64
	var totalMirrorsSuccess, totalMirrorsFailure int64
	var totalDeletesSuccess, totalDeletesFailure int64
	var totalListsSuccess, totalListsFailure int64

	for _, stats := range allStats {
		totalUploadsSuccess += stats.UploadsSuccess
		totalUploadsFailure += stats.UploadsFailure
		totalDownloads += stats.Downloads
		totalMirrorsSuccess += stats.MirrorsSuccess
		totalMirrorsFailure += stats.MirrorsFailure
		totalDeletesSuccess += stats.DeletesSuccess
		totalDeletesFailure += stats.DeletesFailure
		totalListsSuccess += stats.ListsSuccess
		totalListsFailure += stats.ListsFailure
	}

	response["totals"] = map[string]interface{}{
		"uploads_success": totalUploadsSuccess,
		"uploads_failure": totalUploadsFailure,
		"downloads":       totalDownloads,
		"mirrors_success": totalMirrorsSuccess,
		"mirrors_failure": totalMirrorsFailure,
		"deletes_success": totalDeletesSuccess,
		"deletes_failure": totalDeletesFailure,
		"lists_success":   totalListsSuccess,
		"lists_failure":   totalListsFailure,
	}

	healthyCount := h.stats.GetHealthyCount()
	response["healthy_count"] = healthyCount
	response["total_servers"] = len(allStats)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}
