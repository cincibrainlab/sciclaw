package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	matches, err := filepath.Glob(filepath.Join(cfg.WorkspacePath(), ".sciclaw", "inbound", "discord-history", "*", "report.pdf"))
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
	attachChannelHistoryCallback(al, func(context.Context, string, int, string, int) ([]tools.ChannelHistoryMessage, error) {
		return []tools.ChannelHistoryMessage{{ID: "1", Content: "message", Media: []string{filepath.Join(t.TempDir(), "missing")}}}, nil
	}, nil)
	_, err := al.RunJob(context.Background(), bus.InboundMessage{Channel: "discord", ChatID: "room", SenderID: "user", Content: "summarize"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(provider.toolResultLog, "Attachment could not be staged") {
		t.Fatalf("missing failure: %s", provider.toolResultLog)
	}
}

func TestHistoryPaginationCacheAndNoOverwrite(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); _, _ = w.Write([]byte("download")) }))
	defer server.Close()
	workspace := t.TempDir()
	live := filepath.Join(workspace, ".sciclaw", "inbound", "discord", "newest", "file-0.txt")
	if err := os.MkdirAll(filepath.Dir(live), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("agent edit"), 0600); err != nil {
		t.Fatal(err)
	}
	makeMessages := func() []tools.ChannelHistoryMessage {
		return []tools.ChannelHistoryMessage{
			{ID: "oldest", Media: []string{server.URL + "/old.txt"}},
			{ID: "newest", Media: []string{server.URL + "/file-0.txt", server.URL + "/file-1.txt", server.URL + "/file-2.txt", server.URL + "/file-3.txt", server.URL + "/file-4.txt"}},
		}
	}
	messages, err := prepareHistoryMessages(context.Background(), workspace, "room", makeMessages(), 0, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 4 || !strings.Contains(messages[1].Content, "attachment_offset=4") || !strings.Contains(messages[0].Content, "attachment_offset=5") {
		t.Fatalf("requests=%d messages=%+v", requests.Load(), messages)
	}
	files, err := filepath.Glob(filepath.Join(workspace, ".sciclaw", "inbound", "discord-history", "*", "file-0.txt"))
	if err != nil || len(files) != 1 {
		t.Fatalf("cache files %v: %v", files, err)
	}
	if err := os.WriteFile(files[0], []byte("history edit"), 0600); err != nil {
		t.Fatal(err)
	}
	messages, err = prepareHistoryMessages(context.Background(), workspace, "room", makeMessages(), 0, nil, time.Second)
	if err != nil || requests.Load() != 4 {
		t.Fatalf("cache requests=%d err=%v", requests.Load(), err)
	}
	if data, err := os.ReadFile(files[0]); err != nil || string(data) != "history edit" {
		t.Fatalf("cache edit %q: %v", data, err)
	}
	if data, err := os.ReadFile(live); err != nil || string(data) != "agent edit" {
		t.Fatalf("live edit %q: %v", data, err)
	}
	messages, err = prepareHistoryMessages(context.Background(), workspace, "room", makeMessages(), 4, nil, time.Second)
	if err != nil || requests.Load() != 6 || !strings.Contains(messages[1].Content, "file-4.txt") || !strings.Contains(messages[0].Content, "old.txt") {
		t.Fatalf("offset requests=%d messages=%+v error=%v", requests.Load(), messages, err)
	}
}

func TestHistoryDeadlinePreservesEarlierAttachmentsInSameMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte("first"))
	}))
	defer server.Close()
	workspace := t.TempDir()
	messages, err := prepareHistoryMessages(context.Background(), workspace, "room", []tools.ChannelHistoryMessage{{ID: "1", Media: []string{server.URL + "/first.txt", server.URL + "/slow"}}}, 0, nil, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(messages[0].Content, "first.txt") || !strings.Contains(messages[0].Content, "attachment_offset=1") {
		t.Fatalf("history content: %s", messages[0].Content)
	}
	files, err := filepath.Glob(filepath.Join(workspace, ".sciclaw", "inbound", "discord-history", "*", "first.txt"))
	if err != nil || len(files) != 1 {
		t.Fatalf("cached files %v: %v", files, err)
	}
}

func TestHistoryCacheUsesAttachmentIDAcrossSignedURLChanges(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); _, _ = w.Write([]byte("original")) }))
	defer server.Close()
	workspace := t.TempDir()
	first, err := stageHistoryAttachment(context.Background(), workspace, "room", "msg", 0, "attachment-id", server.URL+"/file.txt?token=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, filepath.FromSlash(first)), []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := stageHistoryAttachment(context.Background(), workspace, "room", "msg", 2, "attachment-id", server.URL+"/file.txt?token=2", nil)
	if err != nil || second != first || requests.Load() != 1 {
		t.Fatalf("first=%q second=%q requests=%d err=%v", first, second, requests.Load(), err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(second)))
	if err != nil || string(data) != "edited" {
		t.Fatalf("file %q: %v", data, err)
	}
}

func TestHistoryCacheConcurrentStagesDoNotOverwrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("contents")) }))
	defer server.Close()
	workspace := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := stageHistoryAttachment(context.Background(), workspace, "room", "msg", 0, "id", server.URL+"/file.txt", nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	files, err := filepath.Glob(filepath.Join(workspace, ".sciclaw", "inbound", "discord-history", "*", "file.txt"))
	if err != nil || len(files) != 1 {
		t.Fatalf("files %v: %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil || string(data) != "contents" {
		t.Fatalf("content %q: %v", data, err)
	}
}
