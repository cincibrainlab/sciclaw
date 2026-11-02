package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/routing"
)

// startRoutingStatus creates a private directory exclusively so neither stale
// sockets nor another process's files are removed or trusted.
func startRoutingStatus(ctx context.Context, dir string, dispatcher *routing.Dispatcher) (<-chan struct{}, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("routing status directory must be absolute")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, fmt.Errorf("create routing status directory: %w", err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "status.sock"))
	if err != nil {
		os.Remove(dir)
		return nil, fmt.Errorf("listen for routing status: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/routing", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		active := config.RoutingConfig{Mappings: []config.RoutingMapping{}}
		if dispatcher != nil {
			active = dispatcher.ActiveRouting()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(active)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = server.Close()
		_ = listener.Close()
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer os.Remove(dir)
		_ = server.Serve(listener)
	}()
	return done, nil
}
