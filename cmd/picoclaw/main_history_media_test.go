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

	"github.com/bwmarrin/discordgo"
	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestHistoryAttachmentsUseProxyAndStageInLoopWorkspace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/files/report.pdf?token=1" {
			t.Errorf("request URI = %q", r.URL.RequestURI())
		}
		_, _ = w.Write([]byte("history attachment"))
	}))
	defer server.Close()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	fetch := discordHistoryCallback(func(channel string, limit int, before string) ([]*discordgo.Message, error) {
		return []*discordgo.Message{{ID: "history-1", Content: "history text", Attachments: []*discordgo.MessageAttachment{{Filename: "report.pdf", ProxyURL: "http://localhost:13001/files/report.pdf?token=1"}}}}, nil
	})
	provider := &channelHistoryScriptProvider{t: t}
	al := agent.NewAgentLoopWithOptions(cfg, bus.NewMessageBus(), provider, agent.LoopOptions{ToolProfile: agent.ToolProfileSideLane})
	attachChannelHistoryCallback(al, fetch, map[string]string{"http://localhost:13001": server.URL})
	_, err := al.RunJob(context.Background(), bus.InboundMessage{Channel: "discord", ChatID: "room", SenderID: "user", Content: "summarize"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(cfg.WorkspacePath(), ".sciclaw", "inbound", "discord-history-*", "history-1", "report.pdf"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("history paths %v, error %v", matches, err)
	}
	relative, err := filepath.Rel(cfg.WorkspacePath(), matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(provider.toolResultLog, filepath.ToSlash(relative)) {
		t.Fatalf("history result missing staged path: %s", provider.toolResultLog)
	}
	data, err := os.ReadFile(filepath.Join(cfg.WorkspacePath(), filepath.FromSlash(relative)))
	if err != nil || string(data) != "history attachment" {
		t.Fatalf("staged content %q, error %v", data, err)
	}
}

func TestHistoryAttachmentStageFailureReported(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	provider := &channelHistoryScriptProvider{t: t}
	al := agent.NewAgentLoopWithOptions(cfg, bus.NewMessageBus(), provider, agent.LoopOptions{ToolProfile: agent.ToolProfileSideLane})
	attachChannelHistoryCallback(al, func(context.Context, string, int, string) ([]tools.ChannelHistoryMessage, error) {
		return []tools.ChannelHistoryMessage{{ID: "1", Content: "message", Media: []string{filepath.Join(t.TempDir(), "missing")}}}, nil
	}, nil)
	_, err := al.RunJob(context.Background(), bus.InboundMessage{Channel: "discord", ChatID: "room", SenderID: "user", Content: "summarize"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(provider.toolResultLog, "Attachments could not be staged locally") {
		t.Fatalf("missing failure: %s", provider.toolResultLog)
	}
}

func TestHistoryStagingDoesNotOverwriteLiveOrPriorHistoryAndIsBounded(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); _, _ = w.Write([]byte("download")) }))
	defer server.Close()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	live := filepath.Join(cfg.WorkspacePath(), ".sciclaw", "inbound", "discord", "same-id", "file.txt")
	if err := os.MkdirAll(filepath.Dir(live), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("agent edit"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &channelHistoryScriptProvider{t: t}
	al := agent.NewAgentLoopWithOptions(cfg, bus.NewMessageBus(), provider, agent.LoopOptions{ToolProfile: agent.ToolProfileSideLane})
	attachChannelHistoryCallback(al, func(context.Context, string, int, string) ([]tools.ChannelHistoryMessage, error) {
		var messages []tools.ChannelHistoryMessage
		for i := 0; i < 6; i++ {
			messages = append(messages, tools.ChannelHistoryMessage{ID: fmt.Sprintf("same-id-%d", i), Media: []string{server.URL + "/file.txt"}})
		}
		messages[0].ID = "same-id"
		return messages, nil
	}, nil)
	_, err := al.RunJob(context.Background(), bus.InboundMessage{Channel: "discord", ChatID: "room", SenderID: "user", Content: "summarize"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 4 {
		t.Fatalf("requests = %d, want 4", requests.Load())
	}
	if !strings.Contains(provider.toolResultLog, "history attachment limit reached") {
		t.Fatalf("missing limit notice: %s", provider.toolResultLog)
	}
	if data, err := os.ReadFile(live); err != nil || string(data) != "agent edit" {
		t.Fatalf("live file %q: %v", data, err)
	}
	paths, err := filepath.Glob(filepath.Join(cfg.WorkspacePath(), ".sciclaw", "inbound", "discord-history-*", "same-id", "file.txt"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("history paths %v: %v", paths, err)
	}
	if err := os.WriteFile(paths[0], []byte("history edit"), 0600); err != nil {
		t.Fatal(err)
	}
	provider2 := &channelHistoryScriptProvider{t: t}
	al2 := agent.NewAgentLoopWithOptions(cfg, bus.NewMessageBus(), provider2, agent.LoopOptions{ToolProfile: agent.ToolProfileSideLane})
	attachChannelHistoryCallback(al2, func(context.Context, string, int, string) ([]tools.ChannelHistoryMessage, error) {
		return []tools.ChannelHistoryMessage{{ID: "same-id", Media: []string{server.URL + "/file.txt"}}}, nil
	}, nil)
	_, err = al2.RunJob(context.Background(), bus.InboundMessage{Channel: "discord", ChatID: "room", SenderID: "user", Content: "summarize"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(paths[0]); err != nil || string(data) != "history edit" {
		t.Fatalf("prior history file %q: %v", data, err)
	}
}
