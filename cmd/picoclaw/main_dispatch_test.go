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

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
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
