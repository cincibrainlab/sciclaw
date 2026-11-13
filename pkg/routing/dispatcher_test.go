package routing

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

func TestDispatcherSendBlockNotice_UnmappedIncludesAppAndMentionOnlyGuidance(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	d := NewDispatcher(mb, nil, nil)
	msg := bus.InboundMessage{Channel: "discord", ChatID: "1480213273453396101", SenderID: "12345"}
	d.sendBlockNotice(context.Background(), msg, Decision{Event: EventRouteUnmapped})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, ok := mb.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected outbound routing notice")
	}
	if out.Channel != msg.Channel || out.ChatID != msg.ChatID {
		t.Fatalf("unexpected outbound target: %+v", out)
	}
	for _, want := range []string{
		"This chat is not mapped to a workspace yet.",
		"Open `sciclaw app` in your terminal, go to Routing",
		"sciclaw routing add --channel discord --chat-id 1480213273453396101 --workspace /absolute/path --allow <sender_id>",
		"Unmapped behavior to `mention_only`",
		"Use `default` only if you want every unmapped room to fall back automatically.",
	} {
		if !strings.Contains(out.Content, want) {
			t.Fatalf("expected notice to contain %q, got: %s", want, out.Content)
		}
	}
}

func TestDispatcherSendBlockNotice_InternalChannelSkipsNotice(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	d := NewDispatcher(mb, nil, nil)
	d.sendBlockNotice(context.Background(), bus.InboundMessage{Channel: "system", ChatID: "discord:123"}, Decision{Event: EventRouteUnmapped})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, ok := mb.SubscribeOutbound(ctx); ok {
		t.Fatal("expected no outbound notice for internal channel")
	}
}

func TestDispatcherSendJobSubmitError_UsesBackgroundJobMessage(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	d := NewDispatcher(mb, nil, nil)
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room-1"}
	d.sendJobSubmitError(context.Background(), msg, context.DeadlineExceeded)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, ok := mb.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected outbound submit error")
	}
	if out.Content != "Failed to start background job: context deadline exceeded" {
		t.Fatalf("unexpected job submit error content: %q", out.Content)
	}
}

func TestDispatcherLogsDroppedJobSubmitError(t *testing.T) {
	logPath := filepathJoin(t.TempDir(), "server.log")
	if err := logger.EnableFileLogging(logPath); err != nil {
		t.Fatal(err)
	}
	defer logger.DisableFileLogging()
	mb := bus.NewMessageBus()
	defer mb.Close()
	for i := 0; i < 100; i++ {
		if err := mb.PublishOutbound(context.Background(), bus.OutboundMessage{}); err != nil {
			t.Fatal(err)
		}
	}
	d := NewDispatcher(mb, nil, nil)
	d.sendJobSubmitError(context.Background(), bus.InboundMessage{Channel: "discord", ChatID: "room"}, fmt.Errorf("rejected"))
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Job rejection notice dropped") || !strings.Contains(string(data), "queue full") {
		t.Fatalf("missing drop log: %s", data)
	}
}

func TestDroppedPreparationRejectionIsObservableAtHighLogLevels(t *testing.T) {
	oldLevel, oldOutput := logger.GetLevel(), log.Writer()
	file, err := os.Create(filepathJoin(t.TempDir(), "stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	log.SetOutput(file)
	defer func() { logger.SetLevel(oldLevel); log.SetOutput(oldOutput); _ = file.Close() }()
	for _, level := range []logger.LogLevel{logger.ERROR, logger.FATAL} {
		if err := file.Truncate(0); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		logger.SetLevel(level)
		mb := bus.NewMessageBus()
		for i := 0; i < 100; i++ {
			if err := mb.PublishOutbound(context.Background(), bus.OutboundMessage{}); err != nil {
				t.Fatal(err)
			}
		}
		jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 1}, mb, nil, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < cap(jm.preparation); i++ {
			jm.preparation <- struct{}{}
		}
		err = jm.Submit(context.Background(), LoopTarget{Workspace: t.TempDir()}, bus.InboundMessage{Channel: "discord", ChatID: "room", Media: []string{"https://example.invalid/file"}})
		mb.Close()
		if err == nil {
			t.Fatalf("level %v: rejection was reported as success", level)
		}
		data, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "Job rejection notice dropped") {
			t.Fatalf("level %v: missing drop log", level)
		}
	}
}

func TestAsyncCardFailureWithFullBusIsLoggedAtHighLevels(t *testing.T) {
	oldLevel, oldOutput := logger.GetLevel(), log.Writer()
	file, err := os.Create(filepathJoin(t.TempDir(), "stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	log.SetOutput(file)
	defer func() { logger.SetLevel(oldLevel); log.SetOutput(oldOutput); _ = file.Close() }()
	for _, level := range []logger.LogLevel{logger.ERROR, logger.FATAL} {
		if err := file.Truncate(0); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		logger.SetLevel(level)
		mb := bus.NewMessageBus()
		for i := 0; i < 100; i++ {
			if err := mb.PublishOutbound(context.Background(), bus.OutboundMessage{}); err != nil {
				t.Fatal(err)
			}
		}
		jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 1}, mb, &fakeProgressMessenger{editFail: true}, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
		if err != nil {
			t.Fatal(err)
		}
		job := &activeJob{record: JobRecord{ID: "job-1", Channel: "discord", ChatID: "room", State: JobStateRunning, StatusMessageID: "card"}}
		if _, ok := jm.refreshSelectedJobCard(context.Background(), job); !ok {
			t.Fatal("refresh was not scheduled")
		}
		deadline := time.Now().Add(2 * time.Second)
		for len(jm.statusSlots) != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if len(jm.statusSlots) != 0 {
			t.Fatal("refresh did not finish")
		}
		mb.Close()
		data, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "Job status failure notice dropped") {
			t.Fatalf("level %v: asynchronous failure was silent", level)
		}
	}
}

func TestDispatcherDispatchResolved_StagesInboundMediaBeforePoolDispatch(t *testing.T) {
	created := 0
	handlers := map[string]*fakeHandler{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("stub doc"))
	}))
	defer server.Close()

	pool := NewAgentLoopPoolWithFactory(func(target LoopTarget) (inboundHandler, error) {
		created++
		h := &fakeHandler{received: make(chan bus.InboundMessage, 1)}
		handlers[target.key()] = h
		return h, nil
	})
	defer pool.Close()

	mb := bus.NewMessageBus()
	defer mb.Close()
	d := NewDispatcher(mb, nil, pool)
	target := LoopTarget{Workspace: t.TempDir()}
	msg := bus.InboundMessage{
		Channel:    "discord",
		ChatID:     "chan-1",
		Content:    "review this",
		SessionKey: "discord:chan-1",
		Media:      []string{server.URL + "/attachments/report.docx"},
		Metadata:   map[string]string{"message_id": "msg-1"},
	}

	d.dispatchResolved(context.Background(), dispatchTask{
		target:   target,
		decision: Decision{Workspace: target.Workspace, Runtime: RuntimeProfile{Mode: config.ModeCloud}},
		msg:      msg,
	})

	h := handlers[target.key()]
	if h == nil {
		t.Fatal("expected handler")
	}
	select {
	case got := <-h.received:
		if len(got.Media) != 1 {
			t.Fatalf("expected staged media path, got %#v", got.Media)
		}
		if !strings.HasPrefix(got.Media[0], filepath.Join(target.Workspace, ".sciclaw", "inbound")) {
			t.Fatalf("expected staged path under workspace, got %q", got.Media[0])
		}
		if _, err := os.Stat(got.Media[0]); err != nil {
			t.Fatalf("expected staged file to exist: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for dispatched message")
	}
	if created != 1 {
		t.Fatalf("expected one handler creation, got %d", created)
	}
}

func TestDispatcherDispatchResolved_StagesInboundMediaBeforeJobSubmit(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("stub doc"))
	}))
	defer server.Close()

	progress := &fakeProgressMessenger{}
	runner := &fakeJobRunner{}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{
		Enabled:               true,
		MaxConcurrent:         1,
		ProgressUpdateSeconds: 1,
		DiscordAsyncDefault:   true,
	}, mb, progress, func(target LoopTarget) (JobRunner, error) {
		return runner, nil
	})
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}

	workspace := t.TempDir()
	d := NewDispatcher(mb, nil, nil)
	d.SetJobManager(jm)
	target := LoopTarget{Workspace: workspace, Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	msg := bus.InboundMessage{
		Channel:    "discord",
		ChatID:     "room-1",
		Content:    "please review this",
		SessionKey: "discord:room-1",
		Media:      []string{server.URL + "/attachments/report.docx"},
		Metadata:   map[string]string{"message_id": "msg-stage"},
	}

	d.dispatchResolved(context.Background(), dispatchTask{
		target:   target,
		decision: Decision{Workspace: workspace, Runtime: RuntimeProfile{Mode: config.ModeCloud}},
		msg:      msg,
	})
	waitForNoActiveJobs(t, jm, target.key())

	got := runner.snapshotLastMsg()
	if len(got.Media) != 1 {
		t.Fatalf("expected staged media path, got %#v", got.Media)
	}
	if !strings.HasPrefix(got.Media[0], filepath.Join(workspace, ".sciclaw", "inbound")) {
		t.Fatalf("expected staged media path under workspace, got %q", got.Media[0])
	}
	if _, err := os.Stat(got.Media[0]); err != nil {
		t.Fatalf("expected staged file to exist: %v", err)
	}
}

func TestDispatcherCancelDuringAttachmentStaging(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(cancelled) }))
	defer server.Close()
	mb := bus.NewMessageBus()
	defer mb.Close()
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{Enabled: true}, mb, nil, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(mb, nil, nil)
	d.SetJobManager(jm)
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "review", Media: []string{server.URL + "/slow"}}
	if err := d.enqueueDispatch(ctx, dispatchTask{target: target, msg: msg}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("staging did not start")
	}
	msg.Content, msg.Media = "cancel", nil
	msg.Metadata = map[string]string{"has_direct_mention": "true"}
	if err := d.enqueueDispatch(ctx, dispatchTask{target: target, msg: msg}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt staging")
	}
	waitForNoActiveJobs(t, jm, target.key())
}

func TestParentCancellationDuringStagingUpdatesCardOrNotifies(t *testing.T) {
	for _, mode := range []string{"edit", "edit-failure", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
			defer server.Close()
			mb := bus.NewMessageBus()
			defer mb.Close()
			progress := &fakeProgressMessenger{}
			runner := &fakeJobRunner{}
			storePath := filepathJoin(t.TempDir(), "jobs.json")
			jm, err := NewJobManager(storePath, config.JobsConfig{MaxConcurrent: 1}, mb, progress, func(LoopTarget) (JobRunner, error) { return runner, nil })
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			target := LoopTarget{Workspace: t.TempDir()}.normalized()
			msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "attachment", Media: []string{server.URL + "/file?token=SIGNED_CANARY"}}
			if err := jm.Submit(ctx, target, msg); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("staging did not start")
			}
			deadline := time.Now().Add(time.Second)
			cardID := ""
			for time.Now().Before(deadline) {
				cardID = findJobRecordByAskSummary(t, jm, "attachment").StatusMessageID
				if cardID != "" {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if cardID == "" {
				t.Fatal("initial card was not persisted")
			}
			progress.mu.Lock()
			progress.editFail = mode == "edit-failure"
			progress.mu.Unlock()
			if mode == "capacity" {
				bounded := jm.progress.(*boundedProgress)
				for i := 0; i < cap(bounded.slots); i++ {
					bounded.slots <- struct{}{}
				}
			}
			cancel()
			if mode != "edit" {
				readCtx, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				out, ok := mb.SubscribeOutbound(readCtx)
				if !ok || !strings.Contains(strings.ToLower(out.Content), "cancelled") {
					t.Fatalf("missing cancellation fallback: %#v", out)
				}
				if strings.Contains(out.Content, "SIGNED_CANARY") {
					t.Fatal("signed URL leaked in fallback")
				}
			} else {
				deadline = time.Now().Add(time.Second)
				updated := false
				for time.Now().Before(deadline) {
					progress.mu.Lock()
					for _, call := range progress.messages {
						if call.messageID == cardID && strings.Contains(strings.ToLower(call.content), "cancelled") {
							updated = true
						}
					}
					progress.mu.Unlock()
					if updated {
						break
					}
					time.Sleep(time.Millisecond)
				}
				if !updated {
					t.Fatal("cancelled state was not sent to the initial card")
				}
			}
			waitForNoActiveJobs(t, jm, target.key())
			if got := findJobRecordByAskSummary(t, jm, "attachment").State; got != JobStateCancelled {
				t.Fatalf("state = %s", got)
			}
			runner.mu.Lock()
			runs := runner.runs
			runner.mu.Unlock()
			if runs != 0 {
				t.Fatalf("cancelled job ran %d times", runs)
			}
			progress.mu.Lock()
			leaked := false
			for _, call := range progress.messages {
				if strings.Contains(call.content, "SIGNED_CANARY") {
					leaked = true
				}
			}
			progress.mu.Unlock()
			if leaked {
				t.Fatal("signed URL leaked in progress")
			}
			data, err := os.ReadFile(storePath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "SIGNED_CANARY") {
				t.Fatal("signed URL persisted")
			}
		})
	}
}

func TestDispatcherOverloadWithFullOutboundDoesNotBlockCancellation(t *testing.T) {
	for _, kind := range []string{"preparation", "status", "busy"} {
		t.Run(kind, func(t *testing.T) {
			mb := bus.NewMessageBus()
			defer mb.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
			jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 1}, mb, &fakeProgressMessenger{}, func(LoopTarget) (JobRunner, error) { return runner, nil })
			if err != nil {
				t.Fatal(err)
			}
			d := NewDispatcher(mb, nil, nil)
			d.SetJobManager(jm)
			target := LoopTarget{Workspace: t.TempDir()}.normalized()
			defer func() { cancel(); waitForNoActiveJobs(t, jm, target.key()) }()
			msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "running"}
			if err := d.enqueueDispatch(ctx, dispatchTask{target: target, msg: msg}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-runner.started:
			case <-time.After(time.Second):
				t.Fatal("job did not start")
			}
			record := findJobRecordByAskSummary(t, jm, "running")
			if err := jm.Submit(ctx, target, bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "second"}); err != nil {
				t.Fatal(err)
			}
			waitForQueuedWrite(t, jm, target.key(), 1)
			second := findJobRecordByAskSummary(t, jm, "second")
			for i := 0; i < 100; i++ {
				if err := mb.PublishOutbound(ctx, bus.OutboundMessage{Content: "full"}); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "preparation" {
				for i := 0; i < cap(jm.preparation); i++ {
					jm.preparation <- struct{}{}
				}
				msg.Media = []string{"https://example.invalid/file"}
			}
			deadline := time.Now().Add(time.Second)
			for len(jm.statusSlots) != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if len(jm.statusSlots) != 0 {
				t.Fatal("initial statuses did not finish")
			}
			for i := 0; i < cap(jm.statusSlots); i++ {
				jm.statusSlots <- struct{}{}
			}
			msg.Content = "rejected"
			if kind == "busy" {
				msg.Content = "/btw rejected"
			}
			if err := d.enqueueDispatch(ctx, dispatchTask{target: target, msg: msg}); err != nil {
				t.Fatal(err)
			}
			msg.Content, msg.Media = "cancel "+record.ShortID, nil
			msg.Metadata = map[string]string{"has_direct_mention": "true"}
			if err := d.enqueueDispatch(ctx, dispatchTask{target: target, msg: msg}); err != nil {
				t.Fatal(err)
			}
			msg.Content = "cancel " + second.ShortID
			if err := d.enqueueDispatch(ctx, dispatchTask{target: target, msg: msg}); err != nil {
				t.Fatal(err)
			}
			deadline = time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				if got := findJobRecordByAskSummary(t, jm, "second").State; got == JobStateCancelled {
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("full-bus control response blocked the next cancellation")
		})
	}
}

func TestDispatcherLaterJobRunsWhileAttachmentPreparing(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			_, _ = w.Write([]byte("file"))
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	runner := &fakeJobRunner{started: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 1}, nil, nil, func(LoopTarget) (JobRunner, error) { return runner, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	released := false
	defer func() {
		cancel()
		if !released {
			close(release)
		}
		waitForNoActiveJobs(t, jm, target.key())
	}()
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "attachment", Media: []string{server.URL + "/file"}}
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("attachment preparation did not start")
	}
	msg.Content, msg.Media = "ordinary", nil
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
		if got := runner.snapshotLastMsg().Content; got != "ordinary" {
			t.Fatalf("first job = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("ordinary job blocked behind attachment preparation")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if findJobRecordByAskSummary(t, jm, "ordinary").State == JobStateDone {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := findJobRecordByAskSummary(t, jm, "ordinary").State; got != JobStateDone {
		t.Fatalf("ordinary job did not finish: %s", got)
	}
	close(release)
	released = true
	waitForNoActiveJobs(t, jm, target.key())
	if got := findJobRecordByAskSummary(t, jm, "attachment").State; got != JobStateDone {
		t.Fatalf("attachment job did not finish: %s", got)
	}
}

type hungProgress struct {
	started chan struct{}
	release chan struct{}
}

type delayedStatus struct {
	started, release chan struct{}
	edits            chan bus.OutboundMessage
}

func (p *delayedStatus) SendOrEditProgress(_ context.Context, _, _, id string, msg bus.OutboundMessage) (string, error) {
	if id == "" {
		close(p.started)
		<-p.release
		return "card", nil
	}
	p.edits <- msg
	return id, nil
}

func TestLateStatusReflectsCancellation(t *testing.T) {
	p := &delayedStatus{started: make(chan struct{}), release: make(chan struct{}), edits: make(chan bus.OutboundMessage, 1)}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{}, nil, p, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	job := &activeJob{record: JobRecord{ID: "job-1", State: JobStateQueued}}
	done := make(chan error, 1)
	go func() { done <- jm.sendQueuedStatus(context.Background(), job, JobRecord{}, 0) }()
	<-p.started
	job.update(func(r *JobRecord) { r.State = JobStateCancelled; r.Phase = "cancelled" })
	close(p.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-p.edits:
		if !strings.Contains(strings.ToLower(msg.Content), "cancelled") {
			t.Fatalf("stale card: %s", msg.Content)
		}
	default:
		t.Fatal("late card not refreshed")
	}
}

func TestLateQueuedStatusPreservesNewerCardID(t *testing.T) {
	progress := &hungProgress{started: make(chan struct{}, 1), release: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{}, nil, progress, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	job := &activeJob{record: JobRecord{ID: "job-1", State: JobStateQueued}}
	done := make(chan error, 1)
	go func() { done <- jm.sendQueuedStatus(context.Background(), job, JobRecord{}, 0) }()
	<-progress.started
	current := job.update(func(r *JobRecord) { r.State = JobStateCancelled; r.StatusMessageID = "newer" })
	if err := jm.store.Save(current); err != nil {
		t.Fatal(err)
	}
	close(progress.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := job.snapshot(); got.StatusMessageID != "newer" || got.State != JobStateCancelled {
		t.Fatalf("late status replaced newer record: %#v", got)
	}
}

func TestHungProgressCallsStayBounded(t *testing.T) {
	p := &hungProgress{started: make(chan struct{}, 10), release: make(chan struct{})}
	defer close(p.release)
	bounded := &boundedProgress{next: p, slots: make(chan struct{}, 2)}
	for i := 0; i < 8; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := bounded.SendOrEditProgress(ctx, "discord", "room", "", bus.OutboundMessage{})
		cancel()
		if err == nil {
			t.Fatal("hung call unexpectedly succeeded")
		}
	}
	if got := len(p.started); got != 2 {
		t.Fatalf("expected at most two calls, got %d", got)
	}
}

func (p *hungProgress) SendOrEditProgress(_ context.Context, _, _, id string, _ bus.OutboundMessage) (string, error) {
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-p.release
	if id != "" {
		return id, nil
	}
	return "card", nil
}

func TestQueuedStatusDoesNotBlockSubmit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	progress := &hungProgress{started: make(chan struct{}, 10), release: make(chan struct{})}
	runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{}, nil, progress, func(LoopTarget) (JobRunner, error) { return runner, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	defer func() { cancel(); close(progress.release); waitForNoActiveJobs(t, jm, target.key()) }()
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "first"}
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	select {
	case <-progress.started:
	case <-time.After(time.Second):
		t.Fatal("progress did not start")
	}
	msg.Content = "second"
	done := make(chan error, 1)
	go func() { done <- jm.Submit(ctx, target, msg) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("queued submission blocked by progress")
	}
}

func TestWaitingPreparedBTWCancelledByParent(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			_, _ = w.Write([]byte("file"))
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	writer := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 2}, nil, nil, func(LoopTarget) (JobRunner, error) { return writer, nil })
	if err != nil {
		t.Fatal(err)
	}
	jm.SetSideLaneResolver(func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "/btw attachment", Media: []string{server.URL + "/file"}}
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	<-started
	msg.Content, msg.Media = "write", nil
	if err := jm.Submit(context.Background(), target, msg); err != nil {
		t.Fatal(err)
	}
	<-writer.started
	defer func() { close(writer.block); waitForNoActiveJobs(t, jm, target.key()) }()
	close(release)
	deadline := time.Now().Add(time.Second)
	ready := false
	for time.Now().Before(deadline) {
		jm.mu.Lock()
		set := jm.active[target.key()]
		ready = set != nil && len(set.preparing) == 1 && set.preparing[0].prepared
		jm.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !ready {
		t.Fatal("side job did not finish preparation")
	}
	cancel()
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if findJobRecordByAskSummary(t, jm, "attachment").State == JobStateCancelled {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("waiting side job did not cancel")
}

func TestPreparationSaveFailureIsReportedAndNotExecuted(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("file"))
	}))
	defer server.Close()
	mb := bus.NewMessageBus()
	defer mb.Close()
	runner := &fakeJobRunner{}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{}, mb, nil, func(LoopTarget) (JobRunner, error) { return runner, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "attachment", Media: []string{server.URL + "/file"}}
	if err := jm.Submit(context.Background(), target, msg); err != nil {
		t.Fatal(err)
	}
	<-started
	jm.store.mu.Lock()
	jm.store.path = filepath.Join(t.TempDir(), "missing", "jobs.json")
	jm.store.mu.Unlock()
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		out, ok := mb.SubscribeOutbound(ctx)
		if !ok {
			t.Fatal("persistence failure was not reported")
		}
		if strings.Contains(out.Content, "Could not save") {
			break
		}
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.runs != 0 {
		t.Fatal("job ran despite failed persistence")
	}
}

func TestPreparationCapacityIsBounded(t *testing.T) {
	started := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { started <- struct{}{}; <-r.Context().Done() }))
	defer server.Close()
	mb := bus.NewMessageBus()
	defer mb.Close()
	ctx, cancel := context.WithCancel(context.Background())
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 1}, mb, nil, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	defer func() { cancel(); waitForNoActiveJobs(t, jm, target.key()) }()
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "file", Media: []string{server.URL + "/file"}}
	for i := 0; i < 4; i++ {
		if err := jm.Submit(ctx, target, msg); err != nil {
			t.Fatal(err)
		}
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("preparation did not start")
		}
	}
	done := make(chan error, 1)
	go func() { done <- jm.Submit(ctx, target, msg) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("overload blocked")
	}
	readCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	for {
		out, ok := mb.SubscribeOutbound(readCtx)
		if !ok {
			t.Fatal("missing overload notice")
		}
		if strings.Contains(out.Content, "Too many attachments") {
			break
		}
	}
	records, err := jm.store.All()
	if err != nil || len(records) != 4 {
		t.Fatalf("records = %d, %v", len(records), err)
	}
}

func TestCancelRacingReadinessNeverExecutesQueuedAttachment(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			_, _ = w.Write([]byte("file"))
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 2}, nil, nil, func(LoopTarget) (JobRunner, error) { return runner, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "ordinary"}
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	msg.Content, msg.Media = "attachment", []string{server.URL + "/file"}
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	<-started
	record := findJobRecordByAskSummary(t, jm, "attachment")
	done := make(chan error, 1)
	go func() { _, err := jm.cancelQueuedJob(target.key(), record.ID); done <- err }()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(runner.block)
	waitForNoActiveJobs(t, jm, target.key())
	if got := findJobRecordByAskSummary(t, jm, "attachment").State; got != JobStateCancelled {
		t.Fatalf("state = %s", got)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.runs != 1 {
		t.Fatalf("cancelled attachment executed; runs = %d", runner.runs)
	}
}

type serializedRunner struct {
	started chan string
	release chan struct{}
}

func (r *serializedRunner) HandleInbound(context.Context, bus.InboundMessage) {}
func (r *serializedRunner) RunJob(ctx context.Context, msg bus.InboundMessage, _ func(string, string)) (string, error) {
	r.started <- msg.Content
	if msg.Content == "ordinary" {
		select {
		case <-r.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "done", nil
}

func TestPreparedWriteWaitsForActiveWrite(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("file"))
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &serializedRunner{started: make(chan string, 2), release: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 2}, nil, nil, func(LoopTarget) (JobRunner, error) { return runner, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "attachment", Media: []string{server.URL + "/file"}}
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	<-started
	msg.Content, msg.Media = "ordinary", nil
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	if got := <-runner.started; got != "ordinary" {
		t.Fatalf("first job = %q", got)
	}
	close(release)
	waitForQueuedWrite(t, jm, target.key(), 1)
	select {
	case got := <-runner.started:
		t.Fatalf("overlapping write: %q", got)
	default:
	}
	close(runner.release)
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("prepared write did not start")
	}
	waitForNoActiveJobs(t, jm, target.key())
}

func TestPreparedBTWWaitsForActiveWrite(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("file"))
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &serializedRunner{started: make(chan string, 2), release: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 2}, nil, nil, func(LoopTarget) (JobRunner, error) { return runner, nil })
	if err != nil {
		t.Fatal(err)
	}
	jm.SetSideLaneResolver(func(LoopTarget) (JobRunner, error) { return runner, nil })
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "/btw attachment", Media: []string{server.URL + "/file"}}
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	<-started
	msg.Content, msg.Media = "ordinary", nil
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	if got := <-runner.started; got != "ordinary" {
		t.Fatalf("first job = %q", got)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	ready := false
	for time.Now().Before(deadline) {
		jm.mu.Lock()
		set := jm.active[target.key()]
		ready = set != nil && len(set.preparing) == 1 && set.preparing[0].prepared
		jm.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !ready {
		t.Fatal("side job did not finish preparation")
	}
	select {
	case got := <-runner.started:
		t.Fatalf("side job overlapped writer: %q", got)
	default:
	}
	close(runner.release)
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("prepared side job did not start")
	}
	waitForNoActiveJobs(t, jm, target.key())
}

func TestWriteWaitsForActivePreparedBTW(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("file")) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	write := &fakeJobRunner{started: make(chan struct{})}
	side := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 2}, nil, nil, func(LoopTarget) (JobRunner, error) { return write, nil })
	if err != nil {
		t.Fatal(err)
	}
	jm.SetSideLaneResolver(func(LoopTarget) (JobRunner, error) { return side, nil })
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	released := false
	defer func() {
		if !released {
			close(side.block)
		}
		waitForNoActiveJobs(t, jm, target.key())
	}()
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "/btw attachment", Media: []string{server.URL + "/file"}}
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	select {
	case <-side.started:
	case <-time.After(time.Second):
		t.Fatal("side job did not start")
	}
	msg.Content, msg.Media = "write", nil
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	select {
	case <-write.started:
		t.Fatal("write overlapped side job")
	case <-time.After(250 * time.Millisecond):
	}
	close(side.block)
	released = true
	select {
	case <-write.started:
	case <-time.After(time.Second):
		t.Fatal("write did not start after side job finished")
	}
}

func TestPreparedWriteWaitsForActiveBTW(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("file")) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	write := &fakeJobRunner{started: make(chan struct{})}
	side := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 2}, nil, nil, func(LoopTarget) (JobRunner, error) { return write, nil })
	if err != nil {
		t.Fatal(err)
	}
	jm.SetSideLaneResolver(func(LoopTarget) (JobRunner, error) { return side, nil })
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	released := false
	defer func() {
		if !released {
			close(side.block)
		}
		waitForNoActiveJobs(t, jm, target.key())
	}()
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "/btw side"}
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	<-side.started
	msg.Content, msg.Media = "attachment", []string{server.URL + "/file"}
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	waitForQueuedWrite(t, jm, target.key(), 1)
	select {
	case <-write.started:
		t.Fatal("prepared write overlapped side job")
	default:
	}
	close(side.block)
	released = true
	select {
	case <-write.started:
	case <-time.After(time.Second):
		t.Fatal("prepared write did not start after side job")
	}
}

func TestPreparedQueuedWriteCancelsBeforeActiveWriterFinishes(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("file"))
	}))
	defer server.Close()
	writer := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 2}, nil, nil, func(LoopTarget) (JobRunner, error) { return writer, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() { close(writer.block); waitForNoActiveJobs(t, jm, target.key()) }()
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "writer"}
	if err := jm.Submit(context.Background(), target, msg); err != nil {
		t.Fatal(err)
	}
	<-writer.started
	msg.Content, msg.Media = "attachment", []string{server.URL + "/file"}
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	<-started
	close(release)
	waitForQueuedWrite(t, jm, target.key(), 1)
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if findJobRecordByAskSummary(t, jm, "attachment").State == JobStateCancelled {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("prepared queued write was not cancelled while writer remained active")
}

func TestDispatcherAttachmentCaptionIsNotJobControl(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("file")) }))
	defer server.Close()
	mb := bus.NewMessageBus()
	defer mb.Close()
	runner := &fakeJobRunner{}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{Enabled: true}, mb, nil, func(LoopTarget) (JobRunner, error) { return runner, nil }, func() map[string]string { return map[string]string{"https://public.invalid": server.URL} })
	if err != nil {
		t.Fatal(err)
	}
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	d := NewDispatcher(mb, nil, nil)
	d.SetJobManager(jm)
	d.dispatchResolved(context.Background(), dispatchTask{target: target, msg: bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "cancel", Media: []string{"https://public.invalid/file"}, Metadata: map[string]string{"has_direct_mention": "true"}}})
	waitForNoActiveJobs(t, jm, target.key())
	got := runner.snapshotLastMsg()
	if len(got.Media) != 1 || !strings.Contains(got.Content, "cancel") {
		t.Fatalf("attachment caption was not processed as a job: %+v", got)
	}
}

func TestDispatcherDispatchResolved_PreservesTextWhenStagingFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow.ogg" {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", inboundMediaMaxDownloadBytes+1))
		_, _ = w.Write([]byte("large"))
	}))
	defer server.Close()
	oldClient := inboundMediaHTTPClient
	inboundMediaHTTPClient = &http.Client{Timeout: 100 * time.Millisecond}
	defer func() { inboundMediaHTTPClient = oldClient }()
	for _, name := range []string{"large.ogg", "slow.ogg"} {
		t.Run(name, func(t *testing.T) {
			received := make(chan bus.InboundMessage, 1)
			pool := NewAgentLoopPoolWithFactory(func(LoopTarget) (inboundHandler, error) { return &fakeHandler{received: received}, nil })
			defer pool.Close()
			mb := bus.NewMessageBus()
			defer mb.Close()
			d := NewDispatcher(mb, nil, pool)
			target := LoopTarget{Workspace: t.TempDir()}
			msg := bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "question\n[audio transcription: hello]", Media: []string{server.URL + "/" + name}}
			d.dispatchResolved(context.Background(), dispatchTask{target: target, msg: msg})
			select {
			case got := <-received:
				if !strings.Contains(got.Content, msg.Content) || !strings.Contains(got.Content, "Attachments could not be staged locally.") || len(got.Media) != 0 {
					t.Fatalf("dispatched message = %+v", got)
				}
			case <-time.After(time.Second):
				t.Fatal("text was not dispatched")
			}
		})
	}
}

func TestDispatcherDispatchResolved_CancelledContextDoesNotDispatch(t *testing.T) {
	received := make(chan bus.InboundMessage, 1)
	pool := NewAgentLoopPoolWithFactory(func(LoopTarget) (inboundHandler, error) { return &fakeHandler{received: received}, nil })
	defer pool.Close()
	mb := bus.NewMessageBus()
	defer mb.Close()
	d := NewDispatcher(mb, nil, pool)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	msg := bus.InboundMessage{Channel: "discord", Content: "[audio transcription: hello]", Media: []string{"http://example.invalid/voice.ogg"}}
	d.dispatchResolved(ctx, dispatchTask{target: LoopTarget{Workspace: t.TempDir()}, msg: msg})
	select {
	case got := <-received:
		t.Fatalf("cancelled message dispatched: %+v", got)
	default:
	}
	if msg.Content != "[audio transcription: hello]" {
		t.Fatalf("original content changed: %q", msg.Content)
	}
}
