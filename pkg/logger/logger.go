package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
	FATAL
)

const (
	defaultQueueSize     = 1024
	defaultFlushInterval = 250 * time.Millisecond
	defaultBatchBytes    = 64 * 1024
	defaultBufferedBytes = 1024 * 1024
)

var (
	logLevelNames = map[LogLevel]string{
		DEBUG: "DEBUG",
		INFO:  "INFO",
		WARN:  "WARN",
		ERROR: "ERROR",
		FATAL: "FATAL",
	}

	currentLevel = INFO
	logger       = newLogger(defaultQueueSize, defaultFlushInterval, defaultBatchBytes, defaultBufferedBytes)
	mu           sync.RWMutex
)

type syncWriteCloser interface {
	io.Writer
	Sync() error
	Close() error
}

type queuedLog struct {
	file   []byte
	stderr []byte
	bytes  int
	flush  chan error
	sync   bool
}

type Logger struct {
	lifecycleMu      sync.Mutex
	mu               sync.Mutex
	errMu            sync.Mutex
	admission        *sync.Cond
	file             syncWriteCloser
	stderr           io.Writer
	queue            chan queuedLog
	done             chan struct{}
	wake             chan struct{}
	enabled          bool
	closing          bool
	firstErr         error
	inFlightBytes    int
	queueSize        int
	flushInterval    time.Duration
	batchBytes       int
	maxBufferedBytes int
	exit             func(int)
}

type LogEntry struct {
	Level     string                 `json:"level"`
	Timestamp string                 `json:"timestamp"`
	Component string                 `json:"component,omitempty"`
	Message   string                 `json:"message"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
	Caller    string                 `json:"caller,omitempty"`
}

func newLogger(queueSize int, flushInterval time.Duration, batchBytes, maxBufferedBytes int) *Logger {
	logger := &Logger{
		queueSize:        queueSize,
		flushInterval:    flushInterval,
		batchBytes:       batchBytes,
		maxBufferedBytes: maxBufferedBytes,
		exit:             os.Exit,
	}
	logger.admission = sync.NewCond(&logger.mu)
	return logger
}

func SetLevel(level LogLevel) {
	mu.Lock()
	defer mu.Unlock()
	currentLevel = level
}

func GetLevel() LogLevel {
	mu.RLock()
	defer mu.RUnlock()
	return currentLevel
}

func EnableFileLogging(filePath string) error {
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	if err := logger.enable(file); err != nil {
		_ = file.Close()
		return err
	}
	log.Println("File logging enabled:", filePath)
	return nil
}

func DisableFileLogging() {
	disabled, _ := logger.close()
	if disabled {
		log.Println("File logging disabled")
	}
}

func Shutdown() error {
	_, err := logger.close()
	return err
}

func (logger *Logger) enable(file syncWriteCloser) error {
	logger.lifecycleMu.Lock()
	defer logger.lifecycleMu.Unlock()
	_, _ = logger.closeActive()

	logger.mu.Lock()
	logger.file = file
	logger.stderr = log.Writer()
	logger.queue = make(chan queuedLog, logger.queueSize)
	logger.done = make(chan struct{})
	logger.wake = make(chan struct{}, 1)
	logger.enabled = true
	logger.closing = false
	logger.inFlightBytes = 0
	queue := logger.queue
	done := logger.done
	wake := logger.wake
	stderr := logger.stderr
	logger.mu.Unlock()
	logger.clearError()

	go logger.run(queue, done, wake, file, stderr)
	return nil
}

func (logger *Logger) close() (bool, error) {
	logger.lifecycleMu.Lock()
	defer logger.lifecycleMu.Unlock()
	return logger.closeActive()
}

func (logger *Logger) closeActive() (bool, error) {
	logger.mu.Lock()
	if !logger.enabled {
		logger.mu.Unlock()
		return false, nil
	}
	logger.enabled = false
	done := logger.done
	wake := logger.wake
	logger.closing = true
	logger.admission.Broadcast()
	logger.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
	<-done

	logger.mu.Lock()
	logger.file = nil
	logger.stderr = nil
	logger.queue = nil
	logger.done = nil
	logger.wake = nil
	logger.closing = false
	logger.inFlightBytes = 0
	logger.mu.Unlock()
	return true, logger.takeError()
}

func (logger *Logger) flush() error {
	return logger.flushBuffered(true)
}

func (logger *Logger) flushBuffered(syncFile bool) error {
	acknowledged := make(chan error, 1)
	logger.mu.Lock()
	if logger.enabled {
		logger.inFlightBytes++
		queue := logger.queue
		logger.mu.Unlock()
		queue <- queuedLog{bytes: 1, flush: acknowledged, sync: syncFile}
		return <-acknowledged
	}
	done := logger.done
	logger.mu.Unlock()
	if done != nil {
		<-done
	}
	return logger.loadError()
}

func (logger *Logger) run(queue <-chan queuedLog, done chan<- struct{}, wake <-chan struct{}, file syncWriteCloser, stderr io.Writer) {
	ticker := time.NewTicker(logger.flushInterval)
	defer ticker.Stop()
	defer close(done)

	var fileBuffer bytes.Buffer
	var stderrBuffer bytes.Buffer
	bufferedBytes := 0
	fileFailed := false
	stderrFailed := false
	flush := func(syncFile bool) {
		if stderrBuffer.Len() > 0 && !stderrFailed {
			if err := writeAll(stderr, stderrBuffer.Bytes()); err != nil {
				logger.recordError(fmt.Errorf("write stderr log: %w", err))
				stderrFailed = true
			}
		}
		if fileBuffer.Len() > 0 && !fileFailed {
			if err := writeAll(file, fileBuffer.Bytes()); err != nil {
				logger.recordError(fmt.Errorf("write log file: %w", err))
				fileFailed = true
			}
		}
		resetBuffer(&fileBuffer, logger.batchBytes)
		resetBuffer(&stderrBuffer, logger.batchBytes)
		if syncFile && !fileFailed {
			if err := file.Sync(); err != nil {
				logger.recordError(fmt.Errorf("sync log file: %w", err))
				fileFailed = true
			}
		}
		logger.release(bufferedBytes)
		bufferedBytes = 0
	}

	closeWorker := func() bool {
		logger.mu.Lock()
		closing := logger.closing
		inFlight := logger.inFlightBytes
		logger.mu.Unlock()
		if !closing || inFlight != 0 {
			return false
		}
		flush(true)
		if err := file.Close(); err != nil {
			logger.recordError(fmt.Errorf("close log file: %w", err))
		}
		return true
	}

	for {
		select {
		case item := <-queue:
			if !fileFailed {
				_, _ = fileBuffer.Write(item.file)
			}
			if !stderrFailed {
				_, _ = stderrBuffer.Write(item.stderr)
			}
			bufferedBytes += item.bytes
			if fileBuffer.Len()+stderrBuffer.Len() >= logger.batchBytes {
				flush(false)
			}
			if item.flush != nil {
				flush(item.sync)
				item.flush <- logger.loadError()
			}
			if logger.isClosing() {
				flush(false)
				if closeWorker() {
					return
				}
			}
		case <-ticker.C:
			flush(false)
			if closeWorker() {
				return
			}
		case <-wake:
			flush(false)
			if closeWorker() {
				return
			}
		}
	}
}

func (logger *Logger) recordError(err error) {
	if err == nil {
		return
	}
	logger.errMu.Lock()
	logger.firstErr = errors.Join(logger.firstErr, err)
	logger.errMu.Unlock()
}

func (logger *Logger) isClosing() bool {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	return logger.closing
}

func (logger *Logger) loadError() error {
	logger.errMu.Lock()
	defer logger.errMu.Unlock()
	return logger.firstErr
}

func (logger *Logger) takeError() error {
	logger.errMu.Lock()
	defer logger.errMu.Unlock()
	err := logger.firstErr
	logger.firstErr = nil
	return err
}

func (logger *Logger) clearError() {
	logger.errMu.Lock()
	logger.firstErr = nil
	logger.errMu.Unlock()
}

func (logger *Logger) release(bytes int) {
	if bytes == 0 {
		return
	}
	logger.mu.Lock()
	logger.inFlightBytes -= bytes
	logger.admission.Broadcast()
	logger.mu.Unlock()
}

func resetBuffer(buffer *bytes.Buffer, retainBytes int) {
	if buffer.Cap() > retainBytes {
		*buffer = bytes.Buffer{}
		return
	}
	buffer.Reset()
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if written > 0 {
			data = data[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func logMessage(level LogLevel, component string, message string, fields map[string]interface{}) {
	if level < GetLevel() {
		return
	}

	entry := LogEntry{
		Level:     logLevelNames[level],
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Component: component,
		Message:   message,
		Fields:    fields,
	}

	if pc, file, line, ok := runtime.Caller(2); ok {
		fn := runtime.FuncForPC(pc)
		if fn != nil {
			entry.Caller = fmt.Sprintf("%s:%d (%s)", file, line, fn.Name())
		}
	}

	var fieldStr string
	if len(fields) > 0 {
		fieldStr = " " + formatFields(fields)
	}

	logLine := fmt.Sprintf("[%s] [%s]%s %s%s",
		entry.Timestamp,
		logLevelNames[level],
		formatComponent(component),
		message,
		fieldStr,
	)

	queued := logger.formatAndEnqueue(entry, logLine)
	if !queued {
		log.Println(logLine)
	}

	if level == FATAL {
		if queued {
			_ = logger.flush()
		}
		logger.exit(1)
	}
}

func (logger *Logger) formatAndEnqueue(entry LogEntry, logLine string) bool {
	logger.mu.Lock()
	if !logger.enabled {
		logger.mu.Unlock()
		return false
	}
	logger.mu.Unlock()

	var stderrBuffer bytes.Buffer
	log.New(&stderrBuffer, log.Prefix(), log.Flags()).Println(logLine)
	stderrLine := stderrBuffer.Bytes()
	var fileLine []byte
	if jsonData, err := json.Marshal(entry); err == nil {
		fileLine = append(jsonData, '\n')
	}
	itemBytes := len(fileLine) + len(stderrLine)

	logger.mu.Lock()
	for logger.enabled && logger.inFlightBytes > 0 && logger.inFlightBytes+itemBytes > logger.maxBufferedBytes {
		logger.admission.Wait()
	}
	if !logger.enabled {
		logger.mu.Unlock()
		return false
	}
	logger.inFlightBytes += itemBytes
	queue := logger.queue
	logger.mu.Unlock()
	queue <- queuedLog{file: fileLine, stderr: stderrLine, bytes: itemBytes}
	return true
}

func formatComponent(component string) string {
	if component == "" {
		return ""
	}
	return fmt.Sprintf(" %s:", component)
}

func formatFields(fields map[string]interface{}) string {
	var parts []string
	for k, v := range fields {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return fmt.Sprintf("{%s}", strings.Join(parts, ", "))
}

func Debug(message string) {
	logMessage(DEBUG, "", message, nil)
}

func DebugC(component string, message string) {
	logMessage(DEBUG, component, message, nil)
}

func DebugF(message string, fields map[string]interface{}) {
	logMessage(DEBUG, "", message, fields)
}

func DebugCF(component string, message string, fields map[string]interface{}) {
	logMessage(DEBUG, component, message, fields)
}

func Info(message string) {
	logMessage(INFO, "", message, nil)
}

func InfoC(component string, message string) {
	logMessage(INFO, component, message, nil)
}

func InfoF(message string, fields map[string]interface{}) {
	logMessage(INFO, "", message, fields)
}

func InfoCF(component string, message string, fields map[string]interface{}) {
	logMessage(INFO, component, message, fields)
}

func Warn(message string) {
	logMessage(WARN, "", message, nil)
}

func WarnC(component string, message string) {
	logMessage(WARN, component, message, nil)
}

func WarnF(message string, fields map[string]interface{}) {
	logMessage(WARN, "", message, fields)
}

func WarnCF(component string, message string, fields map[string]interface{}) {
	logMessage(WARN, component, message, fields)
}

func Error(message string) {
	logMessage(ERROR, "", message, nil)
}

func ErrorC(component string, message string) {
	logMessage(ERROR, component, message, nil)
}

func ErrorF(message string, fields map[string]interface{}) {
	logMessage(ERROR, "", message, fields)
}

func ErrorCF(component string, message string, fields map[string]interface{}) {
	logMessage(ERROR, component, message, fields)
}

func Fatal(message string) {
	logMessage(FATAL, "", message, nil)
}

func FatalC(component string, message string) {
	logMessage(FATAL, component, message, nil)
}

func FatalF(message string, fields map[string]interface{}) {
	logMessage(FATAL, "", message, fields)
}

func FatalCF(component string, message string, fields map[string]interface{}) {
	logMessage(FATAL, component, message, fields)
}
