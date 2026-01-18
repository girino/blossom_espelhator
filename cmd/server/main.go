package main

import (
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/girino/blossom_espelhator/internal/cache"
	"github.com/girino/blossom_espelhator/internal/config"
	"github.com/girino/blossom_espelhator/internal/handler"
	"github.com/girino/blossom_espelhator/internal/stats"
	"github.com/girino/blossom_espelhator/internal/upstream"
)

func main() {
	configPath := flag.String("config", "config/config.yaml", "Path to configuration file")
	verbose := flag.Bool("v", false, "Enable verbose debug logging")
	flag.BoolVar(verbose, "verbose", false, "Enable verbose debug logging")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize cache
	cache := cache.New()

	// Initialize stats tracker
	statsTracker := stats.New(cfg.Server.MaxFailures)

	// Initialize upstream manager
	upstreamManager, err := upstream.New(cfg, *verbose)
	if err != nil {
		log.Fatalf("Failed to initialize upstream manager: %v", err)
	}

	// Initialize stats for all upstream servers (they all start as healthy)
	allServerURLs := upstreamManager.GetServerURLs()
	statsTracker.InitializeServers(allServerURLs)

	// Initialize handler
	blossomHandler := handler.New(upstreamManager, cache, statsTracker, cfg, *verbose)

	// Setup routes
	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("/health", blossomHandler.HandleHealth)

	// Stats endpoint
	mux.HandleFunc("/stats", blossomHandler.HandleStats)

	// Upload endpoint
	mux.HandleFunc("/upload", blossomHandler.HandleUpload)

	// Mirror endpoint
	mux.HandleFunc("/mirror", blossomHandler.HandleMirror)

	// List endpoint
	mux.HandleFunc("/list/", blossomHandler.HandleList)

	// Home page endpoint
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" && r.Method == http.MethodGet {
			blossomHandler.HandleHome(w, r)
			return
		}

		// Download and Delete endpoints (hash-based)
		// These need to be handled by a catch-all that validates the hash format

		// Remove leading slash
		hashPath := path[1:]

		// Extract hash - take first 64 characters (hash may be followed by file extension)
		var hash string
		if len(hashPath) >= 64 {
			// Check if first 64 characters are valid hex
			hashCandidate := hashPath[:64]
			// Simple check: if all characters after position 64 are not hex, it's likely an extension
			if len(hashPath) > 64 {
				// Assume first 64 chars are the hash
				hash = hashCandidate
			} else {
				hash = hashCandidate
			}
		} else {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		// Validate hash is 64 hex characters
		if len(hash) == 64 {
			// Verify it's valid hex
			if _, err := hex.DecodeString(hash); err == nil {
				// Modify the request path to contain only the hash for handlers
				r.URL.Path = "/" + hash
				switch r.Method {
				case http.MethodGet:
					blossomHandler.HandleDownload(w, r)
				case http.MethodHead:
					blossomHandler.HandleHead(w, r)
				case http.MethodDelete:
					blossomHandler.HandleDelete(w, r)
				default:
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				}
				return
			}
		}

		http.Error(w, "Not found", http.StatusNotFound)
	})

	// Create HTTP server
	server := &http.Server{
		Addr:    cfg.Server.ListenAddr,
		Handler: mux,
	}

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start server in a goroutine
	go func() {
		log.Printf("Starting Blossom proxy server on %s", cfg.Server.ListenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Wait for interrupt signal
	<-sigChan
	log.Println("Shutting down server...")

	// Server shutdown is handled automatically by the OS
	// In a production environment, you might want to use server.Shutdown(context)
}
