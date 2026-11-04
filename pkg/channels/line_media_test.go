package channels

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

type lineTestTransport func(*http.Request) (*http.Response, error)

func (f lineTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLINEProcessEventPreservesMediaUntilConsumerCleanup(t *testing.T) {
	old := http.DefaultTransport
	http.DefaultTransport = lineTestTransport(func(r *http.Request) (*http.Response, error) {
		body := "ok"
		if strings.Contains(r.URL.Path, "/content") {
			body = "line image"
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	defer func() { http.DefaultTransport = old }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mb := bus.NewMessageBus()
	defer mb.Close()
	channel, err := NewLINEChannel(config.LINEConfig{ChannelSecret: "secret", ChannelAccessToken: "token"}, mb)
	if err != nil {
		t.Fatal(err)
	}
	channel.ctx = ctx
	msg, err := json.Marshal(lineMessage{ID: "message-1", Type: "image"})
	if err != nil {
		t.Fatal(err)
	}
	channel.processEvent(lineEvent{Type: "message", Source: lineSource{Type: "user", UserID: "user"}, Message: msg})
	inbound, ok := mb.ConsumeInbound(ctx)
	if !ok || len(inbound.Media) != 1 || inbound.Cleanup == nil {
		t.Fatalf("message=%+v ok=%t", inbound, ok)
	}
	data, err := os.ReadFile(inbound.Media[0])
	if err != nil || string(data) != "line image" {
		t.Fatalf("queued file %q: %v", data, err)
	}
	inbound.Cleanup()
	if _, err := os.Stat(inbound.Media[0]); !os.IsNotExist(err) {
		t.Fatalf("temporary file remains: %v", err)
	}
}
