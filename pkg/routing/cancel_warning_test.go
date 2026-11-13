package routing

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestAutomaticCancellationSaveFailureWarnsWithoutBlocking(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(map[bool]string{false: "available", true: "full"}[full], func(t *testing.T) {
			mb := bus.NewMessageBus()
			defer mb.Close()
			store, err := newJobStore(filepath.Join(t.TempDir(), "jobs.json"))
			if err != nil {
				t.Fatal(err)
			}
			record := JobRecord{ID: "job-1", ShortID: "00001", Channel: "discord", ChatID: "room", State: JobStateQueued}
			if err := store.Save(record); err != nil {
				t.Fatal(err)
			}
			store.path = filepath.Join(t.TempDir(), "SECRET_CANARY", "jobs.json")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			jm := &JobManager{bus: mb, store: store, active: map[string]*targetActiveJobs{"target": {queuedWrites: []*activeJob{{record: record, ctx: ctx, cancel: cancel}}}}}
			if full {
				for i := 0; i < 100; i++ {
					if err := mb.TryPublishOutbound(context.Background(), bus.OutboundMessage{Content: "fill"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			done := make(chan struct{})
			go func() { jm.autoCancelQueuedJob("target", record.ID); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("automatic cancellation blocked on notice")
			}
			records, err := store.All()
			if err != nil || len(records) != 1 || records[0].State != JobStateQueued {
				t.Fatalf("failed save reported as durable: %v, %v", records, err)
			}
			if full {
				return
			}
			readCtx, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer stop()
			msg, ok := mb.SubscribeOutbound(readCtx)
			if !ok || msg.Channel != "discord" || msg.ChatID != "room" || !strings.Contains(msg.Content, "could not be saved") || !strings.Contains(msg.Content, "may run after restart") || strings.Contains(msg.Content, "SECRET_CANARY") {
				t.Fatalf("missing or unsafe cancellation warning: %#v", msg)
			}
		})
	}
}
