package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestHistoryStagesProxyAttachmentsWithoutOverwriting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "token=1" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte("attachment"))
	}))
	defer server.Close()
	fetch := discordHistoryCallback(func(string, int, string) ([]*discordgo.Message, error) {
		return []*discordgo.Message{{ID: "1", Attachments: []*discordgo.MessageAttachment{{ProxyURL: "http://public.invalid/file.txt?token=1"}}}}, nil
	})
	workspace := t.TempDir()
	var rewrites atomic.Pointer[map[string]string]
	mapping := map[string]string{"http://public.invalid": server.URL}
	rewrites.Store(&mapping)
	callback := historyStagingCallback(workspace, fetch, &rewrites)
	for i := 0; i < 2; i++ {
		messages, err := callback(context.Background(), "room", 10, "")
		if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Content, "Attachments staged locally") {
			t.Fatalf("messages = %+v, err = %v", messages, err)
		}
		paths, err := filepath.Glob(filepath.Join(workspace, ".sciclaw", "inbound", "discord-history", "message-*", "file.txt"))
		if err != nil || len(paths) != i+1 {
			t.Fatalf("paths = %v, err = %v", paths, err)
		}
		if i == 0 {
			if err := os.WriteFile(paths[0], []byte("edited"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	paths, _ := filepath.Glob(filepath.Join(workspace, ".sciclaw", "inbound", "discord-history", "message-*", "file.txt"))
	preserved := false
	for _, p := range paths {
		data, _ := os.ReadFile(p)
		if string(data) == "edited" {
			preserved = true
		}
	}
	if !preserved {
		t.Fatal("edited attachment overwritten")
	}
}

func TestHistoryStagingCancellation(t *testing.T) {
	fetch := discordHistoryCallback(func(string, int, string) ([]*discordgo.Message, error) {
		return []*discordgo.Message{{ID: "1", Attachments: []*discordgo.MessageAttachment{{URL: "http://example.invalid/file"}}}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := historyStagingCallback(t.TempDir(), fetch, nil)(ctx, "room", 10, "")
	if err != context.Canceled {
		t.Fatalf("error = %v", err)
	}
}

func TestHistoryStagingDirectoryFailurePreservesMessages(t *testing.T) {
	for _, failure := range []string{"root", "temp"} {
		t.Run(failure, func(t *testing.T) {
			if failure == "temp" && os.Geteuid() == 0 {
				t.Skip("root can write read-only directories")
			}
			workspace := t.TempDir()
			if failure == "root" {
				if err := os.WriteFile(filepath.Join(workspace, ".sciclaw"), []byte("block directory"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				root := filepath.Join(workspace, ".sciclaw", "inbound", "discord-history")
				if err := os.MkdirAll(root, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(root, 0500); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(root, 0700)
			}
			fetch := func(context.Context, string, int, string) ([]tools.ChannelHistoryMessage, error) {
				return []tools.ChannelHistoryMessage{{Content: "first", Media: []string{"http://example.invalid/file"}}, {Content: "text only"}, {Content: "last", Media: []string{"http://example.invalid/other"}}}, nil
			}
			messages, err := historyStagingCallback(workspace, fetch, nil)(context.Background(), "room", 10, "")
			if err != nil || len(messages) != 3 {
				t.Fatalf("messages = %+v, error = %v", messages, err)
			}
			for _, i := range []int{0, 2} {
				if !strings.Contains(messages[i].Content, "Attachments could not be staged locally.") {
					t.Fatalf("message %d = %q", i, messages[i].Content)
				}
			}
			if messages[1].Content != "text only" || !strings.HasPrefix(messages[0].Content, "first") || !strings.HasPrefix(messages[2].Content, "last") {
				t.Fatalf("message text not preserved: %+v", messages)
			}
		})
	}
}

func TestHistoryStagingUsesRoutingReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("old")) }))
	defer oldServer.Close()
	newServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("new")) }))
	defer newServer.Close()
	for _, routed := range []bool{false, true} {
		t.Run(fmt.Sprintf("routed=%t", routed), func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Routing.InboundMediaURLRewrites = map[string]string{"http://public.invalid": newServer.URL}
			if err := config.SaveConfig(getConfigPath(), cfg); err != nil {
				t.Fatal(err)
			}
			initial := map[string]string{"http://public.invalid": oldServer.URL}
			var rewrites atomic.Pointer[map[string]string]
			rewrites.Store(&initial)
			var dispatcher *routing.Dispatcher
			if routed {
				resolver, err := routing.NewResolver(cfg)
				if err != nil {
					t.Fatal(err)
				}
				dispatcher = routing.NewDispatcher(nil, resolver, nil)
			}
			fetch := discordHistoryCallback(func(string, int, string) ([]*discordgo.Message, error) {
				return []*discordgo.Message{{ID: "1", Attachments: []*discordgo.MessageAttachment{{URL: "http://public.invalid/file.txt"}}}}, nil
			})
			workspace := t.TempDir()
			callback := historyStagingCallback(workspace, fetch, &rewrites)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); watchRoutingReload(ctx, dispatcher, &rewrites) }()
			defer func() { cancel(); <-done }()
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			timeout := time.NewTimer(8 * time.Second)
			defer timeout.Stop()
			for {
				select {
				case <-timeout.C:
					t.Fatal("routing reload did not update history rewrites")
				case <-ticker.C:
					if err := requestRoutingReload(); err != nil {
						t.Fatal(err)
					}
					if currentInboundMediaRewrites(&rewrites)["http://public.invalid"] != newServer.URL {
						continue
					}
					messages, err := callback(ctx, "room", 10, "")
					if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Content, "Attachments staged locally") {
						t.Fatalf("messages = %+v, err = %v", messages, err)
					}
					paths, _ := filepath.Glob(filepath.Join(workspace, ".sciclaw", "inbound", "discord-history", "message-*", "file.txt"))
					if len(paths) != 1 {
						t.Fatalf("paths = %v", paths)
					}
					data, err := os.ReadFile(paths[0])
					if err != nil || string(data) != "new" {
						t.Fatalf("data = %q, err = %v", data, err)
					}
					return
				}
			}
		})
	}
}
