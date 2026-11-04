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
		if !strings.HasPrefix(got.Media[0], filepath.Join(workspace, ".sciclaw", "inbound", "discord", "message-1")+string(filepath.Separator)) {
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
		relative := ".sciclaw/inbound/discord/msg-1/file.txt"
		if !strings.Contains(contents.String(), relative) {
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
