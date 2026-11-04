package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
)

type captureJobRunner struct {
	received chan bus.InboundMessage
}

func (r *captureJobRunner) HandleInbound(context.Context, bus.InboundMessage) {}

func (r *captureJobRunner) RunJob(_ context.Context, msg bus.InboundMessage, _ func(phase, detail string)) (string, error) {
	r.received <- msg
	return "done", nil
}

func TestRunGatewayDefaultDispatch_StagesInboundMediaBeforeJob(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/attachments/cherry.jpeg" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte("cherry"))
	}))
	defer server.Close()

	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	runner := &captureJobRunner{received: make(chan bus.InboundMessage, 1)}
	jobManager, err := routing.NewJobManager(filepath.Join(t.TempDir(), "jobs.json"), config.JobsConfig{Enabled: true}, messageBus, nil, func(routing.LoopTarget) (routing.JobRunner, error) {
		return runner, nil
	})
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspace := t.TempDir()
	go runGatewayDefaultDispatch(ctx, messageBus, nil, jobManager, routing.LoopTarget{Workspace: workspace, Runtime: routing.RuntimeProfile{Mode: config.ModeCloud}}, nil)
	if err := messageBus.PublishInbound(ctx, bus.InboundMessage{
		Channel:  "discord",
		ChatID:   "channel-1",
		Content:  "review this attachment",
		Media:    []string{server.URL + "/attachments/cherry.jpeg"},
		Metadata: map[string]string{"message_id": "message-1"},
	}); err != nil {
		t.Fatalf("PublishInbound: %v", err)
	}

	select {
	case got := <-runner.received:
		if len(got.Media) != 1 {
			t.Fatalf("media = %#v", got.Media)
		}
		if !strings.HasPrefix(filepath.Dir(got.Media[0]), filepath.Join(workspace, ".sciclaw", "inbound", "discord", "message-1-")) {
			t.Fatalf("media path = %q, want a canonical path under %q", got.Media[0], workspace)
		}
		bytes, err := os.ReadFile(got.Media[0])
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(bytes) != "cherry" {
			t.Fatalf("file contents = %q", bytes)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for job")
	}
}

type captureDispatchProvider struct{ messages chan []providers.Message }

func (p *captureDispatchProvider) Chat(_ context.Context, messages []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]interface{}) (*providers.LLMResponse, error) {
	p.messages <- messages
	return &providers.LLMResponse{Content: "done"}, nil
}
func (p *captureDispatchProvider) GetDefaultModel() string { return "mock-model" }

func TestRunGatewayDefaultDispatch_StagesWithoutJobManager(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("attachment")) }))
	defer server.Close()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	provider := &captureDispatchProvider{messages: make(chan []providers.Message, 1)}
	al := agent.NewAgentLoop(cfg, messageBus, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go runGatewayDefaultDispatch(ctx, messageBus, al, nil, routing.LoopTarget{Workspace: cfg.WorkspacePath()}, nil)
	if err := messageBus.PublishInbound(ctx, bus.InboundMessage{Channel: "discord", ChatID: "room", SenderID: "user", Content: "inspect", Media: []string{server.URL + "/file.txt"}, Metadata: map[string]string{"message_id": "msg-1"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case messages := <-provider.messages:
		var contents strings.Builder
		for _, msg := range messages {
			contents.WriteString(msg.Content)
		}
		matches, err := filepath.Glob(filepath.Join(cfg.WorkspacePath(), ".sciclaw", "inbound", "discord", "msg-1-*", "file.txt"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("staged paths = %v, error = %v", matches, err)
		}
		relative, err := filepath.Rel(cfg.WorkspacePath(), matches[0])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(contents.String(), filepath.ToSlash(relative)) {
			t.Fatalf("prompt missing staged path: %s", contents.String())
		}
		data, err := os.ReadFile(filepath.Join(cfg.WorkspacePath(), filepath.FromSlash(relative)))
		if err != nil || string(data) != "attachment" {
			t.Fatalf("staged content %q, error %v", data, err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for provider")
	}
}

func TestRunGatewayDefaultDispatch_PreservesTextWhenAttachmentsFail(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	provider := &captureDispatchProvider{messages: make(chan []providers.Message, 1)}
	al := agent.NewAgentLoop(cfg, messageBus, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go runGatewayDefaultDispatch(ctx, messageBus, al, nil, routing.LoopTarget{Workspace: cfg.WorkspacePath()}, nil)
	err := messageBus.PublishInbound(ctx, bus.InboundMessage{Channel: "telegram", ChatID: "room", SenderID: "user", Content: "Important question", Media: []string{filepath.Join(t.TempDir(), "missing")}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case messages := <-provider.messages:
		var text strings.Builder
		for _, m := range messages {
			text.WriteString(m.Content)
		}
		if !strings.Contains(text.String(), "Important question") || !strings.Contains(text.String(), "Attachments could not be staged locally") {
			t.Fatalf("prompt: %s", text.String())
		}
	case <-ctx.Done():
		t.Fatal("message was dropped")
	}
}

func TestRunGatewayDefaultDispatch_CancelledStagingDoesNotDispatch(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	provider := &captureDispatchProvider{messages: make(chan []providers.Message, 1)}
	al := agent.NewAgentLoop(cfg, messageBus, provider)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runGatewayDefaultDispatch(ctx, messageBus, al, nil, routing.LoopTarget{Workspace: cfg.WorkspacePath()}, nil)
	}()
	if err := messageBus.PublishInbound(ctx, bus.InboundMessage{Channel: "telegram", ChatID: "room", Content: "question", Media: []string{server.URL + "/slow"}}); err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatch did not stop")
	}
	select {
	case <-provider.messages:
		t.Fatal("cancelled message dispatched")
	default:
	}
}

func TestRunGatewayDefaultDispatch_IsolatesAttachmentsAcrossChats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/first/") {
			_, _ = w.Write([]byte("first chat"))
			return
		}
		_, _ = w.Write([]byte("second chat"))
	}))
	defer server.Close()
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	runner := &captureJobRunner{received: make(chan bus.InboundMessage, 2)}
	jobManager, err := routing.NewJobManager(filepath.Join(t.TempDir(), "jobs.json"), config.JobsConfig{Enabled: true}, messageBus, nil, func(routing.LoopTarget) (routing.JobRunner, error) { return runner, nil })
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go runGatewayDefaultDispatch(ctx, messageBus, nil, jobManager, routing.LoopTarget{Workspace: workspace, Runtime: routing.RuntimeProfile{Mode: config.ModeCloud}}, nil)
	var paths []string
	for _, item := range []struct{ chat, url string }{{"chat-a", "/first/report.txt"}, {"chat-b", "/second/report.txt"}} {
		if err := messageBus.PublishInbound(ctx, bus.InboundMessage{Channel: "discord", ChatID: item.chat, SenderID: "user", Content: "review", Media: []string{server.URL + item.url}, Metadata: map[string]string{"message_id": "same-id"}}); err != nil {
			t.Fatal(err)
		}
		select {
		case msg := <-runner.received:
			if len(msg.Media) != 1 {
				t.Fatalf("media = %v", msg.Media)
			}
			paths = append(paths, msg.Media[0])
		case <-ctx.Done():
			t.Fatal("timed out waiting for job")
		}
	}
	if paths[0] == paths[1] {
		t.Fatalf("two chats share path %q", paths[0])
	}
	for i, want := range []string{"first chat", "second chat"} {
		data, err := os.ReadFile(paths[i])
		if err != nil || string(data) != want {
			t.Fatalf("path %q contents = %q, err = %v; want %q", paths[i], data, err, want)
		}
	}
}
