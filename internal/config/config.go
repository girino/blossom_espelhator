package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the application configuration
type Config struct {
	UpstreamServers []UpstreamServer `yaml:"upstream_servers"`
	Server          ServerConfig     `yaml:"server"`
}

// UpstreamServer represents an upstream Blossom server configuration
type UpstreamServer struct {
	URL      string `yaml:"url"`
	Priority int    `yaml:"priority"`

	// Capabilities - which endpoints this server supports
	// If not specified in config, defaults are:
	// - supports_mirror: false (not all servers support BUD-04 mirror)
	// - supports_upload_head: false (not all servers support BUD-06 HEAD /upload)
	SupportsMirror     *bool `yaml:"supports_mirror,omitempty"`      // BUD-04: Mirroring
	SupportsUploadHead *bool `yaml:"supports_upload_head,omitempty"` // BUD-06: Upload preflight
}

// ServerConfig represents the proxy server configuration
type ServerConfig struct {
	ListenAddr               string        `yaml:"listen_addr"`
	MinUploadServers         int           `yaml:"min_upload_servers"`
	RedirectStrategy         string        `yaml:"redirect_strategy"`
	DownloadRedirectStrategy string        `yaml:"download_redirect_strategy"` // Fallback redirect strategy for GET requests (defaults to redirect_strategy)
	BaseURL                  string        `yaml:"base_url"`                   // Base URL for local strategy (overrides request-derived URL)
	Timeout                  time.Duration `yaml:"timeout"`
	MaxRetries               int           `yaml:"max_retries"`

	// Health check configuration
	MaxFailures    int   `yaml:"max_failures"`     // Maximum consecutive failures before marking server unhealthy
	MaxGoroutines  int   `yaml:"max_goroutines"`   // Maximum number of goroutines before marking system unhealthy
	MaxMemoryBytes int64 `yaml:"max_memory_bytes"` // Maximum memory usage in bytes before marking system unhealthy

	// Authentication configuration
	AllowedPubkeys []string `yaml:"allowed_pubkeys"` // List of allowed pubkeys (hex format or npub bech32 format). If empty, auth is disabled
}

// Load reads and parses the configuration file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Set defaults
	if config.Server.ListenAddr == "" {
		config.Server.ListenAddr = ":8080"
	}
	if config.Server.MinUploadServers == 0 {
		config.Server.MinUploadServers = 2
	}
	if config.Server.RedirectStrategy == "" {
		config.Server.RedirectStrategy = "round_robin"
	}
	if config.Server.Timeout == 0 {
		config.Server.Timeout = 30 * time.Second
	}
	if config.Server.MaxRetries == 0 {
		config.Server.MaxRetries = 3
	}
	if config.Server.MaxFailures == 0 {
		config.Server.MaxFailures = 5 // Default: 5 consecutive failures before unhealthy
	}
	if config.Server.MaxGoroutines == 0 {
		config.Server.MaxGoroutines = 1000 // Default: 1000 goroutines max
	}
	if config.Server.MaxMemoryBytes == 0 {
		config.Server.MaxMemoryBytes = 512 * 1024 * 1024 // Default: 512 MB
	}

	// Set default capabilities for upstream servers (default to false for optional endpoints)
	for i := range config.UpstreamServers {
		if config.UpstreamServers[i].SupportsMirror == nil {
			defaultMirror := false
			config.UpstreamServers[i].SupportsMirror = &defaultMirror
		}
		if config.UpstreamServers[i].SupportsUploadHead == nil {
			defaultUploadHead := false
			config.UpstreamServers[i].SupportsUploadHead = &defaultUploadHead
		}
	}

	// Validate configuration
	if len(config.UpstreamServers) < config.Server.MinUploadServers {
		return nil, fmt.Errorf("not enough upstream servers: need at least %d, got %d",
			config.Server.MinUploadServers, len(config.UpstreamServers))
	}

	return &config, nil
}
