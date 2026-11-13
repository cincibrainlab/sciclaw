package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

type JobState string

const (
	JobStateQueued      JobState = "queued"
	JobStateRunning     JobState = "running"
	JobStateDone        JobState = "done"
	JobStateFailed      JobState = "failed"
	JobStateCancelled   JobState = "cancelled"
	JobStateInterrupted JobState = "interrupted"
)

type ProgressMessenger interface {
	SendOrEditProgress(ctx context.Context, channelName, chatID, messageID string, msg bus.OutboundMessage) (string, error)
}

// boundedProgress limits calls even when a messenger ignores context cancellation.
type boundedProgress struct {
	next  ProgressMessenger
	slots chan struct{}
}

type reservedProgressKey struct{}

func (p *boundedProgress) SendOrEditProgress(ctx context.Context, channel, chat, id string, msg bus.OutboundMessage) (string, error) {
	reservation, _ := ctx.Value(reservedProgressKey{}).(*atomic.Bool)
	if reservation == nil || !reservation.CompareAndSwap(false, true) {
		select {
		case p.slots <- struct{}{}:
		default:
			return "", fmt.Errorf("progress capacity exhausted")
		}
	}
	type result struct {
		id  string
		err error
	}
	resultCh := make(chan result, 1)
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	go func() {
		defer func() { <-p.slots }()
		id, err := p.next.SendOrEditProgress(ctx, channel, chat, id, msg)
		resultCh <- result{id, err}
	}()
	select {
	case r := <-resultCh:
		return r.id, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type handlerResolverFunc func(target LoopTarget) (JobRunner, error)

type cancellationSaveError struct{ cause error }

func (e cancellationSaveError) Error() string {
	return "Cancellation could not be saved; this job may run after restart."
}

func (e cancellationSaveError) Unwrap() error { return e.cause }

type JobClass string

const (
	JobClassWrite JobClass = "write"
	JobClassBTW   JobClass = "btw"
	// JobClassExternalReadOnly is a legacy alias for pre-/btw scheduler
	// naming. New code should use JobClassBTW.
	JobClassExternalReadOnly JobClass = JobClassBTW
)

type JobRecord struct {
	revision           uint64
	ID                 string             `json:"id"`
	ShortID            string             `json:"short_id"`
	Channel            string             `json:"channel"`
	ChatID             string             `json:"chat_id"`
	Workspace          string             `json:"workspace"`
	RuntimeKey         string             `json:"runtime_key"`
	TargetKey          string             `json:"target_key"`
	Class              JobClass           `json:"class"`
	State              JobState           `json:"state"`
	Phase              string             `json:"phase,omitempty"`
	Detail             string             `json:"detail,omitempty"`
	StatusMessageID    string             `json:"status_message_id,omitempty"`
	Summary            string             `json:"summary,omitempty"`
	AskSummary         string             `json:"ask_summary,omitempty"`
	LastError          string             `json:"last_error,omitempty"`
	Runtime            RuntimeProfile     `json:"runtime,omitempty"`
	Message            bus.InboundMessage `json:"message,omitempty"`
	AttachmentsPending bool               `json:"attachments_pending,omitempty"`
	StartedAt          int64              `json:"started_at"`
	UpdatedAt          int64              `json:"updated_at"`
}

type jobStore struct {
	path string
	mu   sync.Mutex
	jobs map[string]JobRecord
}

type activeJob struct {
	mu       sync.RWMutex
	record   JobRecord
	cancel   context.CancelFunc
	ctx      context.Context
	target   LoopTarget
	msg      bus.InboundMessage
	prepared bool
}

type targetActiveJobs struct {
	write        *activeJob
	btw          *activeJob
	queuedWrites []*activeJob
	preparing    []*activeJob
}

func (j *activeJob) snapshot() JobRecord {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.record
}

func (j *activeJob) update(mutator func(*JobRecord)) JobRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	mutator(&j.record)
	j.record.revision++
	return j.record
}

func (s *targetActiveJobs) set(class JobClass, job *activeJob) {
	switch class {
	case JobClassBTW:
		s.btw = job
	default:
		s.write = job
	}
}

func (s *targetActiveJobs) clear(class JobClass) {
	switch class {
	case JobClassBTW:
		s.btw = nil
	default:
		s.write = nil
	}
}

func (s *targetActiveJobs) jobForClass(class JobClass) *activeJob {
	switch class {
	case JobClassBTW:
		return s.btw
	default:
		return s.write
	}
}

func (s *targetActiveJobs) empty() bool {
	return s == nil || (s.write == nil && s.btw == nil && len(s.queuedWrites) == 0 && len(s.preparing) == 0)
}

func (s *targetActiveJobs) isFull() bool {
	return s != nil && s.write != nil && s.btw != nil
}

func (s *targetActiveJobs) snapshot() []*activeJob {
	if s == nil {
		return nil
	}
	out := make([]*activeJob, 0, 2)
	if s.write != nil {
		out = append(out, s.write)
	}
	if s.btw != nil {
		out = append(out, s.btw)
	}
	return out
}

func (s *targetActiveJobs) snapshotAll() []*activeJob {
	out := s.snapshot()
	if s == nil {
		return out
	}
	out = append(out, s.queuedWrites...)
	out = append(out, s.preparing...)
	return out
}

func (s *targetActiveJobs) enqueueWrite(job *activeJob) int {
	if s == nil || job == nil {
		return 0
	}
	s.queuedWrites = append(s.queuedWrites, job)
	return len(s.queuedWrites)
}

func (s *targetActiveJobs) popQueuedWrite() *activeJob {
	if s == nil || len(s.queuedWrites) == 0 {
		return nil
	}
	job := s.queuedWrites[0]
	s.queuedWrites = s.queuedWrites[1:]
	return job
}

func (s *targetActiveJobs) removeQueuedWrite(jobID string) *activeJob {
	if s == nil || strings.TrimSpace(jobID) == "" {
		return nil
	}
	for i, job := range s.queuedWrites {
		if job == nil {
			continue
		}
		record := job.snapshot()
		if record.ID != jobID {
			continue
		}
		s.queuedWrites = append(s.queuedWrites[:i], s.queuedWrites[i+1:]...)
		return job
	}
	return nil
}

func (s *targetActiveJobs) moveQueuedWriteToFront(jobID string) *activeJob {
	if s == nil || strings.TrimSpace(jobID) == "" || len(s.queuedWrites) == 0 {
		return nil
	}
	for i, job := range s.queuedWrites {
		if job == nil {
			continue
		}
		record := job.snapshot()
		if record.ID != jobID {
			continue
		}
		if i == 0 {
			return job
		}
		s.queuedWrites = append([]*activeJob{job}, append(s.queuedWrites[:i], s.queuedWrites[i+1:]...)...)
		return job
	}
	return nil
}

type JobManager struct {
	bus                    *bus.MessageBus
	progress               ProgressMessenger
	resolve                handlerResolverFunc
	resolveSideLane        handlerResolverFunc
	progressInterval       time.Duration
	store                  *jobStore
	semaphore              chan struct{}
	preparation            chan struct{}
	statusSlots            chan struct{}
	cancelRetries          chan struct{}
	allowBTWDuringWrite    bool
	mediaRewrites          func() map[string]string
	interruptedAttachments []JobRecord

	mu      sync.Mutex
	active  map[string]*targetActiveJobs
	counter uint64
}

type parsedJobRequest struct {
	Class   JobClass
	Message bus.InboundMessage
}

func formatShortJobRef(n uint64) string {
	const width = 5
	ref := strings.ToUpper(strconv.FormatUint(n, 36))
	if len(ref) >= width {
		return ref[len(ref)-width:]
	}
	return strings.Repeat("0", width-len(ref)) + ref
}

func NewJobManager(storePath string, cfg config.JobsConfig, msgBus *bus.MessageBus, progress ProgressMessenger, resolve handlerResolverFunc, rewrites ...func() map[string]string) (*JobManager, error) {
	if strings.TrimSpace(storePath) == "" {
		return nil, fmt.Errorf("job store path is required")
	}
	if resolve == nil {
		return nil, fmt.Errorf("job handler resolver is required")
	}
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	progressEvery := time.Duration(cfg.ProgressUpdateSeconds) * time.Second
	if progressEvery <= 0 {
		progressEvery = 5 * time.Second
	}
	store, err := newJobStore(storePath)
	if err != nil {
		return nil, err
	}
	jm := &JobManager{
		bus:                 msgBus,
		progress:            progress,
		resolve:             resolve,
		progressInterval:    progressEvery,
		store:               store,
		semaphore:           make(chan struct{}, maxConcurrent),
		preparation:         make(chan struct{}, maxConcurrent*4),
		statusSlots:         make(chan struct{}, maxConcurrent*4),
		cancelRetries:       make(chan struct{}, maxConcurrent*4),
		allowBTWDuringWrite: cfg.AllowBTWDuringWrite || cfg.AllowReadOnlyDuringWrite,
		active:              map[string]*targetActiveJobs{},
	}
	if progress != nil {
		jm.progress = &boundedProgress{next: progress, slots: make(chan struct{}, maxConcurrent*4)}
	}
	if len(rewrites) > 0 {
		jm.mediaRewrites = rewrites[0]
	}
	if err := jm.restoreCounterFromStore(); err != nil {
		return nil, err
	}
	if err := jm.interruptStaleActiveJobs(); err != nil {
		return nil, err
	}
	if err := jm.rehydrateQueuedJobs(); err != nil {
		return nil, err
	}
	return jm, nil
}

func (jm *JobManager) SetSideLaneResolver(resolve handlerResolverFunc) {
	jm.mu.Lock()
	jm.resolveSideLane = resolve
	jm.mu.Unlock()
}

// SetExternalReadOnlyResolver is a legacy alias for pre-/btw scheduler
// naming. New call sites should use SetSideLaneResolver.
func (jm *JobManager) SetExternalReadOnlyResolver(resolve handlerResolverFunc) {
	jm.SetSideLaneResolver(resolve)
}

func (jm *JobManager) ShouldHandleChannel(channel string) bool {
	return strings.EqualFold(strings.TrimSpace(channel), "discord")
}

func (jm *JobManager) resolveForClass(class JobClass, target LoopTarget) (JobRunner, error) {
	if class == JobClassBTW {
		jm.mu.Lock()
		resolve := jm.resolveSideLane
		jm.mu.Unlock()
		if resolve == nil {
			return nil, fmt.Errorf("/btw job runner is not configured")
		}
		return resolve(target)
	}
	return jm.resolve(target)
}

func (jm *JobManager) Submit(ctx context.Context, target LoopTarget, msg bus.InboundMessage) error {
	target = target.normalized()
	targetKey := target.key()
	control := parseJobControl(msg)
	request := classifyJobRequest(msg)
	jobClass := request.Class

	jm.mu.Lock()
	activeSet := jm.active[targetKey]
	knownJobs := activeSet.snapshotAll()
	if len(msg.Media) == 0 && control.Kind != jobControlNone && (control.Directed || replyTargetsTrackedJob(knownJobs, msg.Metadata["reply_message_id"])) {
		jm.mu.Unlock()
		return jm.handleControl(ctx, msg, targetKey, knownJobs, control)
	}
	if activeSet == nil {
		activeSet = &targetActiveJobs{}
		jm.active[targetKey] = activeSet
	}
	if jobClass == JobClassBTW {
		busy := activeSet.btw != nil || (!jm.allowBTWDuringWrite && activeSet.write != nil)
		for _, job := range activeSet.preparing {
			busy = busy || job.snapshot().Class == JobClassBTW
		}
		if busy {
			jm.mu.Unlock()
			return jm.sendBusy(ctx, msg, snapshotActiveJobs(knownJobs), jobClass)
		}
	}
	var recoveredWrite *activeJob
	if jobClass == JobClassWrite && activeSet.write == nil && len(activeSet.queuedWrites) > 0 && (jm.allowBTWDuringWrite || activeSet.btw == nil) {
		recoveredWrite = activeSet.popQueuedWrite()
		activeSet.write = recoveredWrite
	}
	preparing := len(request.Message.Media) > 0
	if preparing {
		select {
		case jm.preparation <- struct{}{}:
		default:
			if activeSet.empty() {
				delete(jm.active, targetKey)
			}
			jm.mu.Unlock()
			if recoveredWrite != nil {
				go jm.runJob(recoveredWrite.ctx, recoveredWrite.target, recoveredWrite.msg, recoveredWrite)
			}
			if jm.bus == nil {
				return fmt.Errorf("too many attachments are being prepared; please try again shortly")
			}
			return jm.sendText(ctx, msg.Channel, msg.ChatID, "Too many attachments are being prepared; please try again shortly.")
		}
	}
	select {
	case jm.statusSlots <- struct{}{}:
	default:
		if preparing {
			<-jm.preparation
		}
		if activeSet.empty() {
			delete(jm.active, targetKey)
		}
		jm.mu.Unlock()
		if recoveredWrite != nil {
			go jm.runJob(recoveredWrite.ctx, recoveredWrite.target, recoveredWrite.msg, recoveredWrite)
		}
		return fmt.Errorf("job status capacity exhausted; please try again shortly")
	}
	progress, _ := jm.progress.(*boundedProgress)
	if progress != nil {
		select {
		case progress.slots <- struct{}{}:
		default:
			<-jm.statusSlots
			if preparing {
				<-jm.preparation
			}
			if activeSet.empty() {
				delete(jm.active, targetKey)
			}
			jm.mu.Unlock()
			if recoveredWrite != nil {
				go jm.runJob(recoveredWrite.ctx, recoveredWrite.target, recoveredWrite.msg, recoveredWrite)
			}
			return fmt.Errorf("job progress capacity exhausted; please try again shortly")
		}
	}
	jobID, shortID := jm.nextJobID()
	jobCtx, cancel := context.WithCancel(ctx)
	persistedMessage := request.Message
	persistedMessage.Media = nil
	record := JobRecord{
		ID:                 jobID,
		ShortID:            shortID,
		Channel:            msg.Channel,
		ChatID:             msg.ChatID,
		Workspace:          target.Workspace,
		RuntimeKey:         target.Runtime.Key(),
		TargetKey:          targetKey,
		Class:              jobClass,
		State:              JobStateQueued,
		Phase:              "queued",
		Detail:             "Queued",
		AskSummary:         summarizeJobAsk(request.Message.Content),
		Runtime:            target.Runtime,
		Message:            persistedMessage,
		AttachmentsPending: len(request.Message.Media) > 0,
		StartedAt:          time.Now().UnixMilli(),
		UpdatedAt:          time.Now().UnixMilli(),
	}
	active := &activeJob{
		record: record,
		cancel: cancel,
		ctx:    jobCtx,
		target: target,
		msg:    request.Message,
	}
	if preparing {
		active.record.Phase = "preparing_context"
		active.record.Detail = "Preparing attachments"
		activeSet.preparing = append(activeSet.preparing, active)
		if err := jm.store.Save(active.record); err != nil {
			activeSet.preparing = activeSet.preparing[:len(activeSet.preparing)-1]
			if activeSet.empty() {
				delete(jm.active, targetKey)
			}
			jm.mu.Unlock()
			<-jm.preparation
			<-jm.statusSlots
			if progress != nil {
				<-progress.slots
			}
			cancel()
			if recoveredWrite != nil {
				go jm.runJob(recoveredWrite.ctx, recoveredWrite.target, recoveredWrite.msg, recoveredWrite)
			}
			return fmt.Errorf("persist preparing job: %w", err)
		}
		jm.mu.Unlock()
		if recoveredWrite != nil {
			go jm.runJob(recoveredWrite.ctx, recoveredWrite.target, recoveredWrite.msg, recoveredWrite)
		}
		jm.sendQueuedStatusReserved(ctx, active, JobRecord{}, 0, progress != nil)
		go jm.prepareJob(active)
		return nil
	}
	var (
		startImmediately bool
		queuedPosition   int
		activeWrite      JobRecord
	)
	switch jobClass {
	case JobClassWrite:
		if activeSet.write != nil || (!jm.allowBTWDuringWrite && activeSet.btw != nil) {
			if activeSet.write != nil {
				activeWrite = activeSet.write.snapshot()
			} else {
				activeWrite = activeSet.btw.snapshot()
			}
			queuedPosition = activeSet.enqueueWrite(active)
			record.Detail = fmt.Sprintf("Queued in main lane · behind %s · position %d", activeWrite.ShortID, queuedPosition)
			record.Summary = record.Detail
			active.record = record
		} else {
			activeSet.set(jobClass, active)
			startImmediately = true
		}
	case JobClassBTW:
		activeSet.set(jobClass, active)
		startImmediately = true
	default:
		activeSet.set(jobClass, active)
		startImmediately = true
	}
	if err := jm.store.Save(record); err != nil {
		if startImmediately && activeSet.jobForClass(jobClass) == active {
			activeSet.clear(jobClass)
		} else {
			activeSet.removeQueuedWrite(record.ID)
		}
		if activeSet.empty() {
			delete(jm.active, targetKey)
		}
		jm.mu.Unlock()
		<-jm.statusSlots
		if progress != nil {
			<-progress.slots
		}
		cancel()
		if recoveredWrite != nil {
			go jm.runJob(recoveredWrite.ctx, recoveredWrite.target, recoveredWrite.msg, recoveredWrite)
		}
		return fmt.Errorf("persist queued job: %w", err)
	}
	if !startImmediately && jobClass == JobClassWrite {
		context.AfterFunc(jobCtx, func() { jm.autoCancelQueuedJob(targetKey, record.ID) })
	}
	jm.mu.Unlock()

	if recoveredWrite != nil {
		go jm.runJob(recoveredWrite.ctx, recoveredWrite.target, recoveredWrite.msg, recoveredWrite)
	}

	if !startImmediately {
		jm.sendQueuedStatusReserved(ctx, active, activeWrite, queuedPosition, progress != nil)
		return nil
	}
	jm.sendQueuedStatusReserved(ctx, active, JobRecord{}, 0, progress != nil)
	go jm.runJob(jobCtx, target, request.Message, active)
	return nil
}

func (jm *JobManager) sendQueuedStatusAsync(ctx context.Context, active *activeJob, running JobRecord, position int) bool {
	select {
	case jm.statusSlots <- struct{}{}:
	default:
		logger.WarnCF("jobs", "Queued status capacity exhausted", map[string]interface{}{"job_id": active.snapshot().ID})
		return false
	}
	jm.sendQueuedStatusReserved(ctx, active, running, position, false)
	return true
}

func (jm *JobManager) sendQueuedStatusReserved(ctx context.Context, active *activeJob, running JobRecord, position int, progressReserved bool) {
	go func() {
		defer func() { <-jm.statusSlots }()
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if progressReserved {
			ctx = context.WithValue(ctx, reservedProgressKey{}, &atomic.Bool{})
		}
		if err := jm.sendQueuedStatus(ctx, active, running, position); err != nil {
			logger.WarnCF("jobs", "Failed to send queued status", map[string]interface{}{"job_id": active.snapshot().ID, "error": err.Error()})
			record := active.snapshot()
			noticeCtx, cancelNotice := context.WithTimeout(context.Background(), time.Second)
			defer cancelNotice()
			if noticeErr := jm.sendText(noticeCtx, record.Channel, record.ChatID, "Could not send a job status card; use status "+record.ShortID+" to check the job."); noticeErr != nil {
				logger.WarnCF("jobs", "Failed to send status failure notice", map[string]interface{}{"job_id": record.ID, "error": noticeErr.Error()})
			}
		}
	}()
}

func replyTargetsTrackedJob(jobs []*activeJob, replyMessageID string) bool {
	replyMessageID = strings.TrimSpace(replyMessageID)
	if replyMessageID == "" {
		return false
	}
	for _, job := range jobs {
		if job == nil {
			continue
		}
		if strings.TrimSpace(job.snapshot().StatusMessageID) == replyMessageID {
			return true
		}
	}
	return false
}

func (jm *JobManager) interruptStaleActiveJobs() error {
	records, err := jm.store.All()
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.State != JobStateRunning {
			continue
		}
		record.State = JobStateInterrupted
		record.Phase = "interrupted"
		record.Detail = "Gateway restarted before this job finished"
		if record.AttachmentsPending {
			record.Detail = "Gateway restarted; please resend the attachments"
		}
		record.LastError = "interrupted on restart"
		record.UpdatedAt = time.Now().UnixMilli()
		if err := jm.store.Save(record); err != nil {
			return err
		}
		if record.AttachmentsPending {
			jm.interruptedAttachments = append(jm.interruptedAttachments, record)
		}
	}
	return nil
}

func (jm *JobManager) rehydrateQueuedJobs() error {
	records, err := jm.store.All()
	if err != nil {
		return err
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].StartedAt == records[j].StartedAt {
			return records[i].ID < records[j].ID
		}
		return records[i].StartedAt < records[j].StartedAt
	})

	for _, record := range records {
		if record.State != JobStateQueued {
			continue
		}
		if record.AttachmentsPending {
			record.State = JobStateInterrupted
			record.Phase = "interrupted"
			record.Detail = "Gateway restarted; please resend the attachments"
			record.Summary = record.Detail
			record.UpdatedAt = time.Now().UnixMilli()
			if err := jm.store.Save(record); err != nil {
				return err
			}
			jm.interruptedAttachments = append(jm.interruptedAttachments, record)
			continue
		}
		target, msg, err := rehydrateQueuedJob(record)
		if err != nil {
			record.State = JobStateInterrupted
			record.Phase = "interrupted"
			record.Detail = "Queued job could not be resumed after restart"
			record.Summary = record.Detail
			record.LastError = utils.Truncate(compactJobLine(err.Error()), 180)
			record.UpdatedAt = time.Now().UnixMilli()
			if saveErr := jm.store.Save(record); saveErr != nil {
				return saveErr
			}
			continue
		}

		jobCtx, cancel := context.WithCancel(context.Background())
		active := &activeJob{
			record: record,
			cancel: cancel,
			ctx:    jobCtx,
			target: target,
			msg:    msg,
		}

		targetKey := target.key()
		jm.mu.Lock()
		activeSet := jm.active[targetKey]
		if activeSet == nil {
			activeSet = &targetActiveJobs{}
			jm.active[targetKey] = activeSet
		}
		activeSet.enqueueWrite(active)
		jm.mu.Unlock()
	}
	var updatedQueued []JobRecord
	jm.mu.Lock()
	for _, activeSet := range jm.active {
		if activeSet == nil {
			continue
		}
		updatedQueued = append(updatedQueued, jm.reindexQueuedWritesLocked(activeSet)...)
	}
	jm.mu.Unlock()
	for _, record := range updatedQueued {
		if err := jm.store.Save(record); err != nil {
			return err
		}
	}
	return nil
}

// NotifyInterruptedAttachments updates restart notices after channels are ready.
func (jm *JobManager) NotifyInterruptedAttachments(ctx context.Context) {
	for _, record := range jm.interruptedAttachments {
		noticeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, updated := jm.trySyncProgressRecord(noticeCtx, record)
		cancel()
		if !updated {
			noticeCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
			if err := jm.sendText(noticeCtx, record.Channel, record.ChatID, "A job was interrupted by a restart. Please resend its attachments."); err != nil {
				logger.WarnCF("jobs", "Failed to send interrupted attachment notice", map[string]interface{}{"job_id": record.ID, "error": err.Error()})
			}
			cancel()
		}
	}
	jm.interruptedAttachments = nil
}

func rehydrateQueuedJob(record JobRecord) (LoopTarget, bus.InboundMessage, error) {
	target, err := targetFromJobRecord(record)
	if err != nil {
		return LoopTarget{}, bus.InboundMessage{}, err
	}
	msg := record.Message
	if strings.TrimSpace(msg.Channel) == "" || strings.TrimSpace(msg.ChatID) == "" || strings.TrimSpace(msg.SessionKey) == "" {
		return LoopTarget{}, bus.InboundMessage{}, fmt.Errorf("queued job payload missing original inbound context")
	}
	return target, msg, nil
}

func targetFromJobRecord(record JobRecord) (LoopTarget, error) {
	workspace := filepath.Clean(strings.TrimSpace(record.Workspace))
	if workspace == "" {
		return LoopTarget{}, fmt.Errorf("queued job missing workspace")
	}

	runtime := record.Runtime.normalized()
	if runtime == (RuntimeProfile{}) {
		runtime = parseRuntimeProfileKey(record.RuntimeKey)
	}

	return LoopTarget{Workspace: workspace, Runtime: runtime}.normalized(), nil
}

func parseRuntimeProfileKey(key string) RuntimeProfile {
	key = strings.TrimSpace(key)
	if key == "" {
		return RuntimeProfile{Mode: config.ModeCloud}
	}
	parts := strings.Split(key, "|")
	if strings.EqualFold(parts[0], config.ModePhi) {
		rt := RuntimeProfile{Mode: config.ModePhi}
		if len(parts) > 1 {
			rt.LocalBackend = parts[1]
		}
		if len(parts) > 2 {
			rt.LocalModel = parts[2]
		}
		if len(parts) > 3 {
			rt.LocalPreset = parts[3]
		}
		return rt.normalized()
	}
	return RuntimeProfile{Mode: parts[0]}.normalized()
}

func (jm *JobManager) handleControl(ctx context.Context, msg bus.InboundMessage, targetKey string, jobs []*activeJob, control parsedJobControl) error {
	if len(jobs) == 0 {
		return jm.sendOutbound(ctx, msg.Channel, msg.ChatID, formatControlNoJobsOutbound(control))
	}

	selected, err := selectTrackedJob(jobs, control.TargetID, msg.Metadata["reply_message_id"])
	if err != nil {
		if control.TargetID == "" && len(jobs) > 1 && control.Kind == jobControlStatus {
			return jm.sendOutbound(ctx, msg.Channel, msg.ChatID, formatControlJobListOutbound(control, snapshotActiveJobs(jobs), false))
		}
		if control.TargetID == "" && len(jobs) > 1 && control.Kind == jobControlCancel {
			return jm.sendOutbound(ctx, msg.Channel, msg.ChatID, formatControlJobListOutbound(control, snapshotActiveJobs(jobs), true))
		}
		return jm.sendOutbound(ctx, msg.Channel, msg.ChatID, formatControlErrorOutbound(control, err.Error()))
	}

	record := selected.snapshot()
	switch control.Kind {
	case jobControlStatus:
		if synced, ok := jm.refreshSelectedJobCard(ctx, selected); ok {
			_ = synced
			return nil
		}
		return jm.sendOutbound(ctx, msg.Channel, msg.ChatID, formatControlRecordOutbound(control, record, humanizePhase(record.Phase)))
	case jobControlCancel:
		if record.State == JobStateQueued {
			cancelled, cancelErr := jm.cancelQueuedJob(targetKey, record.ID)
			if cancelErr != nil {
				return jm.sendOutbound(ctx, msg.Channel, msg.ChatID, formatControlErrorOutbound(control, cancelErr.Error()))
			}
			if strings.TrimSpace(cancelled.StatusMessageID) != "" {
				return nil
			}
			return jm.sendOutbound(ctx, msg.Channel, msg.ChatID, formatControlRecordOutbound(control, cancelled, "Removed from main queue"))
		}
		cancelling := false
		record = selected.update(func(record *JobRecord) {
			if record.State == JobStateRunning {
				record.Phase = "cancelling"
				record.Detail = "Stopping now"
				record.Summary = record.Detail
				record.UpdatedAt = time.Now().UnixMilli()
				selected.cancel()
				cancelling = true
			}
		})
		if !cancelling {
			return jm.sendOutbound(ctx, msg.Channel, msg.ChatID, formatControlRecordOutbound(control, record, "Already finished"))
		}
		if synced, ok := jm.refreshSelectedJobCard(ctx, selected); ok {
			_ = synced
			return nil
		}
		return jm.sendOutbound(ctx, msg.Channel, msg.ChatID, formatControlRecordOutbound(control, record, "Stopping now"))
	case jobControlForce:
		if record.State != JobStateQueued {
			if synced, ok := jm.refreshSelectedJobCard(ctx, selected); ok {
				_ = synced
				return nil
			}
			return jm.sendOutbound(ctx, msg.Channel, msg.ChatID, formatControlRecordOutbound(control, record, "Already running"))
		}
		forced, forceErr := jm.forceQueuedJob(targetKey, record.ID)
		if forceErr != nil {
			return jm.sendOutbound(ctx, msg.Channel, msg.ChatID, formatControlErrorOutbound(control, forceErr.Error()))
		}
		if strings.TrimSpace(forced.StatusMessageID) != "" {
			return nil
		}
		return jm.sendOutbound(ctx, msg.Channel, msg.ChatID, formatControlRecordOutbound(control, forced, "Moved to the front of the main queue"))
	default:
		return nil
	}
}

func (jm *JobManager) refreshSelectedJobCard(ctx context.Context, job *activeJob) (JobRecord, bool) {
	if job == nil {
		return JobRecord{}, false
	}
	record := job.snapshot()
	if jm.progress == nil || strings.TrimSpace(record.StatusMessageID) == "" {
		return record, false
	}
	return record, jm.sendQueuedStatusAsync(ctx, job, JobRecord{}, 0)
}

func (jm *JobManager) cancelQueuedJob(targetKey, jobID string) (JobRecord, error) {
	jm.mu.Lock()
	activeSet := jm.active[targetKey]
	if activeSet == nil {
		jm.mu.Unlock()
		return JobRecord{}, fmt.Errorf("No queued sciClaw job matches %s.", jobID)
	}
	job := activeSet.removeQueuedWrite(jobID)
	for i, candidate := range activeSet.preparing {
		if candidate.snapshot().ID == jobID {
			job = candidate
			activeSet.preparing = append(activeSet.preparing[:i], activeSet.preparing[i+1:]...)
			break
		}
	}
	if job == nil {
		for _, candidate := range []*activeJob{activeSet.write, activeSet.btw} {
			if candidate != nil && candidate.snapshot().ID == jobID {
				candidate.cancel()
				jm.mu.Unlock()
				return candidate.snapshot(), nil
			}
		}
	}
	if job == nil {
		jm.mu.Unlock()
		return JobRecord{}, fmt.Errorf("No queued sciClaw job matches %s.", jobID)
	}
	record := job.update(func(record *JobRecord) {
		record.State = JobStateCancelled
		record.Phase = "cancelled"
		record.Detail = "Removed from main queue"
		record.Summary = record.Detail
		record.UpdatedAt = time.Now().UnixMilli()
		record.LastError = ""
	})
	job.cancel()
	updatedQueued := jm.reindexQueuedWritesLocked(activeSet)
	queuedJobs := append([]*activeJob(nil), activeSet.queuedWrites...)
	cancelErr := jm.store.Save(record)
	if cancelErr != nil {
		logger.ErrorCF("jobs", "Cancellation not persisted; job may run after restart", map[string]interface{}{"job_id": record.ID})
		if logger.GetLevel() > logger.ERROR {
			log.Print("jobs: Cancellation not persisted; job may run after restart")
		}
	}
	saveErr := cancelErr
	for _, queued := range updatedQueued {
		if err := jm.store.Save(queued); err != nil && saveErr == nil {
			saveErr = err
		}
	}
	if activeSet.empty() {
		delete(jm.active, targetKey)
	}
	jm.mu.Unlock()

	if saveErr != nil {
		if cancelErr != nil {
			return record, cancellationSaveError{cancelErr}
		}
		return record, saveErr
	}
	if !jm.sendQueuedStatusAsync(context.Background(), job, JobRecord{}, 0) {
		record.StatusMessageID = ""
	}
	for _, queued := range queuedJobs {
		jm.sendQueuedStatusAsync(context.Background(), queued, JobRecord{}, 0)
	}
	return record, nil
}

func (jm *JobManager) autoCancelQueuedJob(targetKey, jobID string) {
	record, err := jm.cancelQueuedJob(targetKey, jobID)
	if err == nil || record.ID == "" {
		return
	}
	var saveFailure cancellationSaveError
	if !errors.As(err, &saveFailure) {
		logger.WarnCF("jobs", "Failed to persist queue updates after automatic cancellation", map[string]interface{}{"job_id": jobID})
		return
	}
	select {
	case jm.cancelRetries <- struct{}{}:
		defer func() { <-jm.cancelRetries }()
	default:
		logger.ErrorCF("jobs", "Cancellation not persisted; job may run after restart (retry capacity exhausted)", map[string]interface{}{"job_id": jobID})
		return
	}
	ref := record.ShortID
	if ref == "" {
		ref = record.ID
	}
	notice := func(text string) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := jm.sendText(ctx, record.Channel, record.ChatID, text); err != nil {
			logger.WarnCF("jobs", "Failed to send cancellation persistence notice", map[string]interface{}{"job_id": jobID})
		}
	}
	notice(fmt.Sprintf("Cancellation of job %s could not be saved; this job may run after restart. Retrying.", ref))
	// ponytail: three retries; use durable retry coordination if outages commonly last longer.
	for attempt := 0; attempt < 3; attempt++ {
		time.Sleep(200 * time.Millisecond)
		if err = jm.store.Save(record); err == nil {
			notice(fmt.Sprintf("Cancellation of job %s was saved.", ref))
			return
		}
	}
	logger.ErrorCF("jobs", "Cancellation not persisted; job may run after restart (retries failed)", map[string]interface{}{"job_id": jobID})
	notice(fmt.Sprintf("Cancellation of job %s could not be saved; this job may run after restart. Please contact an administrator.", ref))
}

func (jm *JobManager) prepareJob(active *activeJob) {
	defer func() { <-jm.preparation }()
	ctx, target, msg := active.ctx, active.target, active.msg
	staged := msg
	staged.Metadata = map[string]string{"message_id": msg.Metadata["message_id"] + "-" + active.snapshot().ID}
	var rewrites map[string]string
	if jm.mediaRewrites != nil {
		rewrites = jm.mediaRewrites()
	}
	err := prepareInboundMedia(ctx, target.Workspace, &staged, rewrites)
	msg.Content, msg.Media = staged.Content, staged.Media
	if err != nil {
		msg.Media = nil
		msg.Content = appendInboundContent(msg.Content, "Attachments could not be staged locally.")
	}
	jm.mu.Lock()
	set := jm.active[target.key()]
	index := -1
	if set != nil {
		for i, candidate := range set.preparing {
			if candidate == active {
				index = i
				break
			}
		}
	}
	if index < 0 {
		jm.mu.Unlock()
		return
	}
	set.preparing = append(set.preparing[:index], set.preparing[index+1:]...)
	if ctx.Err() != nil {
		record := active.update(func(r *JobRecord) {
			r.State = JobStateCancelled
			r.Phase = "cancelled"
			r.Detail = "Cancelled"
			r.Summary = r.Detail
		})
		if err := jm.store.Save(record); err != nil {
			logger.WarnCF("jobs", "Failed to persist cancelled preparation", map[string]interface{}{"job_id": record.ID, "error": err.Error()})
		}
		if set.empty() {
			delete(jm.active, target.key())
		}
		jm.mu.Unlock()
		return
	}
	active.msg, active.prepared = msg, true
	record := active.update(func(r *JobRecord) { r.Phase = "queued"; r.Detail = "Queued"; r.Summary = r.Detail })
	if err := jm.store.Save(record); err != nil {
		active.update(func(r *JobRecord) {
			r.State = JobStateFailed
			r.Phase = "failed"
			r.Detail = "Failed to persist prepared job"
			r.Summary = r.Detail
		})
		if set.empty() {
			delete(jm.active, target.key())
		}
		jm.mu.Unlock()
		logger.WarnCF("jobs", "Failed to persist prepared job", map[string]interface{}{"job_id": record.ID, "error": err.Error()})
		noticeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := jm.sendText(noticeCtx, record.Channel, record.ChatID, "Could not save this job; please try again."); err != nil {
			logger.WarnCF("jobs", "Failed to send persistence failure notice", map[string]interface{}{"job_id": record.ID, "error": err.Error()})
		}
		return
	}
	start := true
	if record.Class == JobClassBTW {
		if !jm.allowBTWDuringWrite && set.write != nil {
			set.preparing = append(set.preparing, active)
			context.AfterFunc(ctx, func() { jm.autoCancelQueuedJob(target.key(), record.ID) })
			start = false
		} else {
			set.btw = active
		}
	} else if set.write == nil && (jm.allowBTWDuringWrite || set.btw == nil) {
		set.write = active
	} else {
		set.enqueueWrite(active)
		context.AfterFunc(ctx, func() { jm.autoCancelQueuedJob(target.key(), record.ID) })
		start = false
	}
	jm.mu.Unlock()
	if start {
		go jm.runJob(ctx, target, msg, active)
	}
}

func (jm *JobManager) forceQueuedJob(targetKey, jobID string) (JobRecord, error) {
	jm.mu.Lock()
	activeSet := jm.active[targetKey]
	if activeSet == nil {
		jm.mu.Unlock()
		return JobRecord{}, fmt.Errorf("No queued sciClaw job matches %s.", jobID)
	}
	for _, candidate := range activeSet.preparing {
		if candidate.snapshot().ID == jobID {
			prepared := candidate.prepared
			jm.mu.Unlock()
			if prepared {
				return JobRecord{}, fmt.Errorf("Job %s is waiting for another job and cannot be forced yet.", jobID)
			}
			return JobRecord{}, fmt.Errorf("Job %s is still preparing and cannot be forced yet.", jobID)
		}
	}
	var updatedQueued []JobRecord
	if activeSet.write != nil {
		writeRecord := activeSet.write.snapshot()
		if writeRecord.ID == jobID && writeRecord.State == JobStateQueued {
			record := writeRecord
			updatedQueued = jm.reindexQueuedWritesLocked(activeSet)
			queuedJobs := append([]*activeJob(nil), activeSet.queuedWrites...)
			jm.mu.Unlock()
			for _, queued := range updatedQueued {
				if err := jm.store.Save(queued); err != nil {
					return JobRecord{}, err
				}
			}
			for _, queued := range queuedJobs {
				jm.sendQueuedStatusAsync(context.Background(), queued, JobRecord{}, 0)
			}
			return record, nil
		}
	}
	job := activeSet.moveQueuedWriteToFront(jobID)
	if job == nil {
		jm.mu.Unlock()
		return JobRecord{}, fmt.Errorf("No queued sciClaw job matches %s.", jobID)
	}
	updatedQueued = jm.reindexQueuedWritesLocked(activeSet)
	queuedJobs := append([]*activeJob(nil), activeSet.queuedWrites...)
	record := job.snapshot()
	jm.mu.Unlock()

	if err := jm.store.Save(record); err != nil {
		return JobRecord{}, err
	}
	for _, queued := range updatedQueued {
		if err := jm.store.Save(queued); err != nil {
			return JobRecord{}, err
		}
	}
	for _, queued := range queuedJobs {
		if !jm.sendQueuedStatusAsync(context.Background(), queued, JobRecord{}, 0) && queued == job {
			record.StatusMessageID = ""
		}
	}
	return record, nil
}

func (jm *JobManager) syncTrackedJobProgress(ctx context.Context, job *activeJob) (JobRecord, error) {
	if job == nil {
		return JobRecord{}, nil
	}
	record := job.snapshot()
	if jm.progress == nil || strings.TrimSpace(record.StatusMessageID) == "" {
		return record, nil
	}
	message := formatProgressOutbound(record)
	message.Channel = record.Channel
	message.ChatID = record.ChatID
	messageID, err := jm.progress.SendOrEditProgress(ctx, record.Channel, record.ChatID, record.StatusMessageID, message)
	if err != nil {
		return record, err
	}
	if trimmed := strings.TrimSpace(messageID); trimmed != "" && trimmed != strings.TrimSpace(record.StatusMessageID) {
		record = job.update(func(existing *JobRecord) {
			if existing.StatusMessageID == record.StatusMessageID {
				existing.StatusMessageID = trimmed
			}
		})
		if err := jm.store.Save(record); err != nil {
			logger.WarnCF("jobs", "Failed to persist synced tracked progress record", map[string]interface{}{"job_id": record.ID, "error": err.Error()})
		}
	}
	return record, nil
}

func (jm *JobManager) syncTrackedJobsProgress(ctx context.Context, jobs ...*activeJob) []JobRecord {
	updated := make([]JobRecord, 0, len(jobs))
	for _, job := range jobs {
		if job == nil {
			continue
		}
		record, err := jm.syncTrackedJobProgress(ctx, job)
		if err != nil {
			snapshot := job.snapshot()
			logger.WarnCF("jobs", "Failed to sync tracked job progress", map[string]interface{}{"job_id": snapshot.ID, "error": err.Error()})
			updated = append(updated, snapshot)
			continue
		}
		updated = append(updated, record)
	}
	return updated
}

func (jm *JobManager) reindexQueuedWritesLocked(activeSet *targetActiveJobs) []JobRecord {
	if activeSet == nil || len(activeSet.queuedWrites) == 0 {
		return nil
	}
	leadShortID := ""
	if activeSet.write != nil {
		leadShortID = activeSet.write.snapshot().ShortID
	}
	updated := make([]JobRecord, 0, len(activeSet.queuedWrites))
	for idx, job := range activeSet.queuedWrites {
		if job == nil {
			continue
		}
		position := idx + 1
		detail := fmt.Sprintf("Queued in main lane · position %d", position)
		switch {
		case position == 1 && leadShortID != "":
			detail = fmt.Sprintf("Queued in main lane · next up after %s", leadShortID)
		case position == 1:
			detail = "Queued in main lane · next up"
		case leadShortID != "":
			detail = fmt.Sprintf("Queued in main lane · behind %s · position %d", leadShortID, position)
		}
		updated = append(updated, job.update(func(record *JobRecord) {
			record.State = JobStateQueued
			record.Phase = "queued"
			record.Detail = detail
			record.Summary = detail
			record.UpdatedAt = time.Now().UnixMilli()
		}))
	}
	return updated
}

func (jm *JobManager) syncProgressRecord(ctx context.Context, record JobRecord) JobRecord {
	record, _ = jm.trySyncProgressRecord(ctx, record)
	return record
}

func (jm *JobManager) trySyncProgressRecord(ctx context.Context, record JobRecord) (JobRecord, bool) {
	if jm.progress == nil || strings.TrimSpace(record.StatusMessageID) == "" {
		return record, false
	}
	message := formatProgressOutbound(record)
	message.Channel = record.Channel
	message.ChatID = record.ChatID
	messageID, err := jm.progress.SendOrEditProgress(ctx, record.Channel, record.ChatID, record.StatusMessageID, message)
	if err != nil {
		logger.WarnCF("jobs", "Failed to sync progress record", map[string]interface{}{"job_id": record.ID, "error": err.Error()})
		return record, false
	}
	if trimmed := strings.TrimSpace(messageID); trimmed != "" && trimmed != strings.TrimSpace(record.StatusMessageID) {
		record.StatusMessageID = trimmed
		if err := jm.store.Save(record); err != nil {
			logger.WarnCF("jobs", "Failed to persist synced progress record", map[string]interface{}{"job_id": record.ID, "error": err.Error()})
		}
	}
	return record, true
}

func (jm *JobManager) syncProgressRecords(ctx context.Context, records ...JobRecord) []JobRecord {
	updated := make([]JobRecord, 0, len(records))
	for _, record := range records {
		updated = append(updated, jm.syncProgressRecord(ctx, record))
	}
	return updated
}

func (jm *JobManager) runJob(ctx context.Context, target LoopTarget, msg bus.InboundMessage, active *activeJob) {
	defer active.cancel()
	targetKey := target.key()
	jobClass := active.snapshot().Class
	progress := jm.newProgressReporter(ctx, active)
	defer progress.finish()
	defer func() {
		var nextWrite, nextBTW *activeJob
		jm.mu.Lock()
		if activeSet := jm.active[targetKey]; activeSet != nil {
			if activeSet.jobForClass(jobClass) != active {
				jm.mu.Unlock()
				return
			}
			activeSet.clear(jobClass)
			if jobClass == JobClassBTW && !jm.allowBTWDuringWrite && activeSet.write == nil {
				nextWrite = activeSet.popQueuedWrite()
				if nextWrite != nil {
					activeSet.write = nextWrite
				}
			} else if jobClass == JobClassWrite {
				nextWrite = activeSet.popQueuedWrite()
				if nextWrite != nil {
					activeSet.write = nextWrite
				} else if !jm.allowBTWDuringWrite {
					for i, candidate := range activeSet.preparing {
						if candidate.prepared && candidate.snapshot().Class == JobClassBTW {
							nextBTW = candidate
							activeSet.preparing = append(activeSet.preparing[:i], activeSet.preparing[i+1:]...)
							activeSet.btw = candidate
							break
						}
					}
				}
			}
			if activeSet.empty() {
				delete(jm.active, targetKey)
			}
		}
		jm.mu.Unlock()
		if nextWrite != nil {
			go jm.runJob(nextWrite.ctx, nextWrite.target, nextWrite.msg, nextWrite)
		}
		if nextBTW != nil {
			go jm.runJob(nextBTW.ctx, nextBTW.target, nextBTW.msg, nextBTW)
		}
	}()

	progress.update(JobStateQueued, "queued", "Queued")

	select {
	case jm.semaphore <- struct{}{}:
		defer func() { <-jm.semaphore }()
	case <-ctx.Done():
		progress.complete(JobStateCancelled, "cancelled", "Cancelled before start", ctx.Err())
		return
	}
	if err := ctx.Err(); err != nil {
		progress.complete(JobStateCancelled, "cancelled", "Cancelled before start", err)
		return
	}

	progress.update(JobStateRunning, "preparing_context", "Preparing context")
	if !active.prepared {
		staged := msg
		staged.Metadata = map[string]string{"message_id": msg.Metadata["message_id"] + "-" + active.snapshot().ID}
		var rewrites map[string]string
		if jm.mediaRewrites != nil {
			rewrites = jm.mediaRewrites()
		}
		stageErr := prepareInboundMedia(ctx, target.Workspace, &staged, rewrites)
		if ctx.Err() != nil {
			progress.complete(JobStateCancelled, "cancelled", "Cancelled", ctx.Err())
			return
		}
		msg.Content, msg.Media = staged.Content, staged.Media
		if stageErr != nil {
			msg.Media = nil
			msg.Content = appendInboundContent(msg.Content, "Attachments could not be staged locally.")
		}
	}

	handler, err := jm.resolveForClass(jobClass, target)
	if err != nil {
		progress.complete(JobStateFailed, "failed", "Failed to start", err)
		return
	}

	_, runErr := handler.RunJob(ctx, msg, func(phase, detail string) {
		progress.throttledUpdate(phase, detail)
	})

	switch {
	case ctx.Err() != nil:
		progress.complete(JobStateCancelled, "cancelled", "Cancelled", ctx.Err())
	case runErr != nil:
		progress.complete(JobStateFailed, "failed", "Job failed", runErr)
	default:
		progress.complete(JobStateDone, "done", "Done. Reply posted below.", nil)
	}
}

func (jm *JobManager) sendText(ctx context.Context, channel, chatID, content string) error {
	return jm.sendOutbound(ctx, channel, chatID, bus.OutboundMessage{Content: content})
}

func (jm *JobManager) sendOutbound(ctx context.Context, channel, chatID string, msg bus.OutboundMessage) error {
	if jm.bus == nil {
		return nil
	}
	msg.Channel = channel
	msg.ChatID = chatID
	return jm.bus.PublishOutbound(ctx, msg)
}

func (jm *JobManager) sendQueuedStatus(ctx context.Context, active *activeJob, running JobRecord, position int) error {
	record := active.snapshot()
	initial := record
	if strings.TrimSpace(record.Detail) == "" && position > 0 {
		record.Detail = fmt.Sprintf("Queued in main lane · behind %s · position %d", running.ShortID, position)
	}
	message := formatProgressOutbound(record)
	message.Channel = record.Channel
	message.ChatID = record.ChatID
	if jm.progress != nil {
		messageID, err := jm.progress.SendOrEditProgress(ctx, record.Channel, record.ChatID, record.StatusMessageID, message)
		if err != nil {
			return err
		}
		if strings.TrimSpace(messageID) != "" && strings.TrimSpace(messageID) != record.StatusMessageID {
			record = active.update(func(existing *JobRecord) {
				if existing.StatusMessageID == record.StatusMessageID {
					existing.StatusMessageID = strings.TrimSpace(messageID)
				}
			})
			if err := jm.store.Save(record); err != nil {
				return err
			}
		}
		record = active.snapshot()
		if record.State != initial.State || record.Phase != initial.Phase || record.Detail != initial.Detail {
			_, err := jm.syncTrackedJobProgress(ctx, active)
			return err
		}
		return nil
	}
	if jm.bus == nil {
		return nil
	}
	return jm.bus.PublishOutbound(ctx, message)
}

func (jm *JobManager) nextJobID() (string, string) {
	n := atomic.AddUint64(&jm.counter, 1)
	return fmt.Sprintf("job-%d-%d", time.Now().UnixMilli(), n), formatShortJobRef(n)
}

func (jm *JobManager) restoreCounterFromStore() error {
	records, err := jm.store.All()
	if err != nil {
		return err
	}
	var maxCounter uint64
	for _, record := range records {
		if counter, ok := parseJobCounter(record); ok && counter > maxCounter {
			maxCounter = counter
		}
	}
	atomic.StoreUint64(&jm.counter, maxCounter)
	return nil
}

func parseJobCounter(record JobRecord) (uint64, bool) {
	if id := strings.TrimSpace(record.ID); id != "" {
		if idx := strings.LastIndex(id, "-"); idx >= 0 && idx < len(id)-1 {
			if counter, err := strconv.ParseUint(id[idx+1:], 10, 64); err == nil {
				return counter, true
			}
		}
	}
	if shortID := strings.TrimSpace(record.ShortID); shortID != "" {
		if counter, err := strconv.ParseUint(shortID, 36, 64); err == nil {
			return counter, true
		}
	}
	return 0, false
}

type progressReporter struct {
	manager   *JobManager
	ctx       context.Context
	active    *activeJob
	recordMu  sync.Mutex
	lastPhase string
	lastSent  time.Time
	done      chan struct{}
}

func (jm *JobManager) newProgressReporter(ctx context.Context, active *activeJob) *progressReporter {
	reporter := &progressReporter{
		manager: jm,
		ctx:     ctx,
		active:  active,
		done:    make(chan struct{}),
	}
	if jm.progressInterval > 0 {
		go reporter.runHeartbeat()
	}
	return reporter
}

func (p *progressReporter) finish() {
	close(p.done)
}

func (p *progressReporter) runHeartbeat() {
	ticker := time.NewTicker(p.manager.progressInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.heartbeat()
		}
	}
}

func (p *progressReporter) heartbeat() {
	p.recordMu.Lock()
	defer p.recordMu.Unlock()
	if p.active == nil {
		return
	}
	record := p.active.snapshot()
	if record.State != JobStateQueued && record.State != JobStateRunning {
		return
	}
	if !p.lastSent.IsZero() && time.Since(p.lastSent) < p.manager.progressInterval {
		return
	}
	p.sendLocked(record)
}

func (p *progressReporter) throttledUpdate(phase, detail string) {
	p.recordMu.Lock()
	defer p.recordMu.Unlock()
	now := time.Now()
	if phase == p.lastPhase && !p.lastSent.IsZero() && now.Sub(p.lastSent) < p.manager.progressInterval {
		return
	}
	p.updateLocked(JobStateRunning, phase, detail)
}

func (p *progressReporter) update(state JobState, phase, detail string) {
	p.recordMu.Lock()
	defer p.recordMu.Unlock()
	p.updateLocked(state, phase, detail)
}

func (p *progressReporter) updateLocked(state JobState, phase, detail string) {
	if p.active == nil {
		return
	}
	record := p.active.update(func(record *JobRecord) {
		record.State = state
		record.Phase = strings.TrimSpace(phase)
		record.Detail = strings.TrimSpace(detail)
		record.Summary = record.Detail
		record.UpdatedAt = time.Now().UnixMilli()
		if record.Phase == "" {
			record.Phase = string(state)
		}
	})
	p.lastPhase = record.Phase

	if err := p.manager.store.Save(record); err != nil {
		logger.WarnCF("jobs", "Failed to persist job progress", map[string]interface{}{"job_id": record.ID, "error": err.Error()})
	}
	p.sendLocked(record)
}

func (p *progressReporter) sendLocked(record JobRecord) {
	if p.manager.progress == nil {
		return
	}

	message := formatProgressOutbound(record)
	message.Channel = record.Channel
	message.ChatID = record.ChatID
	messageID, err := p.manager.progress.SendOrEditProgress(p.ctx, record.Channel, record.ChatID, record.StatusMessageID, message)
	if err != nil {
		logger.WarnCF("jobs", "Failed to send progress update", map[string]interface{}{"job_id": record.ID, "error": err.Error()})
		return
	}
	if strings.TrimSpace(messageID) != "" && strings.TrimSpace(messageID) != record.StatusMessageID {
		record = p.active.update(func(existing *JobRecord) {
			if existing.StatusMessageID == record.StatusMessageID {
				existing.StatusMessageID = strings.TrimSpace(messageID)
			}
		})
		if err := p.manager.store.Save(record); err != nil {
			logger.WarnCF("jobs", "Failed to persist progress message id", map[string]interface{}{"job_id": record.ID, "error": err.Error()})
		}
	}
	p.lastSent = time.Now()
}

func (p *progressReporter) complete(state JobState, phase, detail string, err error) {
	p.recordMu.Lock()
	defer p.recordMu.Unlock()
	if p.active == nil {
		return
	}
	record := p.active.update(func(record *JobRecord) {
		record.State = state
		record.Phase = strings.TrimSpace(phase)
		record.Detail = strings.TrimSpace(detail)
		record.Summary = record.Detail
		record.UpdatedAt = time.Now().UnixMilli()
		record.LastError = ""
		if err != nil {
			if userMessage, _, ok := agent.UserError(err); ok {
				record.LastError = utils.Truncate(compactJobLine(userMessage), 180)
			} else {
				record.LastError = "Job failed. Check the server logs for details."
			}
		}
	})
	if err != nil {
		logger.ErrorCF("jobs", "Job failed", map[string]interface{}{"job_id": record.ID, "phase": record.Phase, "error": err.Error()})
	}
	if saveErr := p.manager.store.Save(record); saveErr != nil {
		logger.WarnCF("jobs", "Failed to persist final job state", map[string]interface{}{"job_id": record.ID, "error": saveErr.Error()})
		record = p.active.update(func(r *JobRecord) {
			r.State = JobStateFailed
			r.Phase = "failed"
			r.Detail = "Could not save the final job status; check the result before retrying."
			r.Summary = r.Detail
			r.LastError = "Failed to persist final job status"
		})
		noticeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		if noticeErr := p.manager.sendText(noticeCtx, record.Channel, record.ChatID, "Could not save the final job status; check the result before retrying."); noticeErr != nil {
			logger.WarnCF("jobs", "Failed to send persistence failure notice", map[string]interface{}{"job_id": record.ID, "error": noticeErr.Error()})
		}
		cancel()
	}
	p.sendLocked(record)
}

type jobControl int

const (
	jobControlNone jobControl = iota
	jobControlStatus
	jobControlCancel
	jobControlForce
)

type parsedJobControl struct {
	Kind     jobControl
	TargetID string
	Directed bool
}

var discordMentionPattern = regexp.MustCompile(`<@!?\d+>`)
var jobControlShortIDPattern = regexp.MustCompile(`^[A-Z0-9]{5}$`)
var jobControlLongIDPattern = regexp.MustCompile(`^JOB-\d+-\d+$`)

func isJobControlTarget(value string) bool {
	target := strings.ToUpper(strings.TrimSpace(value))
	if target == "" {
		return false
	}
	return jobControlShortIDPattern.MatchString(target) || jobControlLongIDPattern.MatchString(target)
}

func parseJobControl(msg bus.InboundMessage) parsedJobControl {
	content := compactJobLine(discordMentionPattern.ReplaceAllString(strings.TrimSpace(msg.Content), ""))
	fields := strings.Fields(strings.ToLower(content))
	if len(fields) == 0 {
		return parsedJobControl{}
	}

	parseTarget := func(idx int) string {
		if len(fields) <= idx {
			return ""
		}
		return strings.ToUpper(strings.TrimSpace(fields[idx]))
	}

	control := parsedJobControl{
		Directed: isControlDirected(msg),
	}
	switch fields[0] {
	case "status", "progress":
		if len(fields) == 1 {
			control.Kind = jobControlStatus
			return control
		}
		if len(fields) == 2 && isJobControlTarget(fields[1]) {
			control.Kind = jobControlStatus
			control.TargetID = parseTarget(1)
			return control
		}
	case "job":
		if len(fields) == 2 && fields[1] == "status" {
			control.Kind = jobControlStatus
			return control
		}
		if len(fields) == 3 && fields[1] == "status" && isJobControlTarget(fields[2]) {
			control.Kind = jobControlStatus
			control.TargetID = parseTarget(2)
			return control
		}
	case "cancel", "stop":
		if len(fields) == 1 {
			control.Kind = jobControlCancel
			return control
		}
		if len(fields) == 2 && isJobControlTarget(fields[1]) {
			control.Kind = jobControlCancel
			control.TargetID = parseTarget(1)
			return control
		}
		if len(fields) == 3 && fields[1] == "job" && isJobControlTarget(fields[2]) {
			control.Kind = jobControlCancel
			control.TargetID = parseTarget(2)
			return control
		}
	case "force":
		if len(fields) == 1 {
			control.Kind = jobControlForce
			return control
		}
		if len(fields) == 2 && isJobControlTarget(fields[1]) {
			control.Kind = jobControlForce
			control.TargetID = parseTarget(1)
			return control
		}
		if len(fields) == 3 && fields[1] == "job" && isJobControlTarget(fields[2]) {
			control.Kind = jobControlForce
			control.TargetID = parseTarget(2)
			return control
		}
	}
	return parsedJobControl{Directed: control.Directed}
}

func isControlDirected(msg bus.InboundMessage) bool {
	meta := msg.Metadata
	if strings.EqualFold(strings.TrimSpace(meta["is_dm"]), "true") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(meta["has_direct_mention"]), "true") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(meta["reply_to_bot"]), "true")
}

func classifyJobRequest(msg bus.InboundMessage) parsedJobRequest {
	out := parsedJobRequest{
		Class:   JobClassWrite,
		Message: msg,
	}
	content := compactJobLine(discordMentionPattern.ReplaceAllString(strings.TrimSpace(msg.Content), ""))
	if content == "" {
		return out
	}
	lower := strings.ToLower(content)
	if lower == "/btw" || strings.HasPrefix(lower, "/btw ") {
		cleaned := strings.TrimSpace(content[len("/btw"):])
		out.Class = JobClassBTW
		out.Message.Content = cleaned
	}
	return out
}

func fallbackAskSummary(summary string) string {
	if strings.TrimSpace(summary) == "" {
		return "Working request"
	}
	return strings.TrimSpace(summary)
}

func formatDiscordRelativeTimestamp(startedAtMillis int64) string {
	if startedAtMillis <= 0 {
		return "just now"
	}
	return fmt.Sprintf("<t:%d:R>", startedAtMillis/1000)
}

var jobCardNow = time.Now

func formatJobElapsed(startedAtMillis int64) string {
	if startedAtMillis <= 0 {
		return "just now"
	}
	elapsed := jobCardNow().Sub(time.UnixMilli(startedAtMillis))
	if elapsed < 0 {
		elapsed = 0
	}
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds", int(elapsed/time.Second))
	case elapsed < time.Hour:
		minutes := int(elapsed / time.Minute)
		seconds := int((elapsed % time.Minute) / time.Second)
		return fmt.Sprintf("%dm %02ds", minutes, seconds)
	case elapsed < 24*time.Hour:
		hours := int(elapsed / time.Hour)
		minutes := int((elapsed % time.Hour) / time.Minute)
		return fmt.Sprintf("%dh %02dm", hours, minutes)
	default:
		days := int(elapsed / (24 * time.Hour))
		hours := int((elapsed % (24 * time.Hour)) / time.Hour)
		return fmt.Sprintf("%dd %02dh", days, hours)
	}
}

func formatJobCardHeader(record JobRecord) string {
	return fmt.Sprintf("**%s** · **%s** · %s · %s", record.ShortID, humanizePhase(record.Phase), jobLaneLabel(record.Class), formatDiscordRelativeTimestamp(record.StartedAt))
}

func formatJobCardDetail(record JobRecord) string {
	detail := strings.TrimSpace(record.Detail)
	if detail == "" || strings.EqualFold(detail, humanizePhase(record.Phase)) {
		return ""
	}
	return "_" + utils.Truncate(detail, 120) + "_"
}

func formatJobCardAsk(record JobRecord) string {
	return "> " + fallbackAskSummary(record.AskSummary)
}

func formatJobControlHint(record JobRecord) string {
	if record.State == JobStateQueued {
		return "Reply `status` · `force` to move next · `cancel` to drop from queue"
	}
	if record.Class == JobClassBTW {
		return "Reply `status` · `cancel` to clear /btw"
	}
	return "Reply `status` · `cancel` to stop it"
}

func summarizeJobAsk(content string) string {
	trimmed := compactJobLine(discordMentionPattern.ReplaceAllString(content, ""))
	if trimmed == "" {
		return "Working request"
	}
	if idx := strings.IndexAny(trimmed, ".!?"); idx >= 0 && idx < 120 {
		trimmed = strings.TrimSpace(trimmed[:idx+1])
	}
	return utils.Truncate(trimmed, 100)
}

func snapshotActiveJobs(active []*activeJob) []JobRecord {
	records := make([]JobRecord, 0, len(active))
	for _, job := range active {
		if job == nil {
			continue
		}
		records = append(records, job.snapshot())
	}
	return records
}

func selectTrackedJob(jobs []*activeJob, targetID, replyMessageID string) (*activeJob, error) {
	if len(jobs) == 0 {
		return nil, fmt.Errorf("sciClaw is not currently running a background job for this workspace.")
	}
	if targetID != "" {
		for _, job := range jobs {
			record := job.snapshot()
			if strings.EqualFold(record.ShortID, targetID) || strings.EqualFold(record.ID, targetID) {
				return job, nil
			}
		}
		return nil, fmt.Errorf("No sciClaw job matches %s.", targetID)
	}
	if strings.TrimSpace(replyMessageID) != "" {
		for _, job := range jobs {
			if job.snapshot().StatusMessageID == strings.TrimSpace(replyMessageID) {
				return job, nil
			}
		}
	}
	if len(jobs) == 1 {
		return jobs[0], nil
	}
	return nil, fmt.Errorf("Multiple sciClaw jobs are active here.")
}

func formatProgressMessage(record JobRecord) string {
	lines := []string{formatJobCardHeader(record), formatJobCardAsk(record)}
	if detail := formatJobCardDetail(record); detail != "" {
		lines = append(lines, detail)
	}
	switch record.State {
	case JobStateQueued, JobStateRunning:
		lines = append(lines, "", formatJobControlHint(record))
	case JobStateDone:
		lines = append(lines, "", "Done. Reply below.")
	case JobStateCancelled:
		lines = append(lines, "", "This job was cancelled.")
	case JobStateFailed, JobStateInterrupted:
		if strings.TrimSpace(record.LastError) != "" {
			lines = append(lines, "", "Last error: "+strings.TrimSpace(record.LastError))
		}
	}
	return strings.Join(lines, "\n")
}

func formatProgressOutbound(record JobRecord) bus.OutboundMessage {
	return bus.OutboundMessage{
		Content: formatProgressMessage(record),
		Embeds:  []bus.OutboundEmbed{formatProgressEmbed(record)},
	}
}

func formatControlNoJobsOutbound(control parsedJobControl) bus.OutboundMessage {
	status := "No running or queued jobs in this workspace."
	if control.Kind == jobControlCancel {
		status = "No running or queued job can be cancelled here."
	}
	return formatControlOutbound(
		control,
		"sciClaw · jobs",
		fmt.Sprintf("Treated this as `%s`.", controlCommandLabel(control.Kind)),
		[]bus.OutboundEmbedField{
			{Name: "Now", Value: status, Inline: false},
			{Name: "Try", Value: "`@sciclaw-app <request>` · `/btw <question>`", Inline: false},
		},
		"When jobs exist: `status <job-id>` · `cancel <job-id>` · `force <job-id>`",
		0x64748B,
		compactJobLine(status+" Start a normal request or `/btw <question>`."),
	)
}

func formatControlJobListOutbound(control parsedJobControl, records []JobRecord, requireTarget bool) bus.OutboundMessage {
	description := fmt.Sprintf("Treated this as `%s`.", controlCommandLabel(control.Kind))
	footer := "Reply on a job card: `status` · `cancel`"
	if hasQueuedMainLane(records) {
		footer += " · `force`"
	}
	content := "Multiple sciClaw jobs are active in this workspace."
	if requireTarget {
		description = "Treated this as `cancel`. Pick one job."
		footer = "Reply `cancel` on a card, or direct `cancel <job-id>`."
		content = "Multiple sciClaw jobs are active. Choose a job to cancel."
	} else if hasQueuedMainLane(records) {
		footer = "Reply on a job card: `status` · `cancel` · `force`"
	}
	fields := []bus.OutboundEmbedField{
		{Name: "Jobs", Value: formatControlJobListValue(records), Inline: false},
	}
	if requireTarget {
		fields = append(fields, bus.OutboundEmbedField{Name: "Next", Value: "`cancel <job-id>` · reply `cancel` on a card", Inline: false})
	}
	return formatControlOutbound(control, "sciClaw · jobs", description, fields, footer, 0x3B82F6, content)
}

func formatControlErrorOutbound(control parsedJobControl, message string) bus.OutboundMessage {
	message = compactJobLine(message)
	return formatControlOutbound(
		control,
		"sciClaw · jobs",
		fmt.Sprintf("Treated this as `%s`.", controlCommandLabel(control.Kind)),
		[]bus.OutboundEmbedField{{Name: "Problem", Value: utils.Truncate(message, 240), Inline: false}},
		"`status <job-id>` · `cancel <job-id>` · `force <job-id>`",
		0xEF4444,
		message,
	)
}

func formatControlRecordOutbound(control parsedJobControl, record JobRecord, note string) bus.OutboundMessage {
	footer := "Reply on this card: `status` · `cancel`"
	if record.Class == JobClassWrite && record.State == JobStateQueued {
		footer += " · `force`"
	}
	fields := []bus.OutboundEmbedField{
		{Name: "Lane", Value: jobLaneLabel(record.Class), Inline: true},
		{Name: "Status", Value: humanizePhase(record.Phase), Inline: true},
		{Name: "Started", Value: formatDiscordRelativeTimestamp(record.StartedAt), Inline: true},
	}
	if trimmed := strings.TrimSpace(note); trimmed != "" {
		fields = append(fields, bus.OutboundEmbedField{Name: "Now", Value: utils.Truncate(trimmed, 240), Inline: false})
	}
	if detail := formatJobCardDetail(record); detail != "" && !strings.EqualFold(strings.TrimSpace(detail), strings.TrimSpace(note)) {
		fields = append(fields, bus.OutboundEmbedField{Name: "Detail", Value: utils.Truncate(detail, 240), Inline: false})
	}
	description := fmt.Sprintf("Treated this as `%s`.\n\n%s", controlCommandLabel(control.Kind), formatJobCardAsk(record))
	content := fmt.Sprintf("%s %s · %s.", record.ShortID, humanizePhase(record.Phase), fallbackAskSummary(record.AskSummary))
	return formatControlOutbound(control, fmt.Sprintf("sciClaw · %s", record.ShortID), description, fields, footer, jobStateColor(record.State), content)
}

func formatProgressEmbed(record JobRecord) bus.OutboundEmbed {
	embed := bus.OutboundEmbed{
		Title:         fmt.Sprintf("sciClaw · %s", record.ShortID),
		Description:   formatJobCardAsk(record),
		Color:         jobStateColor(record.State),
		TimestampUnix: record.UpdatedAt / 1000,
	}

	embed.Fields = append(embed.Fields,
		bus.OutboundEmbedField{Name: "Lane", Value: jobLaneLabel(record.Class), Inline: true},
		bus.OutboundEmbedField{Name: "Status", Value: humanizePhase(record.Phase), Inline: true},
		bus.OutboundEmbedField{Name: "Started", Value: formatDiscordRelativeTimestamp(record.StartedAt), Inline: true},
		bus.OutboundEmbedField{Name: "Elapsed", Value: formatJobElapsed(record.StartedAt), Inline: true},
	)

	if detail := strings.TrimSpace(record.Detail); detail != "" && !strings.EqualFold(detail, humanizePhase(record.Phase)) {
		embed.Fields = append(embed.Fields, bus.OutboundEmbedField{
			Name:   "Detail",
			Value:  utils.Truncate(detail, 240),
			Inline: false,
		})
	}

	switch record.State {
	case JobStateQueued, JobStateRunning:
		embed.Footer = strings.TrimPrefix(formatJobControlHint(record), "Controls: ")
	case JobStateDone:
		embed.Footer = "Done. Reply below."
	case JobStateCancelled:
		embed.Footer = "Cancelled."
	case JobStateFailed, JobStateInterrupted:
		if strings.TrimSpace(record.LastError) != "" {
			embed.Fields = append(embed.Fields, bus.OutboundEmbedField{
				Name:   "Last error",
				Value:  utils.Truncate(strings.TrimSpace(record.LastError), 240),
				Inline: false,
			})
		}
		embed.Footer = strings.ToUpper(string(record.State[:1])) + string(record.State[1:]) + "."
	}

	return embed
}

func jobStateColor(state JobState) int {
	switch state {
	case JobStateQueued:
		return 0x3B82F6
	case JobStateRunning:
		return 0xF59E0B
	case JobStateDone:
		return 0x10B981
	case JobStateCancelled:
		return 0x6B7280
	case JobStateFailed, JobStateInterrupted:
		return 0xEF4444
	default:
		return 0x64748B
	}
}

func formatActiveJobStatus(record JobRecord, includeControlHint bool) string {
	lines := []string{formatJobCardHeader(record), formatJobCardAsk(record)}
	if detail := formatJobCardDetail(record); detail != "" {
		lines = append(lines, detail)
	}
	if includeControlHint {
		lines = append(lines, "", formatJobControlHint(record))
	}
	return strings.Join(lines, "\n")
}

func formatControlOutbound(control parsedJobControl, title, description string, fields []bus.OutboundEmbedField, footer string, color int, content string) bus.OutboundMessage {
	embed := bus.OutboundEmbed{
		Title:       title,
		Description: description,
		Color:       color,
		Footer:      footer,
		Fields:      fields,
	}
	return bus.OutboundMessage{
		Content: compactJobLine(content),
		Embeds:  []bus.OutboundEmbed{embed},
	}
}

func controlCommandLabel(kind jobControl) string {
	switch kind {
	case jobControlCancel:
		return "cancel"
	case jobControlForce:
		return "force"
	default:
		return "status"
	}
}

func formatControlJobListValue(records []JobRecord) string {
	lines := make([]string, 0, len(records))
	for _, record := range records {
		lines = append(lines, fmt.Sprintf("**%s** · %s · %s · %s\n%s", record.ShortID, jobLaneLabel(record.Class), humanizePhase(record.Phase), formatDiscordRelativeTimestamp(record.StartedAt), fallbackAskSummary(record.AskSummary)))
	}
	return utils.Truncate(strings.Join(lines, "\n\n"), 1000)
}

func formatActiveJobList(records []JobRecord) string {
	lines := []string{"**sciClaw jobs in this workspace**", ""}
	for _, record := range records {
		lines = append(lines, fmt.Sprintf("- **%s** · %s · %s · %s", record.ShortID, jobLaneLabel(record.Class), humanizePhase(record.Phase), formatDiscordRelativeTimestamp(record.StartedAt)))
		lines = append(lines, fmt.Sprintf("  %s", fallbackAskSummary(record.AskSummary)))
		if detail := formatJobCardDetail(record); detail != "" {
			lines = append(lines, fmt.Sprintf("  %s", detail))
		}
	}
	lines = append(lines, "", "Reply on a job card with `status` or `cancel`, or direct `status <job-id>` at sciClaw.")
	if hasQueuedMainLane(records) {
		lines = append(lines, "Queued main-lane jobs can also use `force <job-id>` when directed at sciClaw.")
	}
	return strings.Join(lines, "\n")
}

func formatCancelRequiresJobID(records []JobRecord) string {
	lines := []string{"**sciClaw jobs in this workspace**", "Reply `cancel` on a job card, or direct `cancel <job-id>` at sciClaw.", ""}
	for _, record := range records {
		lines = append(lines, fmt.Sprintf("- **%s** · %s · %s", record.ShortID, jobLaneLabel(record.Class), fallbackAskSummary(record.AskSummary)))
	}
	if hasQueuedMainLane(records) {
		lines = append(lines, "", "Queued main-lane jobs can also use directed `force <job-id>` to move next.")
	}
	return strings.Join(lines, "\n")
}

func formatBusyMessage(records []JobRecord, requestedClass JobClass) string {
	if len(records) == 0 {
		return "sciClaw is already busy with this workspace."
	}
	lines := []string{formatActiveJobStatus(records[0], false)}
	if len(records) > 1 {
		lines = []string{formatActiveJobList(records)}
	}
	switch requestedClass {
	case JobClassBTW:
		lines = append(lines, "", "The /btw lane is already in use in this workspace.")
		lines = append(lines, "Reply on that job card with `status` or `cancel`, or direct `status <job-id>` at sciClaw.")
		lines = append(lines, "If this can wait, resend it without `/btw` to place it in the main queue.")
	default:
		lines = append(lines, "", "Main-lane work is queued in this workspace.")
		if hasQueuedMainLane(records) {
			lines = append(lines, "Queued main-lane jobs can use directed `force <job-id>` to move next.")
		}
		lines = append(lines, "Reply on a job card with `status` or `cancel`, or direct `status <job-id>` at sciClaw.")
	}
	return strings.Join(lines, "\n")
}

func jobLaneLabel(class JobClass) string {
	switch class {
	case JobClassBTW:
		return "/btw lane"
	default:
		return "main lane"
	}
}

func hasQueuedMainLane(records []JobRecord) bool {
	for _, record := range records {
		if record.Class == JobClassWrite && record.State == JobStateQueued {
			return true
		}
	}
	return false
}

func (jm *JobManager) sendBusy(ctx context.Context, msg bus.InboundMessage, records []JobRecord, requestedClass JobClass) error {
	return jm.sendText(ctx, msg.Channel, msg.ChatID, formatBusyMessage(records, requestedClass))
}

func humanizePhase(phase string) string {
	switch strings.TrimSpace(phase) {
	case "queued":
		return "Queued"
	case "preparing_context":
		return "Preparing context"
	case "thinking":
		return "Thinking"
	case "using_tools":
		return "Using tools"
	case "saving":
		return "Saving"
	case "replying":
		return "Replying"
	case "cancelled":
		return "Cancelled"
	case "failed":
		return "Failed"
	case "done":
		return "Done"
	case "interrupted":
		return "Interrupted"
	default:
		if phase == "" {
			return "Working"
		}
		normalized := strings.ReplaceAll(phase, "_", " ")
		return strings.ToUpper(normalized[:1]) + normalized[1:]
	}
}

func compactJobLine(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func newJobStore(path string) (*jobStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	s := &jobStore{
		path: path,
		jobs: map[string]JobRecord{},
	}
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func (s *jobStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var payload struct {
		Jobs []JobRecord `json:"jobs"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	for _, job := range payload.Jobs {
		s.jobs[job.ID] = job
	}
	return nil
}

func (s *jobStore) Save(record JobRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.jobs[record.ID]
	if exists && current.revision > record.revision {
		return nil
	}
	s.jobs[record.ID] = record
	if err := s.writeLocked(); err != nil {
		if exists {
			s.jobs[record.ID] = current
		} else {
			delete(s.jobs, record.ID)
		}
		return err
	}
	return nil
}

func (s *jobStore) All() ([]JobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JobRecord, 0, len(s.jobs))
	for _, record := range s.jobs {
		out = append(out, record)
	}
	return out, nil
}

func (s *jobStore) writeLocked() error {
	payload := struct {
		Jobs []JobRecord `json:"jobs"`
	}{Jobs: make([]JobRecord, 0, len(s.jobs))}
	for _, record := range s.jobs {
		payload.Jobs = append(payload.Jobs, record)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
