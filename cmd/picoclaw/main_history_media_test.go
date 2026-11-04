package main

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/sipeed/picoclaw/pkg/routing"
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
	attachChannelHistoryCallback(al, func(context.Context, string, int, string, int, string) ([]tools.ChannelHistoryMessage, error) {
		return []tools.ChannelHistoryMessage{{ID: "1", Content: "message", Media: []string{filepath.Join(t.TempDir(), "missing")}}}, nil
	}, nil)
	_, err := al.RunJob(context.Background(), bus.InboundMessage{Channel: "discord", ChatID: "room", SenderID: "user", Content: "summarize"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(provider.toolResultLog, "could not be staged") {
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
			{ID: "newest", Media: []string{server.URL + "/file-0.txt", server.URL + "/file-1.txt", server.URL + "/file-2.txt", server.URL + "/file-3.txt", server.URL + "/file-4.txt"}, MediaNames: []string{"file-0.txt", "file-1.txt", "file-2.txt", "file-3.txt", "file-4.txt"}, MediaIDs: []string{"0", "1", "2", "3", "4"}},
		}
	}
	messages, err := prepareHistoryMessages(context.Background(), workspace, "room", makeMessages(), 0, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 4 || !strings.Contains(messages[1].Content, "attachment_offset=4") || !strings.Contains(messages[1].Content, `"file-4.txt" (id: 4)`) || !strings.Contains(messages[0].Content, "attachment_offset=5") {
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

func TestHistorySnapshotPinsNewMessagesAndRejectsChanges(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("data")) }))
	defer server.Close()
	old := &discordgo.Message{ID: "100", Attachments: []*discordgo.MessageAttachment{{ID: "a", Filename: "one.txt", URL: server.URL + "/one.txt"}, {ID: "b", Filename: "two.txt", URL: server.URL + "/two.txt"}}}
	newest := &discordgo.Message{ID: "101", Attachments: []*discordgo.MessageAttachment{{ID: "c", Filename: "three.txt", URL: server.URL + "/three.txt"}, {ID: "d", Filename: "four.txt", URL: server.URL + "/four.txt"}, {ID: "e", Filename: "five.txt", URL: server.URL + "/five.txt"}}}
	added := false
	fetch := discordHistoryCallback(func(_ string, _ int, before string) ([]*discordgo.Message, error) {
		if added && before == "" {
			return []*discordgo.Message{{ID: "102"}, newest}, nil
		}
		if before != "" && before != "102" {
			t.Fatalf("before=%q, want anchor+1", before)
		}
		return []*discordgo.Message{newest, old}, nil
	})
	cb := historyStagingCallback(t.TempDir(), fetch, nil)
	first, err := cb(context.Background(), "room", 2, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	token := historyAttachmentSnapshot(first)
	if !strings.Contains(first[0].Content, "attachment_offset=4") || !strings.Contains(first[1].Content, "attachment_snapshot="+token) {
		t.Fatalf("first page %+v", first)
	}
	added = true
	second, err := cb(context.Background(), "room", 2, "", 4, token)
	if err != nil || !strings.Contains(second[0].Content, "two.txt") || !strings.Contains(second[0].Content, "staged locally") {
		t.Fatalf("second page %+v err=%v", second, err)
	}
	old.Attachments = old.Attachments[:1]
	if _, err := cb(context.Background(), "room", 2, "", 4, token); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed page err=%v", err)
	}
}

func TestHistoryIdentifiesUnstagedAndUnavailableAttachments(t *testing.T) {
	fetch := discordHistoryCallback(func(string, int, string) ([]*discordgo.Message, error) {
		return []*discordgo.Message{{ID: "101", Attachments: []*discordgo.MessageAttachment{{ID: "x", Filename: "missing.pdf"}, {ID: "y", Filename: "failed.pdf", URL: "http://127.0.0.1:1/failed.pdf"}}}}, nil
	})
	cb := historyStagingCallback(t.TempDir(), fetch, nil)
	messages, err := cb(context.Background(), "room", 1, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"missing.pdf", "id: x", "no download URL", "failed.pdf", "id: y", "could not be staged"} {
		if !strings.Contains(messages[0].Content, want) {
			t.Fatalf("missing %q in %s", want, messages[0].Content)
		}
	}
}

func TestHistoryCacheQuotaPreservesExistingFilesAndCountsConcurrentDownloads(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4")
		if r.URL.Path == "/first" {
			_, _ = w.Write([]byte("12"))
			w.(http.Flusher).Flush()
			startedOnce.Do(func() { close(started) })
			<-release
			_, _ = w.Write([]byte("34"))
			return
		}
		_, _ = w.Write([]byte("5678"))
	}))
	defer server.Close()
	workspace := t.TempDir()
	type result struct {
		path string
		err  error
	}
	first := make(chan result, 1)
	second := make(chan result, 1)
	go func() {
		path, err := stageHistoryAttachmentWithQuota(context.Background(), workspace, "room", "1", 0, "first", server.URL+"/first", nil, 5)
		first <- result{path, err}
	}()
	<-started
	ready := make(chan struct{})
	go func() {
		close(ready)
		path, err := stageHistoryAttachmentWithQuota(context.Background(), workspace, "room", "2", 0, "second", server.URL+"/second", nil, 5)
		second <- result{path, err}
	}()
	<-ready
	// A concurrent admission must wait for the in-progress download's reservation.
	select {
	case got := <-second:
		t.Fatalf("second admission finished while first was in progress: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	got := <-first
	if got.err != nil {
		t.Fatal(got.err)
	}
	if err := os.WriteFile(filepath.Join(workspace, filepath.FromSlash(got.path)), []byte("EDIT"), 0600); err != nil {
		t.Fatal(err)
	}
	failed := <-second
	if failed.err == nil {
		t.Fatal("second attachment exceeded remaining quota")
	}
	data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(got.path)))
	if err != nil || string(data) != "EDIT" {
		t.Fatalf("existing file %q: %v", data, err)
	}
	cached, err := stageHistoryAttachmentWithQuota(context.Background(), workspace, "room", "1", 0, "first", server.URL+"/first", nil, 4)
	if err != nil || cached != got.path {
		t.Fatalf("cache hit at quota = %q, %v", cached, err)
	}
	_, err = stageHistoryAttachmentWithQuota(context.Background(), workspace, "room", "3", 0, "third", server.URL+"/third", nil, 4)
	if err == nil || !strings.Contains(err.Error(), "quota exhausted") {
		t.Fatalf("admission at quota: %v", err)
	}
}

func TestHistoryCacheQuotaRejectsUnknownLengthAndKeepsExisting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("12345"))
	}))
	defer server.Close()
	workspace := t.TempDir()
	root := filepath.Join(workspace, ".sciclaw", "inbound", "discord-history", "existing")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(root, "edited.txt")
	if err := os.WriteFile(existing, []byte("edit"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := stageHistoryAttachmentWithQuota(context.Background(), workspace, "room", "1", 0, "new", server.URL+"/unknown", nil, 8)
	if err == nil {
		t.Fatal("expected streamed attachment to exceed remaining quota")
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "edit" {
		t.Fatalf("existing file %q: %v", data, err)
	}
	matches, err := filepath.Glob(filepath.Join(workspace, ".sciclaw", "inbound", "discord-history", ".stage-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files %v: %v", matches, err)
	}
}

func TestHistoryCacheLockHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	first, err := lockHistoryCache(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	second, err := lockHistoryCache(ctx, root)
	if second != nil {
		second.Close()
		t.Fatal("lock unexpectedly acquired")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock error: %v", err)
	}
}

func TestHistoryQuotaFailuresExplainHowToRecover(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("123")) }))
	defer server.Close()
	for _, tc := range []struct {
		name string
		used int64
	}{{"full", historyCacheMaxBytes}, {"insufficient", historyCacheMaxBytes - 2}} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			dir := filepath.Join(workspace, ".sciclaw", "inbound", "discord-history", "existing")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			existing := filepath.Join(dir, "edited")
			f, err := os.Create(existing)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Truncate(tc.used); err != nil {
				f.Close()
				t.Fatal(err)
			}
			f.Close()
			messages, err := prepareHistoryMessages(context.Background(), workspace, "room", []tools.ChannelHistoryMessage{{ID: "1", Media: []string{server.URL + "/file"}, MediaIDs: []string{"a"}, MediaNames: []string{"file"}}}, 0, nil, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"history cache quota", "insufficient space", "Preserve any edited files", ".sciclaw/inbound/discord-history", "attachment_offset=0"} {
				if !strings.Contains(messages[0].Content, want) {
					t.Fatalf("missing %q: %s", want, messages[0].Content)
				}
			}
			info, err := os.Stat(existing)
			if err != nil || info.Size() != tc.used {
				t.Fatalf("existing file changed: %v %v", info, err)
			}
		})
	}
}

func TestHistoryOversizedAttachmentIsNotMisreportedAsQuota(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", routing.InboundMediaMaxDownloadBytes+1))
	}))
	defer server.Close()
	messages, err := prepareHistoryMessages(context.Background(), t.TempDir(), "room", []tools.ChannelHistoryMessage{{ID: "1", Media: []string{server.URL + "/large"}}}, 0, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(messages[0].Content, "history cache quota") || !strings.Contains(messages[0].Content, "could not be staged") {
		t.Fatalf("message: %s", messages[0].Content)
	}
}

func TestHistoryRequestTimesOutWaitingForDownloadLock(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, ".sciclaw", "inbound", "discord-history")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	held, err := lockHistoryCache(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = stageHistoryAttachment(ctx, workspace, "room", "1", 0, "id", "https://example.invalid/file", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting for lock: %v", err)
	}
}
