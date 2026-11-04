package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	relative := ".sciclaw/inbound/discord/history-1/report.pdf"
	if !strings.Contains(provider.toolResultLog, relative) {
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
