package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	mux.HandleFunc("/v1/workspace-output-marker", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || dispatcher == nil {
			http.NotFound(w, r)
			return
		}
		channel, marker := r.URL.Query().Get("channel"), r.URL.Query().Get("marker")
		if !regexp.MustCompile(`^[0-9]+$`).MatchString(channel) || !regexp.MustCompile(`^\.sciclaw-check-[0-9a-f]{32}$`).MatchString(marker) {
			http.NotFound(w, r)
			return
		}
		active := dispatcher.ActiveRouting()
		workspace := ""
		for _, m := range active.Mappings {
			if strings.EqualFold(strings.TrimSpace(m.Channel), "discord") && strings.TrimSpace(m.ChatID) == channel {
				if workspace != "" {
					http.NotFound(w, r)
					return
				}
				workspace = m.Workspace
			}
		}
		if !active.Enabled || !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
			http.NotFound(w, r)
			return
		}
		f, err := openOutputMarker(workspace, marker)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != 64 {
			http.NotFound(w, r)
			return
		}
		data, err := io.ReadAll(io.LimitReader(f, 65))
		if err != nil || len(data) != 64 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
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
