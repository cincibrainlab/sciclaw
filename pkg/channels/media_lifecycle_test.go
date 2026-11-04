package channels

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestHandleMessageTransfersTemporaryFilesUntilConsumerCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	channel := NewBaseChannel("telegram", nil, messageBus, nil)
	file := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(file, []byte("photo"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	channel.handleMessage(ctx, "user", "chat", "photo", []string{file}, nil, func() { calls.Add(1); _ = os.Remove(file) })
	msg, ok := messageBus.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("no message")
	}
	if data, err := os.ReadFile(msg.Media[0]); err != nil || string(data) != "photo" {
		t.Fatalf("queued file %q: %v", data, err)
	}
	msg.Cleanup()
	msg.Cleanup()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("file not removed: %v", err)
	}
	cancel()
	if calls.Load() != 1 {
		t.Fatalf("cleanup called %d times", calls.Load())
	}
}

func TestHandleMessageCleansTemporaryFilesOnCancellationAndRejectedPublish(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "closed"}[closed], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			messageBus := bus.NewMessageBus()
			defer messageBus.Close()
			if closed {
				messageBus.Close()
			}
			channel := NewBaseChannel("telegram", nil, messageBus, nil)
			done := make(chan struct{})
			channel.handleMessage(ctx, "user", "chat", "photo", nil, nil, func() { close(done) })
			if !closed {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cleanup not called")
			}
		})
	}
}
