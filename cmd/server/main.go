package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/girino/blossom_espelhator/internal/cache"
	"github.com/girino/blossom_espelhator/internal/config"
	"github.com/girino/blossom_espelhator/internal/handler"
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

	// Initialize upstream manager
	upstreamManager, err := upstream.New(cfg, *verbose)
	if err != nil {
		log.Fatalf("Failed to initialize upstream manager: %v", err)
	}

	// Initialize handler
	blossomHandler := handler.New(upstreamManager, cache, *verbose)

	// Setup routes
	mux := http.NewServeMux()

	// Upload endpoint
	mux.HandleFunc("/upload", blossomHandler.HandleUpload)

	// List endpoint
	mux.HandleFunc("/list/", blossomHandler.HandleList)

	// Download and Delete endpoints (hash-based)
	// These need to be handled by a catch-all that validates the hash format
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		// Remove leading slash to get hash
		hash := path[1:]

		// Check if it's a valid hash (64 hex characters)
		if len(hash) == 64 {
			switch r.Method {
			case http.MethodGet:
				blossomHandler.HandleDownload(w, r)
			case http.MethodDelete:
				blossomHandler.HandleDelete(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		} else {
			http.Error(w, "Not found", http.StatusNotFound)
		}
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
