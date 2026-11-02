package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJSONLAuditSinkCloseDrains(t *testing.T) {
	ws := t.TempDir()
	sink, err := NewJSONLAuditSink(ws)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := sink.Write(AuditEntry{TurnID: "drain"}); err != nil {
			t.Fatal(err)
		}
	}
	sink.Close()
	data, err := os.ReadFile(sink.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), `"turn_id":"drain"`); got != 100 {
		t.Fatalf("got %d entries, want 100", got)
	}
}

func TestJSONLAuditSinkCloseWaitsForInFlightWrite(t *testing.T) {
	sink := &JSONLAuditSink{
		path:  filepath.Join(t.TempDir(), "audit.jsonl"),
		queue: make(chan []byte, auditQueueSize),
		done:  make(chan struct{}),
	}
	started := make(chan struct{})
	release := make(chan struct{})
	first := true
	go sink.writeLoop(func(line []byte) error {
		if first {
			first = false
			close(started)
			<-release
		}
		return sink.appendLine(line)
	})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	if err := sink.Write(AuditEntry{TurnID: "in-flight"}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := sink.Write(AuditEntry{TurnID: "queued"}); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { sink.Close(); close(closed) }()
	deadline := time.After(2 * time.Second)
	for {
		sink.mu.Lock()
		closing := sink.closed
		sink.mu.Unlock()
		if closing {
			break
		}
		select {
		case <-deadline:
			t.Fatal("close did not start")
		default:
			runtime.Gosched()
		}
	}
	select {
	case <-closed:
		t.Fatal("close returned while write was blocked")
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("close did not return after releasing write")
	}
	data, err := os.ReadFile(sink.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"in-flight", "queued"} {
		if !strings.Contains(string(data), `"turn_id":"`+id+`"`) {
			t.Errorf("missing %s entry after close: %s", id, data)
		}
	}
}

func TestJSONLAuditSinkConcurrentClose(t *testing.T) {
	sink, err := NewJSONLAuditSink(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const writers, writesPerWriter = 10, 10 // Total writes stay below auditQueueSize.
	accepted := make(chan string, writers*writesPerWriter)
	start := make(chan struct{})
	var ready, wg sync.WaitGroup
	ready.Add(writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < writesPerWriter; j++ {
				if j == 1 {
					ready.Done()
					<-start
				}
				id := fmt.Sprintf("%d-%d", i, j)
				if err := sink.Write(AuditEntry{TurnID: id}); err == nil {
					accepted <- id
				} else if j == 0 {
					t.Errorf("initial write: %v", err)
				}
			}
		}(i)
	}
	ready.Wait()
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; sink.Close() }()
	}
	close(start)
	wg.Wait()
	close(accepted)
	if err := sink.Write(AuditEntry{}); err == nil {
		t.Fatal("write after close succeeded")
	}
	want := make(map[string]bool)
	for id := range accepted {
		want[id] = true
	}
	data, err := os.ReadFile(sink.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode audit entry: %v", err)
		}
		if !want[entry.TurnID] {
			t.Fatalf("unexpected or duplicate audit entry %q", entry.TurnID)
		}
		delete(want, entry.TurnID)
	}
	if len(want) != 0 {
		t.Fatalf("accepted entries missing after close: %v", want)
	}
}

func TestJSONLAuditSinkWrite(t *testing.T) {
	ws := t.TempDir()
	sink, err := NewJSONLAuditSink(ws)
	if err != nil {
		t.Fatalf("NewJSONLAuditSink: %v", err)
	}
	defer sink.Close()
	entry := AuditEntry{TurnID: "turn-1", Event: EventBeforeTurn, Handler: "h", Status: StatusOK, Timestamp: time.Now()}
	if err := sink.Write(entry); err != nil {
		t.Fatalf("Write: %v", err)
	}

	auditPath := filepath.Join(ws, "hooks", "hook-events.jsonl")
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(auditPath)
		if err == nil && strings.Contains(string(data), "\"turn_id\":\"turn-1\"") {
			return
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("read audit file after wait: %v", err)
			}
			t.Fatalf("audit content missing turn_id after wait: %s", string(data))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
