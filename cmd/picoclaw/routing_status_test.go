package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/routing"
)

func TestRoutingStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	parent, err := os.MkdirTemp("", "rs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(parent) })
	dir := filepath.Join(parent, "private")
	cfg := config.DefaultConfig()
	cfg.Routing.Enabled = true
	cfg.Routing.Mappings = []config.RoutingMapping{{Channel: "discord", ChatID: "123", Workspace: t.TempDir(), AllowedSenders: config.FlexibleStringSlice{"alice"}}}
	resolver, err := routing.NewResolver(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := routing.NewDispatcher(nil, resolver, nil)
	if err := startRoutingStatus(ctx, dir, dispatcher); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("directory permissions: %v, %v", info, err)
	}
	if err := startRoutingStatus(ctx, dir, dispatcher); err == nil {
		t.Fatal("existing directory accepted")
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, "status.sock"))
	}}}
	defer client.CloseIdleConnections()
	get := func() config.RoutingConfig {
		t.Helper()
		resp, err := client.Get("http://unix/v1/routing")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var active config.RoutingConfig
		if err := json.NewDecoder(resp.Body).Decode(&active); err != nil {
			t.Fatal(err)
		}
		return active
	}
	active := get()
	if !active.Enabled || len(active.Mappings) != 1 || active.Mappings[0].ChatID != "123" {
		t.Fatalf("initial: %+v", active)
	}
	cfg.Routing.Mappings[0].ChatID = "456"
	if get().Mappings[0].ChatID != "123" {
		t.Fatal("config mutation changed active state")
	}
	newResolver, err := routing.NewResolver(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher.ReplaceResolver(newResolver)
	if get().Mappings[0].ChatID != "456" {
		t.Fatal("replacement not visible")
	}
	cfg.Routing.Mappings[0].Workspace = "relative"
	if _, err := routing.NewResolver(cfg); err == nil {
		t.Fatal("invalid reload accepted")
	}
	if get().Mappings[0].ChatID != "456" {
		t.Fatal("failed reload changed active state")
	}
	cfg.Routing.Enabled = false
	cfg.Routing.Mappings = nil
	newResolver, err = routing.NewResolver(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher.ReplaceResolver(newResolver)
	if get().Enabled {
		t.Fatal("disabled resolver reported enabled")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			dispatcher.ReplaceResolver(resolver)
			dispatcher.ReplaceResolver(newResolver)
		}
	}()
	for i := 0; i < 100; i++ {
		state := get()
		if state.Enabled != (len(state.Mappings) == 1) {
			t.Fatalf("inconsistent snapshot: %+v", state)
		}
	}
	<-done
	resp, err := client.Post("http://unix/v1/routing", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", resp.StatusCode)
	}
}

func TestRoutingStatusWithoutDispatcher(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	parent, err := os.MkdirTemp("", "rs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(parent) })
	dir := filepath.Join(parent, "private")
	if err := startRoutingStatus(ctx, dir, nil); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, "status.sock"))
	}}}
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://unix/v1/routing")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var active config.RoutingConfig
	if err := json.NewDecoder(resp.Body).Decode(&active); err != nil {
		t.Fatal(err)
	}
	if active.Enabled || len(active.Mappings) != 0 {
		t.Fatalf("unexpected state: %+v", active)
	}
}
