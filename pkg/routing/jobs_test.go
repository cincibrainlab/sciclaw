package routing

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

type fakeProgressMessenger struct {
	mu                sync.Mutex
	calls             []string
	messages          []fakeProgressCall
	nextID            string
	editReplacementID string
	idSeq             int
	sendFail          bool
	editFail          bool
}

type fakeProgressCall struct {
	channel   string
	chatID    string
	messageID string
	content   string
	title     string
	embed     bus.OutboundEmbed
}

func (m *fakeProgressMessenger) SendOrEditProgress(_ context.Context, channelName, chatID, messageID string, msg bus.OutboundMessage) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	title := ""
	if len(msg.Embeds) > 0 {
		title = msg.Embeds[0].Title
	}
	m.calls = append(m.calls, fmt.Sprintf("%s|%s|%s|%s", channelName, chatID, msg.Content, title))
	m.messages = append(m.messages, fakeProgressCall{
		channel:   channelName,
		chatID:    chatID,
		messageID: messageID,
		content:   msg.Content,
		title:     title,
		embed: func() bus.OutboundEmbed {
			if len(msg.Embeds) > 0 {
				return msg.Embeds[0]
			}
			return bus.OutboundEmbed{}
		}(),
	})
	if messageID == "" && m.sendFail {
		return "", fmt.Errorf("send failed")
	}
	if messageID != "" && m.editFail {
		return "", fmt.Errorf("edit failed")
	}
	if messageID != "" && m.editReplacementID != "" {
		id := m.editReplacementID
		m.editReplacementID = ""
		return id, nil
	}
	if messageID != "" {
		return messageID, nil
	}
	if m.nextID == "" {
		m.idSeq++
		m.nextID = fmt.Sprintf("progress-%d", m.idSeq)
	}
	id := m.nextID
	m.nextID = ""
	return id, nil
}

type fakeJobRunner struct {
	started chan struct{}
	block   chan struct{}
	err     error
	runs    int
	mu      sync.Mutex
	lastMsg bus.InboundMessage
}

func TestJobStoreIgnoresStaleActiveSnapshots(t *testing.T) {
	store, err := newJobStore(filepathJoin(t.TempDir(), "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	job := &activeJob{record: JobRecord{ID: "job-1", State: JobStateQueued}}
	old := job.snapshot()
	current := job.update(func(r *JobRecord) { r.State = JobStateCancelled })
	if err := store.Save(current); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(old); err != nil {
		t.Fatal(err)
	}
	records, err := store.All()
	if err != nil || len(records) != 1 || records[0].State != JobStateCancelled {
		t.Fatalf("stale save replaced cancellation: %#v, %v", records, err)
	}
}

func TestJobStoreFailedSaveDoesNotReportSuccessInMemory(t *testing.T) {
	store, err := newJobStore(filepathJoin(t.TempDir(), "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(JobRecord{ID: "job-1", State: JobStateQueued}); err != nil {
		t.Fatal(err)
	}
	store.path = filepathJoin(t.TempDir(), "missing", "jobs.json")
	if err := store.Save(JobRecord{ID: "job-1", State: JobStateDone}); err == nil {
		t.Fatal("expected save error")
	}
	records, err := store.All()
	if err != nil || len(records) != 1 || records[0].State != JobStateQueued {
		t.Fatalf("failed save visible as success: %#v %v", records, err)
	}
}

func TestAutomaticCancellationRetriesFailedPersistence(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	path := filepathJoin(t.TempDir(), "jobs.json")
	runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	jm, err := NewJobManager(path, config.JobsConfig{}, mb, nil, func(LoopTarget) (JobRunner, error) { return runner, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	defer func() { close(runner.block); waitForNoActiveJobs(t, jm, target.key()) }()
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "writer"}); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := jm.Submit(ctx, target, bus.InboundMessage{Channel: "discord", ChatID: "room", SessionKey: "discord:room", Content: "queued"}); err != nil {
		t.Fatal(err)
	}
	waitForQueuedWrite(t, jm, target.key(), 1)
	jm.store.mu.Lock()
	jm.store.path = filepathJoin(t.TempDir(), "missing", "jobs.json")
	jm.store.mu.Unlock()
	cancel()
	readCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	for {
		out, ok := mb.SubscribeOutbound(readCtx)
		if !ok {
			t.Fatal("missing cancellation persistence warning")
		}
		if strings.Contains(out.Content, "may run after restart") {
			break
		}
	}
	jm.store.mu.Lock()
	jm.store.path = path
	jm.store.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), `"state": "cancelled"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("cancellation was not durably retried")
}

func TestAutomaticCancellationReportsUnrecoverablePersistenceFailure(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	path := filepathJoin(t.TempDir(), "jobs.json")
	jm, err := NewJobManager(path, config.JobsConfig{}, mb, nil, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	record := JobRecord{ID: "job-1", Channel: "discord", ChatID: "room", State: JobStateQueued, Class: JobClassWrite}
	if err := jm.store.Save(record); err != nil {
		t.Fatal(err)
	}
	job := &activeJob{record: record, ctx: ctx, cancel: cancel}
	jm.active["target"] = &targetActiveJobs{queuedWrites: []*activeJob{job}}
	jm.store.mu.Lock()
	jm.store.path = filepathJoin(t.TempDir(), "missing", "jobs.json")
	jm.store.mu.Unlock()
	done := make(chan struct{})
	go func() { jm.autoCancelQueuedJob("target", record.ID); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation retries did not finish")
	}
	readCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	initial, ok := mb.SubscribeOutbound(readCtx)
	if !ok || !strings.Contains(initial.Content, "may run after restart") {
		t.Fatalf("missing initial warning: %#v", initial)
	}
	final, ok := mb.SubscribeOutbound(readCtx)
	if !ok || !strings.Contains(final.Content, "may run after restart") || !strings.Contains(final.Content, "administrator") {
		t.Fatalf("missing final warning: %#v", final)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"state": "queued"`) {
		t.Fatalf("unexpected persisted state: %s", data)
	}
}

func TestAutomaticCancellationReportsRetryCapacityExhaustion(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 1}, mb, nil, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	record := JobRecord{ID: "job-1", Channel: "discord", ChatID: "room", State: JobStateQueued}
	if err := jm.store.Save(record); err != nil {
		t.Fatal(err)
	}
	jm.active["target"] = &targetActiveJobs{queuedWrites: []*activeJob{{record: record, ctx: ctx, cancel: cancel}}}
	jm.store.mu.Lock()
	jm.store.path = filepathJoin(t.TempDir(), "missing", "jobs.json")
	jm.store.mu.Unlock()
	for i := 0; i < cap(jm.cancelRetries); i++ {
		jm.cancelRetries <- struct{}{}
	}
	done := make(chan struct{})
	go func() { jm.autoCancelQueuedJob("target", record.ID); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retry overload blocked")
	}
	readCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, ok := mb.SubscribeOutbound(readCtx); !ok {
		t.Fatal("missing initial warning")
	}
	final, ok := mb.SubscribeOutbound(readCtx)
	if !ok || !strings.Contains(final.Content, "may run after restart") || !strings.Contains(final.Content, "administrator") {
		t.Fatalf("missing overload warning: %#v", final)
	}
}

func TestSubmitPersistenceFailureDoesNotRun(t *testing.T) {
	runner := &fakeJobRunner{}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{}, nil, nil, func(LoopTarget) (JobRunner, error) { return runner, nil })
	if err != nil {
		t.Fatal(err)
	}
	jm.store.mu.Lock()
	jm.store.path = filepathJoin(t.TempDir(), "missing", "jobs.json")
	jm.store.mu.Unlock()
	err = jm.Submit(context.Background(), LoopTarget{Workspace: t.TempDir()}, bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "work"})
	if err == nil {
		t.Fatal("expected persistence error")
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.runs != 0 {
		t.Fatal("job executed despite persistence error")
	}
}

func TestCompletionSaveFailureDoesNotReportDone(t *testing.T) {
	progress := &fakeProgressMessenger{}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{}, nil, progress, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	jm.store.mu.Lock()
	jm.store.path = filepathJoin(t.TempDir(), "missing", "jobs.json")
	jm.store.mu.Unlock()
	job := &activeJob{record: JobRecord{ID: "job-1", State: JobStateRunning, Phase: "thinking"}}
	reporter := jm.newProgressReporter(context.Background(), job)
	defer reporter.finish()
	reporter.complete(JobStateDone, "done", "Done. Reply posted below.", nil)
	if got := job.snapshot(); got.State == JobStateDone {
		t.Fatalf("reported undurable completion: %#v", got)
	}
	if progressHas(progress, "Done. Reply below.") {
		t.Fatal("sent success despite persistence failure")
	}
}

func TestPreparationOverloadWithoutBusReturnsError(t *testing.T) {
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 1}, nil, nil, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(jm.preparation); i++ {
		jm.preparation <- struct{}{}
	}
	err = jm.Submit(context.Background(), LoopTarget{Workspace: t.TempDir()}, bus.InboundMessage{Channel: "discord", ChatID: "room", Media: []string{"https://example.invalid/file"}})
	if err == nil {
		t.Fatal("overload was silently discarded")
	}
}

func TestInitialStatusCapacityRejectsWithoutPersisting(t *testing.T) {
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 1}, nil, nil, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(jm.statusSlots); i++ {
		jm.statusSlots <- struct{}{}
	}
	err = jm.Submit(context.Background(), LoopTarget{Workspace: t.TempDir()}, bus.InboundMessage{Channel: "discord", ChatID: "room", Media: []string{"https://example.invalid/file"}})
	if err == nil {
		t.Fatal("status capacity exhaustion accepted an unobservable job")
	}
	records, err := jm.store.All()
	if err != nil || len(records) != 0 {
		t.Fatalf("rejected job persisted: %#v %v", records, err)
	}
}

func TestImmediateJobStatusCapacityRejectsWithoutExecution(t *testing.T) {
	runner := &fakeJobRunner{}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 1}, nil, nil, func(LoopTarget) (JobRunner, error) { return runner, nil })
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(jm.statusSlots); i++ {
		jm.statusSlots <- struct{}{}
	}
	err = jm.Submit(context.Background(), LoopTarget{Workspace: t.TempDir()}, bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "work"})
	if err == nil {
		t.Fatal("status capacity exhaustion accepted immediate job")
	}
	records, err := jm.store.All()
	if err != nil || len(records) != 0 {
		t.Fatalf("rejected job persisted: %#v %v", records, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.runs != 0 {
		t.Fatal("rejected job executed")
	}
}

func TestHungMessengerCapacityRejectsBeforeAcceptingJob(t *testing.T) {
	progress := &hungProgress{started: make(chan struct{}, 10), release: make(chan struct{})}
	defer close(progress.release)
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 1}, nil, progress, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	bounded := jm.progress.(*boundedProgress)
	for i := 0; i < cap(bounded.slots); i++ {
		bounded.slots <- struct{}{}
	}
	err = jm.Submit(context.Background(), LoopTarget{Workspace: t.TempDir()}, bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "work"})
	if err == nil {
		t.Fatal("accepted job with no status capacity or fallback")
	}
	records, err := jm.store.All()
	if err != nil || len(records) != 0 {
		t.Fatalf("rejected job persisted: %#v %v", records, err)
	}
}

func TestQueuedWriteStatusCapacityRejectsWithoutDisturbingWriter(t *testing.T) {
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 1}, nil, nil, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	writer := &activeJob{record: JobRecord{ID: "writer", State: JobStateRunning}}
	jm.active[target.key()] = &targetActiveJobs{write: writer}
	for i := 0; i < cap(jm.statusSlots); i++ {
		jm.statusSlots <- struct{}{}
	}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "new"}); err == nil {
		t.Fatal("expected rejection")
	}
	if got := jm.active[target.key()]; got.write != writer || len(got.queuedWrites) != 0 {
		t.Fatalf("existing writer disturbed: %#v", got)
	}
	records, err := jm.store.All()
	if err != nil || len(records) != 0 {
		t.Fatalf("rejected job persisted: %#v %v", records, err)
	}
}

func TestStatusOverloadDoesNotStrandRecoveredBacklog(t *testing.T) {
	runner := &fakeJobRunner{started: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{MaxConcurrent: 1}, nil, nil, func(LoopTarget) (JobRunner, error) { return runner, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := &activeJob{record: JobRecord{ID: "job-1", State: JobStateQueued, Class: JobClassWrite}, ctx: ctx, cancel: cancel, target: target}
	jm.active[target.key()] = &targetActiveJobs{queuedWrites: []*activeJob{job}}
	for i := 0; i < cap(jm.statusSlots); i++ {
		jm.statusSlots <- struct{}{}
	}
	err = jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room", Content: "new"})
	if err == nil {
		t.Fatal("expected status overload rejection")
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("existing backlog was stranded")
	}
	waitForNoActiveJobs(t, jm, target.key())
}

func TestForcePreparingJobExplainsPreparation(t *testing.T) {
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{}, nil, nil, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	jm.active["target"] = &targetActiveJobs{preparing: []*activeJob{{record: JobRecord{ID: "job-1", State: JobStateQueued}}}}
	_, err = jm.forceQueuedJob("target", "job-1")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "prepar") {
		t.Fatalf("expected preparation explanation, got %v", err)
	}
}

func TestCancelDoesNotBlockOnHungCard(t *testing.T) {
	progress := &hungProgress{started: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(progress.release)
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{}, nil, progress, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := &activeJob{record: JobRecord{ID: "job-1", State: JobStateQueued, StatusMessageID: "card"}, ctx: ctx, cancel: cancel}
	jm.active["target"] = &targetActiveJobs{queuedWrites: []*activeJob{job}}
	done := make(chan error, 1)
	go func() { _, err := jm.cancelQueuedJob("target", job.snapshot().ID); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("cancel blocked on progress card")
	}
}

func TestForceDoesNotBlockOnHungCard(t *testing.T) {
	progress := &hungProgress{started: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(progress.release)
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{}, nil, progress, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	job := &activeJob{record: JobRecord{ID: "job-1", State: JobStateQueued, StatusMessageID: "card"}}
	jm.active["target"] = &targetActiveJobs{write: &activeJob{record: JobRecord{ID: "write", State: JobStateRunning}}, queuedWrites: []*activeJob{job}}
	done := make(chan error, 1)
	go func() { _, err := jm.forceQueuedJob("target", "job-1"); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("force blocked on progress card")
	}
}

func TestStatusDoesNotBlockOnHungCard(t *testing.T) {
	progress := &hungProgress{started: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(progress.release)
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{}, nil, progress, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	job := &activeJob{record: JobRecord{ID: "job-1", State: JobStateRunning, StatusMessageID: "card"}}
	done := make(chan error, 1)
	go func() {
		done <- jm.handleControl(context.Background(), bus.InboundMessage{}, "target", []*activeJob{job}, parsedJobControl{Kind: jobControlStatus})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("status blocked on progress card")
	}
}

func TestCancelControlDoesNotRewriteFinishedJob(t *testing.T) {
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{}, nil, nil, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := &activeJob{record: JobRecord{ID: "job-1", State: JobStateDone, Phase: "done"}, ctx: ctx, cancel: cancel}
	if err := jm.handleControl(ctx, bus.InboundMessage{}, "target", []*activeJob{job}, parsedJobControl{Kind: jobControlCancel}); err != nil {
		t.Fatal(err)
	}
	if got := job.snapshot(); got.Phase != "done" || ctx.Err() != nil {
		t.Fatalf("finished job changed: %#v", got)
	}
}

func TestQueuedAttachmentDoesNotPersistURLAndRequiresResendAfterRestart(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	path := filepathJoin(t.TempDir(), "jobs.json")
	resolver := func(LoopTarget) (JobRunner, error) { return runner, nil }
	progress := &fakeProgressMessenger{}
	jm, err := NewJobManager(path, config.JobsConfig{Enabled: true}, mb, progress, resolver)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	target := LoopTarget{Workspace: t.TempDir()}.normalized()
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room", SessionKey: "discord:room", Content: "first"}
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	msg.Content = "review attachment"
	msg.Media = []string{"https://example.invalid/file?token=SIGNED_SECRET"}
	if err := jm.Submit(ctx, target, msg); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if findJobRecordByAskSummary(t, jm, "review attachment").StatusMessageID != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SIGNED_SECRET") {
		t.Error("signed attachment URL was persisted")
	}
	restartPath := filepathJoin(t.TempDir(), "jobs.json")
	if err := os.WriteFile(restartPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewJobManager(restartPath, config.JobsConfig{Enabled: true}, mb, progress, resolver)
	if err != nil {
		t.Fatal(err)
	}
	progress.mu.Lock()
	for _, call := range progress.messages {
		if embedHas(call.embed, "resend") {
			t.Error("restart notification sent before channels were ready")
		}
	}
	progress.mu.Unlock()
	restarted.NotifyInterruptedAttachments(context.Background())
	records, err := restarted.store.All()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.AskSummary == "review attachment" {
			if record.State != JobStateInterrupted || !strings.Contains(strings.ToLower(record.Detail), "resend") {
				t.Errorf("queued attachment restart: state=%s detail=%q", record.State, record.Detail)
			}
			progress.mu.Lock()
			reported := false
			for _, call := range progress.messages {
				if embedHas(call.embed, "resend") {
					reported = true
				}
			}
			progress.mu.Unlock()
			if !reported {
				t.Error("restart did not update a progress card with resend guidance")
			}
			cancel()
			waitForNoActiveJobs(t, jm, target.key())
			return
		}
	}
	t.Fatal("attachment job missing")
}

func TestRestartAttachmentNoticeFallsBackWhenCardUnavailable(t *testing.T) {
	for _, test := range []struct {
		name, card string
		editFail   bool
	}{{"missing card", "", false}, {"edit failure", "old-card", true}} {
		t.Run(test.name, func(t *testing.T) {
			mb := bus.NewMessageBus()
			defer mb.Close()
			path := filepathJoin(t.TempDir(), "jobs.json")
			store, err := newJobStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save(JobRecord{ID: "job-1-1", Channel: "discord", ChatID: "room", State: JobStateQueued, AttachmentsPending: true, StatusMessageID: test.card}); err != nil {
				t.Fatal(err)
			}
			progress := &fakeProgressMessenger{editFail: test.editFail}
			jm, err := NewJobManager(path, config.JobsConfig{Enabled: true}, mb, progress, func(LoopTarget) (JobRunner, error) { return &fakeJobRunner{}, nil })
			if err != nil {
				t.Fatal(err)
			}
			jm.NotifyInterruptedAttachments(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			msg, ok := mb.SubscribeOutbound(ctx)
			if !ok || msg.ChatID != "room" || !strings.Contains(strings.ToLower(msg.Content), "resend") {
				t.Fatalf("missing fallback notice: %+v ok=%v", msg, ok)
			}
		})
	}
}

func (r *fakeJobRunner) HandleInbound(context.Context, bus.InboundMessage) {}

func (r *fakeJobRunner) RunJob(ctx context.Context, msg bus.InboundMessage, onProgress func(phase, detail string)) (string, error) {
	r.mu.Lock()
	r.runs++
	r.lastMsg = msg
	started := r.started
	block := r.block
	r.mu.Unlock()
	if started != nil {
		select {
		case <-started:
		default:
			close(started)
		}
	}
	if onProgress != nil {
		onProgress("thinking", "Thinking")
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "done", r.err
}

type safeJobError struct {
	raw     string
	message string
}

func (e safeJobError) Error() string       { return e.raw }
func (e safeJobError) UserMessage() string { return e.message }

func (r *fakeJobRunner) snapshotLastMsg() bus.InboundMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastMsg
}

func TestJobManagerBusyStatusAndCancel(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
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

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	msg := bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "please do work"}
	if err := jm.Submit(context.Background(), target, msg); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for job start")
	}

	if err := jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "another request"}); err != nil {
		t.Fatalf("second submit: %v", err)
	}
	waitForQueuedWrite(t, jm, target.key(), 1)
	waitForQueuedCards(t, jm, target.key(), 1)
	jm.mu.Lock()
	queuedID := jm.active[target.key()].queuedWrites[0].snapshot().ShortID
	jm.mu.Unlock()
	if len(queuedID) != 5 {
		t.Fatalf("expected stable 5-char queued ref, got %q", queuedID)
	}
	if !progressHas(progress, queuedID) {
		t.Fatalf("expected queued progress card for %s, calls=%v", queuedID, progress.calls)
	}
	if !progressHas(progress, "Reply `status` · `force` to move next · `cancel` to drop from queue") {
		t.Fatalf("expected queued progress card to include force/cancel hint for %s, calls=%v", queuedID, progress.calls)
	}

	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel:  "discord",
		ChatID:   "room-1",
		Content:  "status",
		Metadata: map[string]string{"has_direct_mention": "true"},
	}); err != nil {
		t.Fatalf("status submit: %v", err)
	}
	out := mustNextOutbound(t, mb)
	if !strings.Contains(out.Content, "Multiple sciClaw jobs are active") {
		t.Fatalf("expected concise multi-job fallback, got %q", out.Content)
	}
	if len(out.Embeds) != 1 {
		t.Fatalf("expected control embed, got %#v", out.Embeds)
	}
	if out.Embeds[0].Title != "sciClaw · jobs" || !strings.Contains(out.Embeds[0].Description, "`status`") || !embedHas(out.Embeds[0], queuedID) {
		t.Fatalf("expected multi-job status embed with queued id %s, got %#v", queuedID, out.Embeds[0])
	}

	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel:  "discord",
		ChatID:   "room-1",
		Content:  "cancel " + queuedID,
		Metadata: map[string]string{"has_direct_mention": "true"},
	}); err != nil {
		t.Fatalf("cancel submit: %v", err)
	}
	assertNoOutbound(t, mb)
	if !progressHas(progress, "This job was cancelled.") {
		t.Fatalf("expected queued progress card to be updated to cancelled, calls=%v", progress.calls)
	}

	close(runner.block)
	waitForNoActiveJobs(t, jm, target.key())

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.runs != 1 {
		t.Fatalf("expected queued job to stay cancelled, runs=%d", runner.runs)
	}
}

func TestJobManagerAllowsBTWOverlap(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	writeRunner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	readRunner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{
		Enabled:               true,
		MaxConcurrent:         2,
		ProgressUpdateSeconds: 1,
		DiscordAsyncDefault:   true,
		AllowBTWDuringWrite:   true,
	}, mb, progress, func(target LoopTarget) (JobRunner, error) {
		return writeRunner, nil
	})
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}
	jm.SetSideLaneResolver(func(target LoopTarget) (JobRunner, error) {
		return readRunner, nil
	})

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel: "discord",
		ChatID:  "room-1",
		Content: "please update the workspace files",
	}); err != nil {
		t.Fatalf("write submit: %v", err)
	}
	select {
	case <-writeRunner.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for write job start")
	}

	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel: "discord",
		ChatID:  "room-1",
		Content: "/btw collect some images of related probes",
	}); err != nil {
		t.Fatalf("/btw submit: %v", err)
	}
	select {
	case <-readRunner.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for /btw job start")
	}

	close(writeRunner.block)
	close(readRunner.block)
	waitForNoActiveJobs(t, jm, target.key())
}

func TestJobManagerPlainStatusWithoutDirectionStartsNormalJob(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
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

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel: "discord",
		ChatID:  "room-1",
		Content: "status",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("expected plain status to start a normal job when not bot-directed")
	}

	close(runner.block)
	waitForNoActiveJobs(t, jm, target.key())
}

func TestJobManagerDirectedStatusWithExtraWordsStartsNormalJob(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
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

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel:  "discord",
		ChatID:   "room-1",
		Content:  "status and hi",
		Metadata: map[string]string{"has_direct_mention": "true"},
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("expected directed natural-language status message to start a normal job")
	}

	close(runner.block)
	waitForNoActiveJobs(t, jm, target.key())
}

func TestJobManagerDirectedCancelWithExtraWordsStartsNormalJob(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
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

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel:  "discord",
		ChatID:   "room-1",
		Content:  "cancel that literature brief later",
		Metadata: map[string]string{"has_direct_mention": "true"},
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("expected directed natural-language cancel message to start a normal job")
	}

	close(runner.block)
	waitForNoActiveJobs(t, jm, target.key())
}

func TestJobManagerDirectedStatusWithoutActiveJobsReturnsControlCard(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{
		Enabled:               true,
		MaxConcurrent:         1,
		ProgressUpdateSeconds: 1,
		DiscordAsyncDefault:   true,
	}, mb, progress, func(target LoopTarget) (JobRunner, error) {
		return &fakeJobRunner{}, nil
	})
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel:  "discord",
		ChatID:   "room-1",
		Content:  "status",
		Metadata: map[string]string{"has_direct_mention": "true"},
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	out := mustNextOutbound(t, mb)
	if !strings.Contains(out.Content, "No running or queued jobs in this workspace.") {
		t.Fatalf("expected concise fallback, got %q", out.Content)
	}
	if len(out.Embeds) != 1 {
		t.Fatalf("expected control embed, got %#v", out.Embeds)
	}
	embed := out.Embeds[0]
	if embed.Title != "sciClaw · jobs" || !strings.Contains(embed.Description, "`status`") {
		t.Fatalf("expected status intercept card, got %#v", embed)
	}
	if !embedHas(embed, "No running or queued jobs in this workspace.") || !embedHas(embed, "`@sciclaw-app <request>` · `/btw <question>`") {
		t.Fatalf("expected actionable control hints, got %#v", embed)
	}
}

func TestJobManagerStatusListsMultipleJobs(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	writeRunner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	readRunner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{
		Enabled:               true,
		MaxConcurrent:         2,
		ProgressUpdateSeconds: 1,
		DiscordAsyncDefault:   true,
		AllowBTWDuringWrite:   true,
	}, mb, progress, func(target LoopTarget) (JobRunner, error) {
		return writeRunner, nil
	})
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}
	jm.SetSideLaneResolver(func(target LoopTarget) (JobRunner, error) {
		return readRunner, nil
	})

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	_ = jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "please update the workspace files"})
	<-writeRunner.started
	_ = jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "/btw collect some images of related probes"})
	<-readRunner.started

	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel:  "discord",
		ChatID:   "room-1",
		Content:  "status",
		Metadata: map[string]string{"has_direct_mention": "true"},
	}); err != nil {
		t.Fatalf("status submit: %v", err)
	}
	out := mustNextOutbound(t, mb)
	if len(out.Embeds) != 1 {
		t.Fatalf("expected multi-job status embed, got %#v", out.Embeds)
	}
	if out.Embeds[0].Title != "sciClaw · jobs" || !embedHas(out.Embeds[0], "/btw lane") {
		t.Fatalf("expected job list to label /btw lane, got %#v", out.Embeds[0])
	}

	close(writeRunner.block)
	close(readRunner.block)
	waitForNoActiveJobs(t, jm, target.key())
}

func TestJobManagerWritesProgressAndFinalState(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{
		Enabled:               true,
		MaxConcurrent:         1,
		ProgressUpdateSeconds: 1,
		DiscordAsyncDefault:   true,
	}, mb, progress, func(target LoopTarget) (JobRunner, error) {
		return &fakeJobRunner{}, nil
	})
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "do it"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		progress.mu.Lock()
		callCount := len(progress.calls)
		progress.mu.Unlock()
		if callCount >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	progress.mu.Lock()
	defer progress.mu.Unlock()
	if len(progress.calls) < 2 {
		t.Fatalf("expected at least 2 progress updates, got %d", len(progress.calls))
	}
	if !strings.Contains(progress.calls[0], "sciClaw ·") || !strings.Contains(progress.calls[0], "> do it") || !strings.Contains(progress.calls[0], "<t:") {
		t.Fatalf("expected progress metadata, got %q", progress.calls[0])
	}
	if !strings.Contains(progress.calls[len(progress.calls)-1], "Done. Reply below.") {
		t.Fatalf("expected final progress update, got %q", progress.calls[len(progress.calls)-1])
	}
}

func TestJobManagerResolverFailureLogsDiagnosticWithoutLeakingIt(t *testing.T) {
	const rawCanary = "RAW_CANARY_resolver_failure_4d4c"
	mb := bus.NewMessageBus()
	defer mb.Close()

	logPath := filepathJoin(t.TempDir(), "server.log")
	if err := logger.EnableFileLogging(logPath); err != nil {
		t.Fatalf("EnableFileLogging: %v", err)
	}
	defer logger.DisableFileLogging()

	progress := &fakeProgressMessenger{}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{
		Enabled: true, MaxConcurrent: 1, ProgressUpdateSeconds: 1, DiscordAsyncDefault: true,
	}, mb, progress, func(LoopTarget) (JobRunner, error) {
		return &fakeJobRunner{}, nil
	})
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}
	jm.SetSideLaneResolver(func(LoopTarget) (JobRunner, error) {
		return nil, fmt.Errorf("provider creation failed: %s", rawCanary)
	})

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel: "discord", ChatID: "room-1", Content: "/btw inspect this",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForNoActiveJobs(t, jm, target.key())

	record := findJobRecordByAskSummary(t, jm, "inspect this")
	if record.State != JobStateFailed {
		t.Fatalf("state = %q, want failed", record.State)
	}
	if strings.Contains(record.LastError, rawCanary) {
		t.Fatalf("raw diagnostics leaked into persisted error: %q", record.LastError)
	}
	for _, message := range []bus.OutboundMessage{
		formatProgressOutbound(record),
		formatControlRecordOutbound(parsedJobControl{Kind: jobControlStatus}, record, ""),
	} {
		if strings.Contains(fmt.Sprintf("%#v", message), rawCanary) {
			t.Fatalf("raw diagnostics leaked into rendered job output: %#v", message)
		}
	}
	progress.mu.Lock()
	for _, message := range progress.messages {
		if strings.Contains(fmt.Sprintf("%#v", message), rawCanary) {
			progress.mu.Unlock()
			t.Fatalf("raw diagnostics leaked into progress output: %#v", message)
		}
	}
	progress.mu.Unlock()

	logger.DisableFileLogging()
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	logOutput := string(logData)
	for _, want := range []string{rawCanary, record.ID, record.Phase} {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("server log missing %q: %s", want, logOutput)
		}
	}
}

func TestJobManagerFailedJobKeepsSafeReferenceOnly(t *testing.T) {
	const rawCanary = "RAW_CANARY_job_failure_2d86"
	const reference = "ERR-job-failure"
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	runner := &fakeJobRunner{
		started: make(chan struct{}),
		block:   make(chan struct{}),
		err: safeJobError{
			raw:     "provider diagnostics: " + rawCanary,
			message: "The AI service is busy.\n\nReference ID: `" + reference + "`",
		},
	}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{
		Enabled: true, MaxConcurrent: 1, ProgressUpdateSeconds: 1, DiscordAsyncDefault: true,
	}, mb, progress, func(LoopTarget) (JobRunner, error) { return runner, nil })
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "do it"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-runner.started
	close(runner.block)
	waitForNoActiveJobs(t, jm, target.key())

	record := findJobRecordByAskSummary(t, jm, "do it")
	if record.State != JobStateFailed || strings.Contains(record.LastError, rawCanary) || !strings.Contains(record.LastError, reference) {
		t.Fatalf("unsafe persisted failure: %#v", record)
	}

	progress.mu.Lock()
	defer progress.mu.Unlock()
	for _, call := range progress.messages {
		rendered := call.content + call.title + call.embed.Description + call.embed.Footer
		for _, field := range call.embed.Fields {
			rendered += field.Name + field.Value
		}
		if strings.Contains(rendered, rawCanary) {
			t.Fatalf("raw diagnostics leaked into job card: %#v", call)
		}
		if strings.Contains(rendered, reference) {
			return
		}
	}
	t.Fatalf("failed job card omitted safe reference %q: %#v", reference, progress.messages)
}

func TestJobManagerBTWBusyMessageExplainsQueueOptions(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	writeRunner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	btwRunner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{
		Enabled:               true,
		MaxConcurrent:         2,
		ProgressUpdateSeconds: 1,
		DiscordAsyncDefault:   true,
		AllowBTWDuringWrite:   true,
	}, mb, progress, func(target LoopTarget) (JobRunner, error) {
		return writeRunner, nil
	})
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}
	jm.SetSideLaneResolver(func(target LoopTarget) (JobRunner, error) {
		return btwRunner, nil
	})

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel: "discord",
		ChatID:  "room-1",
		Content: "/btw collect some images of related probes",
	}); err != nil {
		t.Fatalf("first /btw submit: %v", err)
	}
	select {
	case <-btwRunner.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first /btw job start")
	}

	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel: "discord",
		ChatID:  "room-1",
		Content: "/btw collect more probe images",
	}); err != nil {
		t.Fatalf("second /btw submit: %v", err)
	}
	out := mustNextOutbound(t, mb)
	if !strings.Contains(out.Content, "The /btw lane is already in use") {
		t.Fatalf("expected /btw busy message, got %q", out.Content)
	}
	if !strings.Contains(out.Content, "Reply on that job card with `status` or `cancel`") || !strings.Contains(out.Content, "place it in the main queue") {
		t.Fatalf("expected /btw busy message to explain cancel or main-queue fallback, got %q", out.Content)
	}

	close(btwRunner.block)
	waitForNoActiveJobs(t, jm, target.key())
}

func TestJobManagerRefreshesProgressDuringLongRunningPhase(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
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
	jm.progressInterval = 50 * time.Millisecond

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "do slow work"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for job start")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if countMatchingProgress(progress, "**Thinking**") >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got := countMatchingProgress(progress, "**Thinking**"); got < 2 {
		t.Fatalf("expected repeated thinking progress updates, got %d calls: %#v", got, progress.calls)
	}

	close(runner.block)

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		jm.mu.Lock()
		_, ok := jm.active[target.key()]
		jm.mu.Unlock()
		if !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for long-running job cleanup")
}

func TestJobManagerOrdinaryResearchTurnQueuesMainLane(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	writeRunner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	readRunner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	jm, err := NewJobManager(filepathJoin(t.TempDir(), "jobs.json"), config.JobsConfig{
		Enabled:               true,
		MaxConcurrent:         2,
		ProgressUpdateSeconds: 1,
		DiscordAsyncDefault:   true,
		AllowBTWDuringWrite:   true,
	}, mb, progress, func(target LoopTarget) (JobRunner, error) {
		return writeRunner, nil
	})
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}
	jm.SetSideLaneResolver(func(target LoopTarget) (JobRunner, error) {
		return readRunner, nil
	})

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel: "discord",
		ChatID:  "room-1",
		Content: "please update the workspace files",
	}); err != nil {
		t.Fatalf("write submit: %v", err)
	}
	select {
	case <-writeRunner.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for write job start")
	}

	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel: "discord",
		ChatID:  "room-1",
		Content: "collect some images of related probes",
	}); err != nil {
		t.Fatalf("ordinary research submit: %v", err)
	}
	waitForQueuedWrite(t, jm, target.key(), 1)

	select {
	case <-readRunner.started:
		t.Fatal("ordinary research turn should not start the explicit /btw lane implicitly")
	case <-time.After(100 * time.Millisecond):
	}

	writeRunner.mu.Lock()
	if writeRunner.runs != 1 {
		writeRunner.mu.Unlock()
		t.Fatalf("expected only first write to be running, runs=%d", writeRunner.runs)
	}
	writeRunner.mu.Unlock()

	nextBlock := make(chan struct{})
	nextStarted := make(chan struct{})
	writeRunner.started = nextStarted
	oldBlock := writeRunner.block
	writeRunner.block = nextBlock
	close(oldBlock)

	select {
	case <-nextStarted:
	case <-time.After(time.Second):
		t.Fatal("queued main-lane job did not start after the first write completed")
	}

	close(nextBlock)
	waitForNoActiveJobs(t, jm, target.key())
}

func TestJobManagerQueuedSubmissionSurvivesProgressFailure(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
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

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "first job"}); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first job start")
	}

	progress.mu.Lock()
	progress.sendFail = true
	progress.mu.Unlock()
	err = jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "second job"})
	if err != nil {
		t.Fatalf("expected durable enqueue, got %v", err)
	}
	out := mustNextOutbound(t, mb)
	if !strings.Contains(out.Content, "Could not send a job status card") {
		t.Fatalf("missing status failure notice: %#v", out)
	}

	jm.mu.Lock()
	activeSet := jm.active[target.key()]
	queued := 0
	if activeSet != nil {
		queued = len(activeSet.queuedWrites)
	}
	jm.mu.Unlock()
	if queued != 1 {
		t.Fatalf("expected retained queued job, got %d", queued)
	}
	records, err := jm.store.All()
	if err != nil {
		t.Fatalf("store.All: %v", err)
	}
	for _, record := range records {
		if record.AskSummary == "second job" && record.State != JobStateQueued {
			t.Fatalf("expected second job to remain queued: %#v", record)
		}
	}

	close(runner.block)
	waitForNoActiveJobs(t, jm, target.key())

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.runs != 2 {
		t.Fatalf("expected both jobs to execute, runs=%d", runner.runs)
	}
}

func TestJobManagerReplyToQueuedCardCancelsJob(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
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

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "first job"}); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first job start")
	}

	if err := jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "second job"}); err != nil {
		t.Fatalf("second submit: %v", err)
	}
	waitForQueuedWrite(t, jm, target.key(), 1)
	waitForQueuedCards(t, jm, target.key(), 1)
	jm.mu.Lock()
	queuedStatusID := jm.active[target.key()].queuedWrites[0].snapshot().StatusMessageID
	jm.mu.Unlock()
	progress.editReplacementID = "progress-replacement"

	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel:  "discord",
		ChatID:   "room-1",
		Content:  "cancel",
		Metadata: map[string]string{"reply_message_id": queuedStatusID},
	}); err != nil {
		t.Fatalf("reply cancel submit: %v", err)
	}
	assertNoOutbound(t, mb)
	if !progressHas(progress, "This job was cancelled.") {
		t.Fatalf("expected queued progress card to be updated to cancelled, calls=%v", progress.calls)
	}
	cancelled := findJobRecordByAskSummary(t, jm, "second job")
	if cancelled.State != JobStateCancelled {
		t.Fatalf("expected cancelled state, got %#v", cancelled)
	}
	if cancelled.StatusMessageID != "progress-replacement" {
		t.Fatalf("expected stale-safe card replacement id, got %q", cancelled.StatusMessageID)
	}

	close(runner.block)
	waitForNoActiveJobs(t, jm, target.key())
}

func TestJobManagerReplyToQueuedCardForcesJob(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
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

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "first job"}); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first job start")
	}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "second job"}); err != nil {
		t.Fatalf("second submit: %v", err)
	}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "third job"}); err != nil {
		t.Fatalf("third submit: %v", err)
	}
	waitForQueuedWrite(t, jm, target.key(), 2)
	waitForQueuedCards(t, jm, target.key(), 2)

	jm.mu.Lock()
	second := jm.active[target.key()].queuedWrites[0].snapshot()
	third := jm.active[target.key()].queuedWrites[1].snapshot()
	jm.mu.Unlock()

	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel:  "discord",
		ChatID:   "room-1",
		Content:  "force",
		Metadata: map[string]string{"reply_message_id": third.StatusMessageID},
	}); err != nil {
		t.Fatalf("reply force submit: %v", err)
	}
	assertNoOutbound(t, mb)

	jm.mu.Lock()
	queued := jm.active[target.key()].queuedWrites
	if len(queued) != 2 {
		jm.mu.Unlock()
		t.Fatalf("expected two queued jobs after force, got %d", len(queued))
	}
	if got := queued[0].snapshot().ID; got != third.ID {
		jm.mu.Unlock()
		t.Fatalf("expected forced job %s at front, got %s", third.ID, got)
	}
	if got := queued[1].snapshot().ID; got != second.ID {
		jm.mu.Unlock()
		t.Fatalf("expected original front job %s to move back, got %s", second.ID, got)
	}
	jm.mu.Unlock()
	close(runner.block)
	waitForNoActiveJobs(t, jm, target.key())
}

func TestJobManagerReplyToCardStatusRefreshesCanonicalCard(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	progress := &fakeProgressMessenger{}
	runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
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

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	if err := jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "first job"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first job start")
	}

	jm.mu.Lock()
	statusID := jm.active[target.key()].write.snapshot().StatusMessageID
	jm.mu.Unlock()
	progress.mu.Lock()
	before := len(progress.messages)
	progress.mu.Unlock()

	if err := jm.Submit(context.Background(), target, bus.InboundMessage{
		Channel:  "discord",
		ChatID:   "room-1",
		Content:  "status",
		Metadata: map[string]string{"reply_message_id": statusID},
	}); err != nil {
		t.Fatalf("reply status submit: %v", err)
	}
	assertNoOutbound(t, mb)
	progress.mu.Lock()
	after := len(progress.messages)
	progress.mu.Unlock()
	if after <= before {
		t.Fatalf("expected canonical card refresh, before=%d after=%d", before, after)
	}

	close(runner.block)
	waitForNoActiveJobs(t, jm, target.key())
}

func TestFormatProgressEmbedIncludesElapsed(t *testing.T) {
	originalNow := jobCardNow
	jobCardNow = func() time.Time { return time.Unix(300, 0) }
	defer func() { jobCardNow = originalNow }()

	embed := formatProgressEmbed(JobRecord{
		ShortID:    "0000A",
		Class:      JobClassWrite,
		State:      JobStateRunning,
		Phase:      "thinking",
		AskSummary: "test",
		StartedAt:  time.Unix(235, 0).UnixMilli(),
		UpdatedAt:  time.Unix(300, 0).UnixMilli(),
	})
	if !embedHas(embed, "Elapsed") {
		t.Fatalf("expected elapsed field, got %#v", embed)
	}
	if !embedHas(embed, "1m 05s") {
		t.Fatalf("expected formatted elapsed time, got %#v", embed)
	}
}

func TestJobManagerRestartInterruptsOnlyRunningJobsAndPreservesQueuedBacklog(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	storePath := filepathJoin(t.TempDir(), "jobs.json")
	store, err := newJobStore(storePath)
	if err != nil {
		t.Fatalf("newJobStore: %v", err)
	}

	target := LoopTarget{Workspace: "/tmp/work", Runtime: RuntimeProfile{Mode: config.ModeCloud}}
	running := JobRecord{
		ID:         "job-1000-7",
		ShortID:    formatShortJobRef(7),
		Channel:    "discord",
		ChatID:     "room-1",
		Workspace:  target.Workspace,
		RuntimeKey: target.Runtime.Key(),
		Runtime:    target.Runtime,
		TargetKey:  target.key(),
		Class:      JobClassWrite,
		State:      JobStateRunning,
		Phase:      "thinking",
		Detail:     "Thinking",
		AskSummary: "running job",
		Message: bus.InboundMessage{
			Channel:    "discord",
			ChatID:     "room-1",
			Content:    "running job",
			SessionKey: "discord:room-1@abc123",
		},
		StartedAt: time.Now().Add(-2 * time.Minute).UnixMilli(),
		UpdatedAt: time.Now().Add(-2 * time.Minute).UnixMilli(),
	}
	queued := JobRecord{
		ID:         "job-1000-8",
		ShortID:    formatShortJobRef(8),
		Channel:    "discord",
		ChatID:     "room-1",
		Workspace:  target.Workspace,
		RuntimeKey: target.Runtime.Key(),
		Runtime:    target.Runtime,
		TargetKey:  target.key(),
		Class:      JobClassWrite,
		State:      JobStateQueued,
		Phase:      "queued",
		Detail:     "Queued in main lane · behind 00007 · position 1",
		AskSummary: "resume me",
		Message: bus.InboundMessage{
			Channel:    "discord",
			ChatID:     "room-1",
			Content:    "resume me",
			SessionKey: "discord:room-1@abc123",
		},
		StartedAt: time.Now().Add(-time.Minute).UnixMilli(),
		UpdatedAt: time.Now().Add(-time.Minute).UnixMilli(),
	}
	if err := store.Save(running); err != nil {
		t.Fatalf("Save running: %v", err)
	}
	if err := store.Save(queued); err != nil {
		t.Fatalf("Save queued: %v", err)
	}

	progress := &fakeProgressMessenger{}
	runner := &fakeJobRunner{started: make(chan struct{}), block: make(chan struct{})}
	jm, err := NewJobManager(storePath, config.JobsConfig{
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

	select {
	case <-runner.started:
		t.Fatal("queued backlog should remain queued on restart until new work resumes it")
	case <-time.After(150 * time.Millisecond):
	}

	records, err := jm.store.All()
	if err != nil {
		t.Fatalf("store.All: %v", err)
	}
	gotRunning := findRecordByID(t, records, running.ID)
	if gotRunning.State != JobStateInterrupted {
		t.Fatalf("expected running job to become interrupted, got %#v", gotRunning)
	}
	gotQueued := findRecordByID(t, records, queued.ID)
	if gotQueued.State != JobStateQueued {
		t.Fatalf("expected queued job to remain queued, got %#v", gotQueued)
	}
	if !strings.Contains(gotQueued.Detail, "next up") {
		t.Fatalf("expected preserved queue to be reindexed as next up, got %#v", gotQueued)
	}

	jm.mu.Lock()
	activeSet := jm.active[target.key()]
	if activeSet == nil || activeSet.write != nil || len(activeSet.queuedWrites) != 1 {
		jm.mu.Unlock()
		t.Fatalf("expected preserved backlog without active write, got %#v", activeSet)
	}
	if got := activeSet.queuedWrites[0].snapshot().ID; got != queued.ID {
		jm.mu.Unlock()
		t.Fatalf("expected queued backlog to preserve job %s, got %s", queued.ID, got)
	}
	jm.mu.Unlock()

	if err := jm.Submit(context.Background(), target, bus.InboundMessage{Channel: "discord", ChatID: "room-1", Content: "later job"}); err != nil {
		t.Fatalf("resume submit: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("expected preserved queue head to resume when new write traffic arrives")
	}
	waitForQueuedWrite(t, jm, target.key(), 1)

	jm.mu.Lock()
	activeSet = jm.active[target.key()]
	if activeSet == nil || activeSet.write == nil {
		jm.mu.Unlock()
		t.Fatal("expected resumed write to occupy main lane")
	}
	if got := activeSet.write.snapshot().AskSummary; got != "resume me" {
		jm.mu.Unlock()
		t.Fatalf("expected preserved queued job to resume first, got %q", got)
	}
	if got := activeSet.queuedWrites[0].snapshot().AskSummary; got != "later job" {
		jm.mu.Unlock()
		t.Fatalf("expected new submit to queue behind preserved backlog, got %q", got)
	}
	jm.mu.Unlock()

	close(runner.block)
	waitForNoActiveJobs(t, jm, target.key())
}

func TestJobManagerRestoresCounterFromStore(t *testing.T) {
	storePath := filepathJoin(t.TempDir(), "jobs.json")
	store, err := newJobStore(storePath)
	if err != nil {
		t.Fatalf("newJobStore: %v", err)
	}
	if err := store.Save(JobRecord{
		ID:        "job-1000-42",
		ShortID:   formatShortJobRef(42),
		State:     JobStateDone,
		Channel:   "discord",
		ChatID:    "room-1",
		StartedAt: time.Now().UnixMilli(),
		UpdatedAt: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	mb := bus.NewMessageBus()
	defer mb.Close()
	jm, err := NewJobManager(storePath, config.JobsConfig{
		Enabled:               true,
		MaxConcurrent:         1,
		ProgressUpdateSeconds: 1,
		DiscordAsyncDefault:   true,
	}, mb, &fakeProgressMessenger{}, func(target LoopTarget) (JobRunner, error) {
		return &fakeJobRunner{}, nil
	})
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}

	gotID, gotShort := jm.nextJobID()
	if !strings.HasSuffix(gotID, "-43") {
		t.Fatalf("expected counter to resume at 43, got %q", gotID)
	}
	if gotShort != formatShortJobRef(43) {
		t.Fatalf("expected short ref %q, got %q", formatShortJobRef(43), gotShort)
	}
}

func mustNextOutbound(t *testing.T, mb *bus.MessageBus) bus.OutboundMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg, ok := mb.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected outbound message")
	}
	return msg
}

func assertNoOutbound(t *testing.T, mb *bus.MessageBus) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if msg, ok := mb.SubscribeOutbound(ctx); ok {
		t.Fatalf("expected no outbound message, got %#v", msg)
	}
}

func embedHas(embed bus.OutboundEmbed, needle string) bool {
	if strings.Contains(embed.Title, needle) || strings.Contains(embed.Description, needle) || strings.Contains(embed.Footer, needle) {
		return true
	}
	for _, field := range embed.Fields {
		if strings.Contains(field.Name, needle) || strings.Contains(field.Value, needle) {
			return true
		}
	}
	return false
}

func filepathJoin(elem ...string) string {
	if len(elem) == 0 {
		return ""
	}
	out := elem[0]
	for _, part := range elem[1:] {
		if strings.HasSuffix(out, "/") {
			out += part
		} else {
			out += "/" + part
		}
	}
	return out
}

func countMatchingProgress(progress *fakeProgressMessenger, needle string) int {
	progress.mu.Lock()
	defer progress.mu.Unlock()
	count := 0
	for _, call := range progress.calls {
		if strings.Contains(call, needle) {
			count++
		}
	}
	return count
}

func progressHas(progress *fakeProgressMessenger, needle string) bool {
	progress.mu.Lock()
	defer progress.mu.Unlock()
	for _, call := range progress.calls {
		if strings.Contains(call, needle) {
			return true
		}
	}
	return false
}

func waitForQueuedWrite(t *testing.T, jm *JobManager, targetKey string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		jm.mu.Lock()
		activeSet := jm.active[targetKey]
		got := 0
		if activeSet != nil {
			got = len(activeSet.queuedWrites)
		}
		jm.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d queued writes for %s", want, targetKey)
}

func waitForQueuedCards(t *testing.T, jm *JobManager, targetKey string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		jm.mu.Lock()
		set := jm.active[targetKey]
		ready := set != nil && len(set.queuedWrites) == want
		if ready {
			for _, job := range set.queuedWrites {
				if job.snapshot().StatusMessageID == "" {
					ready = false
					break
				}
			}
		}
		jm.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for queued status cards")
}

func waitForNoActiveJobs(t *testing.T, jm *JobManager, targetKey string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		jm.mu.Lock()
		_, ok := jm.active[targetKey]
		jm.mu.Unlock()
		if !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for active jobs to clear for %s", targetKey)
}

func findJobRecordByAskSummary(t *testing.T, jm *JobManager, askSummary string) JobRecord {
	t.Helper()
	records, err := jm.store.All()
	if err != nil {
		t.Fatalf("store.All: %v", err)
	}
	for _, record := range records {
		if record.AskSummary == askSummary {
			return record
		}
	}
	t.Fatalf("expected job record with ask summary %q", askSummary)
	return JobRecord{}
}

func findRecordByID(t *testing.T, records []JobRecord, id string) JobRecord {
	t.Helper()
	for _, record := range records {
		if record.ID == id {
			return record
		}
	}
	t.Fatalf("expected job record %q in %#v", id, records)
	return JobRecord{}
}
