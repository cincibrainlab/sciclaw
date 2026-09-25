package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

type testFile struct {
	mu         sync.Mutex
	buffer     bytes.Buffer
	writes     int
	syncs      int
	closes     int
	maxWrite   int
	writeErr   error
	syncErr    error
	closeErr   error
	writeStart chan struct{}
	writeGate  chan struct{}
	startOnce  sync.Once
}

type lockedBuffer struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	writeErr error
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.writeErr != nil {
		return 0, buffer.writeErr
	}
	return buffer.buffer.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func (file *testFile) Write(data []byte) (int, error) {
	file.startOnce.Do(func() {
		if file.writeStart != nil {
			close(file.writeStart)
		}
	})
	if file.writeGate != nil {
		<-file.writeGate
	}
	file.mu.Lock()
	defer file.mu.Unlock()
	file.writes++
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	if file.maxWrite > 0 && len(data) > file.maxWrite {
		data = data[:file.maxWrite]
	}
	return file.buffer.Write(data)
}

func (file *testFile) Sync() error {
	file.mu.Lock()
	defer file.mu.Unlock()
	file.syncs++
	return file.syncErr
}

func (file *testFile) Close() error {
	file.mu.Lock()
	defer file.mu.Unlock()
	file.closes++
	return file.closeErr
}

func (file *testFile) snapshot() (string, int, int, int) {
	file.mu.Lock()
	defer file.mu.Unlock()
	return file.buffer.String(), file.writes, file.syncs, file.closes
}

func installTestLogger(t *testing.T, queueSize int, interval time.Duration, batchBytes int, file *testFile) (*Logger, *lockedBuffer) {
	return installTestLoggerWithLimit(t, queueSize, interval, batchBytes, defaultBufferedBytes, file, &lockedBuffer{})
}

func installTestLoggerWithLimit(t *testing.T, queueSize int, interval time.Duration, batchBytes, maxBufferedBytes int, file *testFile, stderr *lockedBuffer) (*Logger, *lockedBuffer) {
	t.Helper()
	previousLogger := logger
	previousLevel := GetLevel()
	previousOutput := log.Writer()
	previousPrefix := log.Prefix()
	previousFlags := log.Flags()
	log.SetOutput(stderr)
	log.SetPrefix("")
	log.SetFlags(log.LstdFlags)
	testLogger := newLogger(queueSize, interval, batchBytes, maxBufferedBytes)
	logger = testLogger
	SetLevel(DEBUG)
	if err := testLogger.enable(file); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testLogger.close()
		logger = previousLogger
		SetLevel(previousLevel)
		log.SetOutput(previousOutput)
		log.SetPrefix(previousPrefix)
		log.SetFlags(previousFlags)
	})
	return testLogger, stderr
}

func TestBufferedFormatAndMessageEquivalence(t *testing.T) {
	file := &testFile{maxWrite: 7}
	testLogger, stderr := installTestLogger(t, 8, time.Hour, 1<<20, file)
	fields := map[string]interface{}{"count": 42, "user_id": "12345"}
	InfoCF("telegram", "Telegram message", fields)
	if err := testLogger.flush(); err != nil {
		t.Fatal(err)
	}

	fileOutput, writes, _, _ := file.snapshot()
	if writes < 2 {
		t.Fatalf("partial writer calls = %d, want multiple", writes)
	}
	var entry LogEntry
	if err := json.Unmarshal(bytes.TrimSuffix([]byte(fileOutput), []byte("\n")), &entry); err != nil {
		t.Fatalf("parse JSONL: %v", err)
	}
	if entry.Level != "INFO" || entry.Component != "telegram" || entry.Message != "Telegram message" || entry.Caller == "" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.Fields["count"] != float64(42) || entry.Fields["user_id"] != "12345" {
		t.Fatalf("unexpected fields: %v", entry.Fields)
	}
	stderrOutput := stderr.String()
	wantPrefix := " [" + entry.Timestamp + "] [INFO] telegram: Telegram message {"
	if len(stderrOutput) < 20 || stderrOutput[19] != ' ' || !strings.Contains(stderrOutput[19:], wantPrefix) || !strings.HasSuffix(stderrOutput, "}\n") || !strings.Contains(stderrOutput, "count=42") || !strings.Contains(stderrOutput, "user_id=12345") {
		t.Fatalf("unexpected stderr format: %q", stderrOutput)
	}
}

func TestTimerFlushKeepsFileLive(t *testing.T) {
	file := &testFile{}
	_, _ = installTestLogger(t, 8, 20*time.Millisecond, 1<<20, file)
	Info("timer flush")
	waitFor(t, func() bool {
		output, _, _, _ := file.snapshot()
		return strings.Contains(output, "timer flush")
	})
}

func TestByteThresholdFlush(t *testing.T) {
	file := &testFile{}
	_, stderr := installTestLogger(t, 8, time.Hour, 1, file)
	Info("threshold flush")
	waitFor(t, func() bool {
		output, _, _, _ := file.snapshot()
		return strings.Contains(output, "threshold flush") && strings.Contains(stderr.String(), "threshold flush")
	})
}

func TestConcurrentOrderingAndNoLoss(t *testing.T) {
	file := &testFile{}
	testLogger, stderr := installTestLogger(t, 32, time.Hour, 1<<20, file)
	const goroutines = 16
	const perGoroutine = 100
	var waitGroup sync.WaitGroup
	for worker := 0; worker < goroutines; worker++ {
		waitGroup.Add(1)
		go func(worker int) {
			defer waitGroup.Done()
			for event := 0; event < perGoroutine; event++ {
				Info(fmt.Sprintf("event-%02d-%03d", worker, event))
			}
		}(worker)
	}
	waitGroup.Wait()
	if err := testLogger.flush(); err != nil {
		t.Fatal(err)
	}

	fileMessages := jsonlMessages(t, file)
	stderrMessages := textMessages(t, stderr.String())
	if len(fileMessages) != goroutines*perGoroutine || len(stderrMessages) != len(fileMessages) {
		t.Fatalf("message counts file=%d stderr=%d, want %d", len(fileMessages), len(stderrMessages), goroutines*perGoroutine)
	}
	for index := range fileMessages {
		if fileMessages[index] != stderrMessages[index] {
			t.Fatalf("sink order differs at %d: file=%q stderr=%q", index, fileMessages[index], stderrMessages[index])
		}
	}
}

func TestGracefulCloseDrainsSyncsClosesAndLeavesStderrAlone(t *testing.T) {
	file := &testFile{}
	testLogger, stderr := installTestLogger(t, 8, time.Hour, 1<<20, file)
	Info("drain me")
	disabled, err := testLogger.close()
	if err != nil || !disabled {
		t.Fatalf("close = (%v, %v), want (true, nil)", disabled, err)
	}
	output, _, syncs, closes := file.snapshot()
	if !strings.Contains(output, "drain me") || syncs != 1 || closes != 1 {
		t.Fatalf("output=%q syncs=%d closes=%d", output, syncs, closes)
	}
	log.Println("restored")
	if !strings.Contains(stderr.String(), "restored") {
		t.Fatalf("stderr was not restored: %q", stderr.String())
	}
	if disabled, err := testLogger.close(); err != nil || disabled {
		t.Fatalf("second close = (%v, %v), want (false, nil)", disabled, err)
	}
}

func TestConcurrentCloseWaitsForDrain(t *testing.T) {
	file := &testFile{writeStart: make(chan struct{}), writeGate: make(chan struct{})}
	testLogger, _ := installTestLogger(t, 8, time.Hour, 1, file)
	Info("close race")
	select {
	case <-file.writeStart:
	case <-time.After(time.Second):
		t.Fatal("worker did not reach sink")
	}

	results := make(chan bool, 2)
	for range 2 {
		go func() {
			disabled, _ := testLogger.close()
			results <- disabled
		}()
	}
	select {
	case <-results:
		t.Fatal("close returned before drain completed")
	case <-time.After(30 * time.Millisecond):
	}
	close(file.writeGate)
	first := <-results
	second := <-results
	if first == second {
		t.Fatalf("close ownership results = %v, %v; want one lifecycle owner", first, second)
	}
	_, _, syncs, closes := file.snapshot()
	if syncs != 1 || closes != 1 {
		t.Fatalf("syncs=%d closes=%d, want one each", syncs, closes)
	}
}

func TestFlushRacingCloseReturns(t *testing.T) {
	file := &testFile{writeStart: make(chan struct{}), writeGate: make(chan struct{})}
	testLogger, _ := installTestLogger(t, 8, time.Hour, 1, file)
	Info("flush race")
	select {
	case <-file.writeStart:
	case <-time.After(time.Second):
		t.Fatal("worker did not reach sink")
	}

	flushed := make(chan error, 1)
	go func() { flushed <- testLogger.flush() }()
	closed := make(chan struct{})
	go func() {
		_, _ = testLogger.close()
		close(closed)
	}()
	close(file.writeGate)
	select {
	case <-flushed:
	case <-time.After(time.Second):
		t.Fatal("flush racing close did not return")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close racing flush did not return")
	}
}

func TestByteBoundedBackpressureIncludesRetainedBatch(t *testing.T) {
	file := &testFile{writeStart: make(chan struct{}), writeGate: make(chan struct{})}
	first := strings.Repeat("a", 128)
	second := strings.Repeat("b", 128)
	maxBytes := formattedSize(first) * 2
	testLogger, _ := installTestLoggerWithLimit(t, 8, time.Hour, 1, maxBytes, file, &lockedBuffer{})
	Info(first)
	select {
	case <-file.writeStart:
	case <-time.After(time.Second):
		t.Fatal("worker did not reach sink")
	}
	testLogger.mu.Lock()
	inFlight := testLogger.inFlightBytes
	testLogger.mu.Unlock()
	if inFlight > maxBytes || inFlight*2 <= maxBytes {
		t.Fatalf("first admission bytes=%d max=%d; test requires one normal record but not two", inFlight, maxBytes)
	}
	callerDone := make(chan struct{})
	go func() {
		Info(second)
		close(callerDone)
	}()
	select {
	case <-callerDone:
		t.Fatal("caller did not apply bounded backpressure")
	case <-time.After(30 * time.Millisecond):
	}
	close(file.writeGate)
	select {
	case <-callerDone:
	case <-time.After(time.Second):
		t.Fatal("caller remained blocked after worker resumed")
	}
}

func TestCloseUnblocksByteAdmissionWhileFileIsBlocked(t *testing.T) {
	file := &testFile{writeStart: make(chan struct{}), writeGate: make(chan struct{})}
	first := strings.Repeat("a", 128)
	maxBytes := formattedSize(first) * 2
	testLogger, _ := installTestLoggerWithLimit(t, 8, time.Hour, 1, maxBytes, file, &lockedBuffer{})
	Info(first)
	select {
	case <-file.writeStart:
	case <-time.After(time.Second):
		t.Fatal("worker did not reach blocked file")
	}

	producerDone := make(chan struct{})
	go func() {
		Info(strings.Repeat("b", 128))
		close(producerDone)
	}()
	select {
	case <-producerDone:
		t.Fatal("producer did not block at byte admission")
	case <-time.After(30 * time.Millisecond):
	}

	closeDone := make(chan struct{})
	go func() {
		_, _ = testLogger.close()
		close(closeDone)
	}()
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not unblock blocked producer")
	}
	select {
	case <-closeDone:
		t.Fatal("shutdown finished while file remained blocked")
	case <-time.After(30 * time.Millisecond):
	}
	close(file.writeGate)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not drain accepted record")
	}
}

func TestOversizedRecordIsSoleAdmissionAndBufferIsReleased(t *testing.T) {
	file := &testFile{writeStart: make(chan struct{}), writeGate: make(chan struct{})}
	oversized := strings.Repeat("x", 4096)
	testLogger, _ := installTestLoggerWithLimit(t, 8, time.Hour, 128, 64, file, &lockedBuffer{})
	Info(oversized)
	select {
	case <-file.writeStart:
	case <-time.After(time.Second):
		t.Fatal("oversized record did not flush")
	}
	done := make(chan struct{})
	go func() {
		Info("after oversized")
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("second record admitted beside oversized record")
	case <-time.After(30 * time.Millisecond):
	}
	close(file.writeGate)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("oversized admission was not released")
	}
	if err := testLogger.flushBuffered(false); err != nil {
		t.Fatal(err)
	}
	testLogger.mu.Lock()
	inFlight := testLogger.inFlightBytes
	testLogger.mu.Unlock()
	if inFlight != 0 {
		t.Fatalf("in-flight bytes = %d, want 0", inFlight)
	}

	var buffer bytes.Buffer
	_, _ = buffer.Write(make([]byte, 4096))
	resetBuffer(&buffer, 128)
	if buffer.Cap() != 0 {
		t.Fatalf("oversized buffer capacity retained: %d", buffer.Cap())
	}
}

func TestFatalSynchronouslyFlushesBeforeInjectedExit(t *testing.T) {
	file := &testFile{}
	testLogger, _ := installTestLogger(t, 8, time.Hour, 1<<20, file)
	exitCode := make(chan int, 1)
	testLogger.exit = func(code int) {
		output, _, syncs, _ := file.snapshot()
		if !strings.Contains(output, "fatal message") || syncs == 0 {
			t.Errorf("fatal exit before flush: output=%q syncs=%d", output, syncs)
		}
		exitCode <- code
	}
	Fatal("fatal message")
	if code := <-exitCode; code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestFileErrorDoesNotLoseStderr(t *testing.T) {
	file := &testFile{writeErr: errors.New("disk failed")}
	testLogger, stderr := installTestLogger(t, 8, time.Hour, 1, file)
	Error("survives file failure")
	if err := testLogger.flush(); err == nil || !strings.Contains(err.Error(), "disk failed") {
		t.Fatalf("flush error = %v", err)
	}
	if !strings.Contains(stderr.String(), "survives file failure") {
		t.Fatalf("stderr output = %q", stderr.String())
	}
	if disabled, err := testLogger.close(); !disabled || err == nil {
		t.Fatalf("close = (%v, %v), want recorded error", disabled, err)
	}
}

func TestStderrWrittenBeforeBlockedFile(t *testing.T) {
	file := &testFile{writeStart: make(chan struct{}), writeGate: make(chan struct{})}
	_, stderr := installTestLogger(t, 8, time.Hour, 1, file)
	Info("visible before blocked file")
	select {
	case <-file.writeStart:
	case <-time.After(time.Second):
		t.Fatal("worker did not reach blocked file")
	}
	if !strings.Contains(stderr.String(), "visible before blocked file") {
		t.Fatalf("stderr not visible before file unblocked: %q", stderr.String())
	}
	close(file.writeGate)
}

func TestStderrErrorDoesNotPreventFileWrite(t *testing.T) {
	file := &testFile{}
	stderr := &lockedBuffer{writeErr: errors.New("stderr failed")}
	testLogger, _ := installTestLoggerWithLimit(t, 8, time.Hour, 1, defaultBufferedBytes, file, stderr)
	Error("survives stderr failure")
	if err := testLogger.flush(); err == nil || !strings.Contains(err.Error(), "stderr failed") {
		t.Fatalf("flush error = %v", err)
	}
	fileOutput, _, _, _ := file.snapshot()
	if !strings.Contains(fileOutput, "survives stderr failure") {
		t.Fatalf("file output = %q", fileOutput)
	}
}

func TestSyncAndCloseErrorsDoNotPoisonReenable(t *testing.T) {
	firstFile := &testFile{syncErr: errors.New("sync failed"), closeErr: errors.New("close failed")}
	testLogger, _ := installTestLogger(t, 8, time.Hour, 1<<20, firstFile)
	Info("first lifecycle")
	if disabled, err := testLogger.close(); !disabled || err == nil || !strings.Contains(err.Error(), "sync failed") || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("first close = (%v, %v)", disabled, err)
	}

	secondFile := &testFile{}
	if err := testLogger.enable(secondFile); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	Info("second lifecycle")
	if err := testLogger.flush(); err != nil {
		t.Fatalf("second flush retained old error: %v", err)
	}
	if disabled, err := testLogger.close(); !disabled || err != nil {
		t.Fatalf("second close = (%v, %v)", disabled, err)
	}
}

func TestMarshalFailureDoesNotWriteEmptyFileRecord(t *testing.T) {
	file := &testFile{}
	testLogger, stderr := installTestLogger(t, 8, time.Hour, 1<<20, file)
	InfoF("stderr only", map[string]interface{}{"unsupported": make(chan int)})
	if err := testLogger.flushBuffered(false); err != nil {
		t.Fatal(err)
	}
	fileOutput, _, _, _ := file.snapshot()
	if fileOutput != "" {
		t.Fatalf("marshal failure wrote file bytes: %q", fileOutput)
	}
	if !strings.Contains(stderr.String(), "stderr only") {
		t.Fatalf("stderr output = %q", stderr.String())
	}
}

func TestUsesCurrentLogPrefixAndFlagsWithoutInterceptingStandardLog(t *testing.T) {
	file := &testFile{}
	testLogger, stderr := installTestLogger(t, 8, time.Hour, 1<<20, file)
	log.SetPrefix("current: ")
	log.SetFlags(0)
	log.Println("standard immediate")
	if stderr.String() != "current: standard immediate\n" {
		t.Fatalf("standard logger was intercepted: %q", stderr.String())
	}
	Info("buffered current format")
	if strings.Contains(stderr.String(), "buffered current format") {
		t.Fatal("structured logger wrote stderr before drain")
	}
	if err := testLogger.flushBuffered(false); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(stderr.String(), "current: ["+time.Now().UTC().Format("2006-01-02T15:04:05Z")+"] [INFO] buffered current format\n") {
		if !strings.Contains(stderr.String(), "current: [") || !strings.Contains(stderr.String(), "] [INFO] buffered current format\n") {
			t.Fatalf("current prefix/flags not used: %q", stderr.String())
		}
	}
}

func TestNoDuplicateMessagesAndStandardLogPreserved(t *testing.T) {
	file := &testFile{}
	testLogger, stderr := installTestLogger(t, 8, time.Hour, 1<<20, file)
	Info("structured-once")
	log.Println("standard-once")
	if err := testLogger.flush(); err != nil {
		t.Fatal(err)
	}
	fileOutput, _, _, _ := file.snapshot()
	if strings.Count(fileOutput, "structured-once") != 1 || strings.Contains(fileOutput, "standard-once") {
		t.Fatalf("unexpected JSONL duplication: %q", fileOutput)
	}
	if strings.Count(stderr.String(), "structured-once") != 1 || strings.Count(stderr.String(), "standard-once") != 1 {
		t.Fatalf("unexpected stderr duplication: %q", stderr.String())
	}
}

func TestPreEnableLoggingRemainsSynchronous(t *testing.T) {
	previousLogger := logger
	previousOutput := log.Writer()
	previousFlags := log.Flags()
	var stderr bytes.Buffer
	logger = newLogger(1, time.Hour, 1, 1)
	log.SetOutput(&stderr)
	log.SetFlags(0)
	t.Cleanup(func() {
		logger = previousLogger
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})
	Info("pre-enable")
	if !strings.Contains(stderr.String(), "] [INFO] pre-enable\n") {
		t.Fatalf("pre-enable stderr = %q", stderr.String())
	}
}

func TestLogLevelFiltering(t *testing.T) {
	file := &testFile{}
	testLogger, stderr := installTestLogger(t, 8, time.Hour, 1<<20, file)
	SetLevel(WARN)
	Debug("filtered debug")
	Info("filtered info")
	Warn("accepted warn")
	Error("accepted error")
	if err := testLogger.flush(); err != nil {
		t.Fatal(err)
	}
	output, _, _, _ := file.snapshot()
	if strings.Contains(output, "filtered") || strings.Contains(stderr.String(), "filtered") {
		t.Fatalf("filtered messages emitted: file=%q stderr=%q", output, stderr.String())
	}
	if !strings.Contains(output, "accepted warn") || !strings.Contains(output, "accepted error") {
		t.Fatalf("accepted messages missing: %q", output)
	}
}

func TestLogLevels(t *testing.T) {
	wants := []string{"DEBUG", "INFO", "WARN", "ERROR", "FATAL"}
	for level, want := range wants {
		if got := logLevelNames[LogLevel(level)]; got != want {
			t.Errorf("logLevelNames[%d] = %q, want %q", level, got, want)
		}
	}
}

func TestSetGetLevel(t *testing.T) {
	initialLevel := GetLevel()
	defer SetLevel(initialLevel)
	for _, level := range []LogLevel{DEBUG, INFO, WARN, ERROR, FATAL} {
		SetLevel(level)
		if got := GetLevel(); got != level {
			t.Errorf("GetLevel() = %v, want %v", got, level)
		}
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(time.Millisecond)
	}
}

func formattedSize(message string) int {
	entry := LogEntry{Level: "INFO", Timestamp: time.Now().UTC().Format(time.RFC3339), Message: message, Caller: "caller"}
	jsonData, _ := json.Marshal(entry)
	var stderr bytes.Buffer
	log.New(&stderr, log.Prefix(), log.Flags()).Println("[" + entry.Timestamp + "] [INFO] " + message)
	return len(jsonData) + 1 + stderr.Len()
}

func jsonlMessages(t *testing.T, file *testFile) []string {
	t.Helper()
	output, _, _, _ := file.snapshot()
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	messages := make([]string, 0, len(lines))
	for _, line := range lines {
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("parse JSONL line: %v", err)
		}
		messages = append(messages, entry.Message)
	}
	return messages
}

func textMessages(t *testing.T, output string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	messages := make([]string, 0, len(lines))
	for _, line := range lines {
		marker := "] [INFO] "
		index := strings.Index(line, marker)
		if index < 0 {
			t.Fatalf("unexpected stderr line: %q", line)
		}
		messages = append(messages, line[index+len(marker):])
	}
	return messages
}

var _ syncWriteCloser = (*testFile)(nil)
