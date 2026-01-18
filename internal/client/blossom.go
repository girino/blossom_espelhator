package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// Client is an HTTP client for communicating with Blossom servers
type Client struct {
	httpClient *http.Client
	baseURL    string
	verbose    bool
}

// New creates a new Blossom client
func New(baseURL string, timeout time.Duration, verbose bool) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		baseURL: baseURL,
		verbose: verbose,
	}
}

// Upload uploads a blob to the Blossom server
// The request should include the file data and Nostr event in the body
// Returns the response body on success
func (c *Client) Upload(ctx context.Context, body io.Reader, contentType string, headers map[string]string) ([]byte, error) {
	url := fmt.Sprintf("%s/upload", c.baseURL)

	if c.verbose {
		log.Printf("[DEBUG] Client.Upload: %s - method=PUT, content-type=%s", c.baseURL, contentType)
		log.Printf("[DEBUG] Client.Upload: headers=%v", headers)
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	// Copy additional headers (e.g., Nostr event headers)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Explicitly don't request compression (responses are short JSON, uncompressed is fine)
	req.Header.Del("Accept-Encoding")

	if c.verbose {
		log.Printf("[DEBUG] Client.Upload: sending request to %s", url)
	}

	startTime := time.Now()
	resp, err := c.httpClient.Do(req)
	duration := time.Since(startTime)

	if err != nil {
		if c.verbose {
			log.Printf("[DEBUG] Client.Upload: request failed after %v: %v", duration, err)
		}
		return nil, fmt.Errorf("upload request failed: %w", err)
	}
	defer resp.Body.Close()

	if c.verbose {
		log.Printf("[DEBUG] Client.Upload: response received after %v - status=%d, headers=%v", duration, resp.StatusCode, resp.Header)
	}

	// Read response body (gzip will be automatically decompressed by Go's http client)
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		if c.verbose {
			log.Printf("[DEBUG] Client.Upload: failed to read response body: %v", err)
		}
		bodyBytes = nil
	}

	// Accept 200, 201, and 202 as success status codes
	// 200 = OK, 201 = Created, 202 = Accepted (queued for processing)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		bodyStr := string(bodyBytes)
		if bodyStr == "" {
			bodyStr = "(empty response body)"
		}
		if c.verbose {
			log.Printf("[DEBUG] Client.Upload: upload failed - status=%d, body=%s", resp.StatusCode, bodyStr)
		}
		return nil, NewHTTPError(resp.StatusCode, bodyStr)
	}

	if c.verbose {
		log.Printf("[DEBUG] Client.Upload: upload successful, response body: %s", string(bodyBytes))
	}

	return bodyBytes, nil
}

// Download checks if a blob exists at the server (returns the URL)
func (c *Client) Download(ctx context.Context, hash string) (string, error) {
	url := fmt.Sprintf("%s/%s", c.baseURL, hash)

	if c.verbose {
		log.Printf("[DEBUG] Client.Download: checking %s for hash %s", c.baseURL, hash)
	}

	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Explicitly don't request compression (responses are short, uncompressed is fine)
	req.Header.Del("Accept-Encoding")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if c.verbose {
			log.Printf("[DEBUG] Client.Download: request failed: %v", err)
		}
		return "", fmt.Errorf("download check failed: %w", err)
	}
	defer resp.Body.Close()

	if c.verbose {
		log.Printf("[DEBUG] Client.Download: response status=%d", resp.StatusCode)
	}

	if resp.StatusCode == http.StatusOK {
		return url, nil
	}

	return "", fmt.Errorf("blob not found: status %d", resp.StatusCode)
}

// List retrieves the list of blobs for a given pubkey
func (c *Client) List(ctx context.Context, pubkey string) ([]byte, error) {
	url := fmt.Sprintf("%s/list/%s", c.baseURL, pubkey)

	if c.verbose {
		log.Printf("[DEBUG] Client.List: listing blobs for pubkey %s on %s", pubkey, c.baseURL)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Explicitly don't request compression (responses are short, uncompressed is fine)
	req.Header.Del("Accept-Encoding")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if c.verbose {
			log.Printf("[DEBUG] Client.List: request failed: %v", err)
		}
		return nil, fmt.Errorf("list request failed: %w", err)
	}
	defer resp.Body.Close()

	if c.verbose {
		log.Printf("[DEBUG] Client.List: response status=%d", resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if c.verbose {
		log.Printf("[DEBUG] Client.List: received %d bytes", len(body))
	}

	return body, nil
}

// Delete deletes a blob from the server
func (c *Client) Delete(ctx context.Context, hash string, headers map[string]string) error {
	url := fmt.Sprintf("%s/%s", c.baseURL, hash)

	if c.verbose {
		log.Printf("[DEBUG] Client.Delete: deleting hash %s from %s", hash, c.baseURL)
		log.Printf("[DEBUG] Client.Delete: headers=%v", headers)
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Copy headers (e.g., authentication headers)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Explicitly don't request compression (responses are short, uncompressed is fine)
	req.Header.Del("Accept-Encoding")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if c.verbose {
			log.Printf("[DEBUG] Client.Delete: request failed: %v", err)
		}
		return fmt.Errorf("delete request failed: %w", err)
	}
	defer resp.Body.Close()

	if c.verbose {
		log.Printf("[DEBUG] Client.Delete: response status=%d", resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		bodyBytes, _ := io.ReadAll(resp.Body)
		if c.verbose {
			log.Printf("[DEBUG] Client.Delete: delete failed - status=%d, body=%s", resp.StatusCode, string(bodyBytes))
		}
		return fmt.Errorf("delete failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	if c.verbose {
		log.Printf("[DEBUG] Client.Delete: delete successful")
	}

	return nil
}

// CheckHealth checks if the server is reachable
func (c *Client) CheckHealth(ctx context.Context) error {
	// Try to access a non-existent blob to check if server responds
	url := fmt.Sprintf("%s/0000000000000000000000000000000000000000000000000000000000000000", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Explicitly don't request compression (responses are short, uncompressed is fine)
	req.Header.Del("Accept-Encoding")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()

	// Any response (even 404) means the server is reachable
	return nil
}

// GetBaseURL returns the base URL of the client
func (c *Client) GetBaseURL() string {
	return c.baseURL
}

// Head performs a HEAD request to check if a blob exists and returns the response
func (c *Client) Head(ctx context.Context, hash string) (*http.Response, error) {
	url := fmt.Sprintf("%s/%s", c.baseURL, hash)

	if c.verbose {
		log.Printf("[DEBUG] Client.Head: checking %s for hash %s", c.baseURL, hash)
	}

	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Explicitly don't request compression (responses are short, uncompressed is fine)
	req.Header.Del("Accept-Encoding")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if c.verbose {
			log.Printf("[DEBUG] Client.Head: request failed: %v", err)
		}
		return nil, fmt.Errorf("head request failed: %w", err)
	}

	if c.verbose {
		log.Printf("[DEBUG] Client.Head: response status=%d, headers=%v", resp.StatusCode, resp.Header)
	}

	return resp, nil
}

// HeadUpload performs a HEAD request to /upload to check upload requirements (BUD-06)
// The request should include headers: X-SHA-256, X-Content-Length, X-Content-Type
// Returns the HTTP response with headers including X-Reason if rejected
func (c *Client) HeadUpload(ctx context.Context, headers map[string]string) (*http.Response, error) {
	url := fmt.Sprintf("%s/upload", c.baseURL)

	if c.verbose {
		log.Printf("[DEBUG] Client.HeadUpload: checking %s with headers: %v", c.baseURL, headers)
	}

	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Copy headers (X-SHA-256, X-Content-Length, X-Content-Type, etc.)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Explicitly don't request compression (responses are short, uncompressed is fine)
	req.Header.Del("Accept-Encoding")

	if c.verbose {
		log.Printf("[DEBUG] Client.HeadUpload: sending HEAD request to %s", url)
	}

	startTime := time.Now()
	resp, err := c.httpClient.Do(req)
	duration := time.Since(startTime)

	if err != nil {
		if c.verbose {
			log.Printf("[DEBUG] Client.HeadUpload: request failed after %v: %v", duration, err)
		}
		return nil, fmt.Errorf("head upload request failed: %w", err)
	}

	if c.verbose {
		log.Printf("[DEBUG] Client.HeadUpload: response received after %v - status=%d, headers=%v", duration, resp.StatusCode, resp.Header)
	}

	return resp, nil
}

// Mirror sends a PUT /mirror request to request mirroring of a blob (BUD-04)
// The request body should contain blob metadata/hash
// Headers should include authentication (Nostr event)
// Returns the response body on success
func (c *Client) Mirror(ctx context.Context, body io.Reader, contentType string, headers map[string]string) ([]byte, error) {
	url := fmt.Sprintf("%s/mirror", c.baseURL)

	if c.verbose {
		log.Printf("[DEBUG] Client.Mirror: %s - method=PUT, content-type=%s", c.baseURL, contentType)
		log.Printf("[DEBUG] Client.Mirror: headers=%v", headers)
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	// Copy additional headers (e.g., Nostr event headers)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Explicitly don't request compression (responses are short, uncompressed is fine)
	req.Header.Del("Accept-Encoding")

	if c.verbose {
		log.Printf("[DEBUG] Client.Mirror: sending request to %s", url)
	}

	startTime := time.Now()
	resp, err := c.httpClient.Do(req)
	duration := time.Since(startTime)

	if err != nil {
		if c.verbose {
			log.Printf("[DEBUG] Client.Mirror: request failed after %v: %v", duration, err)
		}
		return nil, fmt.Errorf("mirror request failed: %w", err)
	}
	defer resp.Body.Close()

	if c.verbose {
		log.Printf("[DEBUG] Client.Mirror: response received after %v - status=%d, headers=%v", duration, resp.StatusCode, resp.Header)
	}

	// Read response body (gzip will be automatically decompressed by Go's http client)
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		if c.verbose {
			log.Printf("[DEBUG] Client.Mirror: failed to read response body: %v", err)
		}
		bodyBytes = nil
	}

	// Accept 200, 201, and 202 as success status codes
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		bodyStr := string(bodyBytes)
		if bodyStr == "" {
			bodyStr = "(empty response body)"
		}
		if c.verbose {
			log.Printf("[DEBUG] Client.Mirror: mirror request failed - status=%d, body=%s", resp.StatusCode, bodyStr)
		}
		return nil, NewHTTPError(resp.StatusCode, bodyStr)
	}

	if c.verbose {
		log.Printf("[DEBUG] Client.Mirror: mirror request successful, response body: %s", string(bodyBytes))
	}

	return bodyBytes, nil
}

// UploadWithBody uploads using a byte slice body
func (c *Client) UploadWithBody(ctx context.Context, body []byte, contentType string, headers map[string]string) ([]byte, error) {
	return c.Upload(ctx, bytes.NewReader(body), contentType, headers)
}
