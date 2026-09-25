package logger

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

const benchmarkEventsPer250ms = 64

var benchmarkSinkBytes int64

type benchmarkDestination struct {
	name   string
	file   bool
	stderr bool
}

var benchmarkDestinations = []benchmarkDestination{
	{name: "File", file: true},
	{name: "StderrPipe", stderr: true},
	{name: "FileAndStderrPipe", file: true, stderr: true},
}

type benchmarkLogEvent struct {
	component string
	message   string
	fields    map[string]interface{}
}

type benchmarkCountingWriter struct {
	writer io.Writer
	bytes  int64
	err    error
}

func (writer *benchmarkCountingWriter) Write(data []byte) (int, error) {
	written, err := writer.writer.Write(data)
	writer.bytes += int64(written)
	if writer.err == nil {
		writer.err = err
	}
	return written, err
}

type benchmarkPipeDrain struct {
	reader   *os.File
	bytes    atomic.Int64
	progress chan struct{}
	done     chan error
}

type benchmarkSinkHarness struct {
	file       *os.File
	pipeWriter *os.File
	pipeDrain  *benchmarkPipeDrain
	pipeBytes  int64
}

type benchmarkAsyncFile struct {
	file   *os.File
	bytes  atomic.Int64
	writes atomic.Int64
}

func (file *benchmarkAsyncFile) Write(data []byte) (int, error) {
	written, err := file.file.Write(data)
	file.bytes.Add(int64(written))
	file.writes.Add(1)
	return written, err
}

func (file *benchmarkAsyncFile) Sync() error {
	return file.file.Sync()
}

func (file *benchmarkAsyncFile) Close() error {
	return nil
}

type benchmarkAsyncWriter struct {
	writer io.Writer
	bytes  atomic.Int64
	writes atomic.Int64
}

func (writer *benchmarkAsyncWriter) Write(data []byte) (int, error) {
	written, err := writer.writer.Write(data)
	writer.bytes.Add(int64(written))
	writer.writes.Add(1)
	return written, err
}

func BenchmarkLoggerFormatAndIO(b *testing.B) {
	event := representativeBenchmarkEvent()
	fileLine, stderrLine := formatBenchmarkEvent(b, event)
	validateBenchmarkOutput(b, event, fileLine, stderrLine)
	validateBenchmarkBatchEquivalence(b, fileLine, stderrLine)

	for _, destination := range benchmarkDestinations {
		destination := destination
		b.Run(destination.name, func(b *testing.B) {
			b.Run("Immediate", func(b *testing.B) {
				benchmarkFormattedWrites(b, destination, event, false, len(fileLine), len(stderrLine))
			})
			b.Run("Batch250ms", func(b *testing.B) {
				benchmarkFormattedWrites(b, destination, event, true, len(fileLine), len(stderrLine))
			})
		})
	}
}

func BenchmarkLoggerPreformattedSinks(b *testing.B) {
	event := representativeBenchmarkEvent()
	fileLine, stderrLine := formatBenchmarkEvent(b, event)
	validateBenchmarkOutput(b, event, fileLine, stderrLine)
	validateBenchmarkBatchEquivalence(b, fileLine, stderrLine)

	for _, destination := range benchmarkDestinations {
		destination := destination
		b.Run(destination.name, func(b *testing.B) {
			b.Run("Immediate", func(b *testing.B) {
				benchmarkPreformattedWrites(b, destination, fileLine, stderrLine, false)
			})
			b.Run("Batch250ms", func(b *testing.B) {
				benchmarkPreformattedWrites(b, destination, fileLine, stderrLine, true)
			})
		})
	}
}

func BenchmarkLoggerImplemented(b *testing.B) {
	event := representativeBenchmarkEvent()
	b.Run("Producer", func(b *testing.B) {
		asyncLogger, file, stderr := startBenchmarkAsyncLogger(b, defaultBatchBytes)
		b.ReportAllocs()
		b.ReportMetric(1, "events/op")
		b.ResetTimer()
		for range b.N {
			InfoCF(event.component, event.message, event.fields)
		}
		b.StopTimer()
		if err := asyncLogger.flush(); err != nil {
			b.Fatal(err)
		}
		benchmarkSinkBytes += file.bytes.Load() + stderr.bytes.Load()
		b.ReportMetric(float64(file.writes.Load()+stderr.writes.Load())/float64(b.N), "sink-writes/op")
	})

	b.Run("ParallelProducer", func(b *testing.B) {
		asyncLogger, file, stderr := startBenchmarkAsyncLogger(b, defaultBatchBytes)
		b.ReportAllocs()
		b.ReportMetric(1, "events/op")
		b.ResetTimer()
		b.RunParallel(func(parallel *testing.PB) {
			for parallel.Next() {
				InfoCF(event.component, event.message, event.fields)
			}
		})
		b.StopTimer()
		if err := asyncLogger.flushBuffered(false); err != nil {
			b.Fatal(err)
		}
		benchmarkSinkBytes += file.bytes.Load() + stderr.bytes.Load()
		b.ReportMetric(float64(file.writes.Load()+stderr.writes.Load())/float64(b.N), "sink-writes/op")
	})

	b.Run("ProducerAndDrain64", func(b *testing.B) {
		asyncLogger, file, stderr := startBenchmarkAsyncLogger(b, 1<<30)
		b.ReportAllocs()
		b.ReportMetric(benchmarkEventsPer250ms, "events/op")
		b.ResetTimer()
		for range b.N {
			for range benchmarkEventsPer250ms {
				InfoCF(event.component, event.message, event.fields)
			}
			if err := asyncLogger.flushBuffered(false); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		benchmarkSinkBytes += file.bytes.Load() + stderr.bytes.Load()
		b.ReportMetric(float64(file.writes.Load()+stderr.writes.Load())/float64(b.N), "sink-writes/op")
	})
}

func startBenchmarkAsyncLogger(b *testing.B, batchBytes int) (*Logger, *benchmarkAsyncFile, *benchmarkAsyncWriter) {
	b.Helper()
	previousLogger := logger
	previousOutput := log.Writer()
	previousPrefix := log.Prefix()
	previousFlags := log.Flags()
	harness := newBenchmarkSinkHarness(b, benchmarkDestination{file: true, stderr: true})
	file := &benchmarkAsyncFile{file: harness.file}
	stderr := &benchmarkAsyncWriter{writer: harness.pipeWriter}
	log.SetOutput(stderr)
	log.SetPrefix("")
	log.SetFlags(log.LstdFlags)
	asyncLogger := newLogger(defaultQueueSize, time.Hour, batchBytes, defaultBufferedBytes)
	logger = asyncLogger
	if err := asyncLogger.enable(file); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		_, _ = asyncLogger.close()
		logger = previousLogger
		log.SetOutput(previousOutput)
		log.SetPrefix(previousPrefix)
		log.SetFlags(previousFlags)
	})
	return asyncLogger, file, stderr
}

func benchmarkFormattedWrites(b *testing.B, destination benchmarkDestination, event benchmarkLogEvent, batched bool, fileLineBytes, stderrLineBytes int) {
	b.Helper()
	harness := newBenchmarkSinkHarness(b, destination)
	var fileBuffer, stderrBuffer bytes.Buffer
	if batched {
		fileBuffer.Grow(fileLineBytes * benchmarkEventsPer250ms)
		stderrBuffer.Grow(stderrLineBytes * benchmarkEventsPer250ms)
	}
	fileWriter := io.Writer(harness.file)
	stderrWriter := io.Writer(harness.pipeWriter)
	if batched {
		fileWriter = &fileBuffer
		stderrWriter = &stderrBuffer
	}
	fileCounter := &benchmarkCountingWriter{writer: fileWriter}
	stderrCounter := &benchmarkCountingWriter{writer: stderrWriter}
	stderrLogger := log.New(stderrCounter, "", log.LstdFlags)

	b.ReportAllocs()
	b.ResetTimer()
	reportBenchmarkWork(b, destination, batched, fileLineBytes, stderrLineBytes)
	for iteration := 0; iteration < b.N; iteration++ {
		for range benchmarkEventsPer250ms {
			if err := writeFormattedBenchmarkEvent(destination, event, fileCounter, stderrLogger); err != nil {
				b.Fatal(err)
			}
		}
		if fileCounter.err != nil {
			b.Fatalf("write formatted benchmark file output: %v", fileCounter.err)
		}
		if stderrCounter.err != nil {
			b.Fatalf("write formatted benchmark stderr output: %v", stderrCounter.err)
		}
		if batched {
			if err := writeBenchmarkBytes(harness.file, fileBuffer.Bytes()); err != nil {
				b.Fatal(err)
			}
			if err := writeBenchmarkBytes(harness.pipeWriter, stderrBuffer.Bytes()); err != nil {
				b.Fatal(err)
			}
		}

		b.StopTimer()
		harness.consumeAndReset(b, fileCounter.bytes, stderrCounter.bytes)
		benchmarkSinkBytes += fileCounter.bytes + stderrCounter.bytes
		fileBuffer.Reset()
		stderrBuffer.Reset()
		fileCounter.bytes = 0
		fileCounter.err = nil
		stderrCounter.bytes = 0
		stderrCounter.err = nil
		if iteration+1 < b.N {
			b.StartTimer()
		}
	}
}

func benchmarkPreformattedWrites(b *testing.B, destination benchmarkDestination, fileLine, stderrLine []byte, batched bool) {
	b.Helper()
	harness := newBenchmarkSinkHarness(b, destination)
	var fileBuffer, stderrBuffer bytes.Buffer
	if batched {
		fileBuffer.Grow(len(fileLine) * benchmarkEventsPer250ms)
		stderrBuffer.Grow(len(stderrLine) * benchmarkEventsPer250ms)
	}

	b.ReportAllocs()
	b.ResetTimer()
	reportBenchmarkWork(b, destination, batched, len(fileLine), len(stderrLine))
	for iteration := 0; iteration < b.N; iteration++ {
		var fileBytes, stderrBytes int64
		for range benchmarkEventsPer250ms {
			if destination.file {
				if batched {
					if err := writeBenchmarkBytes(&fileBuffer, fileLine); err != nil {
						b.Fatal(err)
					}
				} else {
					if err := writeBenchmarkBytes(harness.file, fileLine); err != nil {
						b.Fatal(err)
					}
				}
				fileBytes += int64(len(fileLine))
			}
			if destination.stderr {
				if batched {
					if err := writeBenchmarkBytes(&stderrBuffer, stderrLine); err != nil {
						b.Fatal(err)
					}
				} else {
					if err := writeBenchmarkBytes(harness.pipeWriter, stderrLine); err != nil {
						b.Fatal(err)
					}
				}
				stderrBytes += int64(len(stderrLine))
			}
		}
		if batched {
			if err := writeBenchmarkBytes(harness.file, fileBuffer.Bytes()); err != nil {
				b.Fatal(err)
			}
			if err := writeBenchmarkBytes(harness.pipeWriter, stderrBuffer.Bytes()); err != nil {
				b.Fatal(err)
			}
		}

		b.StopTimer()
		harness.consumeAndReset(b, fileBytes, stderrBytes)
		benchmarkSinkBytes += fileBytes + stderrBytes
		fileBuffer.Reset()
		stderrBuffer.Reset()
		if iteration+1 < b.N {
			b.StartTimer()
		}
	}
}

func representativeBenchmarkEvent() benchmarkLogEvent {
	return benchmarkLogEvent{
		component: "agent",
		message:   "provider request completed",
		fields:    map[string]interface{}{"request_id": "req-benchmark-0123456789"},
	}
}

func formatBenchmarkEvent(tb testing.TB, event benchmarkLogEvent) ([]byte, []byte) {
	tb.Helper()
	var fileBuffer, stderrBuffer bytes.Buffer
	fileCounter := &benchmarkCountingWriter{writer: &fileBuffer}
	stderrLogger := log.New(&stderrBuffer, "", log.LstdFlags)
	if err := writeFormattedBenchmarkEvent(benchmarkDestination{file: true, stderr: true}, event, fileCounter, stderrLogger); err != nil {
		tb.Fatal(err)
	}
	return bytes.Clone(fileBuffer.Bytes()), bytes.Clone(stderrBuffer.Bytes())
}

func writeFormattedBenchmarkEvent(destination benchmarkDestination, event benchmarkLogEvent, fileWriter io.Writer, stderrLogger *log.Logger) error {
	entry := LogEntry{
		Level:     logLevelNames[INFO],
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Component: event.component,
		Message:   event.message,
		Fields:    event.fields,
	}
	if pc, file, line, ok := runtime.Caller(1); ok {
		if function := runtime.FuncForPC(pc); function != nil {
			entry.Caller = fmt.Sprintf("%s:%d (%s)", file, line, function.Name())
		}
	}

	if destination.file {
		jsonData, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("marshal benchmark log entry: %w", err)
		}
		if err := writeBenchmarkString(fileWriter, string(jsonData)+"\n"); err != nil {
			return err
		}
	}
	if destination.stderr {
		fieldString := ""
		if len(event.fields) > 0 {
			fieldString = " " + formatFields(event.fields)
		}
		stderrLogger.Println(fmt.Sprintf("[%s] [%s]%s %s%s",
			entry.Timestamp,
			logLevelNames[INFO],
			formatComponent(event.component),
			event.message,
			fieldString,
		))
	}
	return nil
}

func validateBenchmarkOutput(tb testing.TB, event benchmarkLogEvent, fileLine, stderrLine []byte) {
	tb.Helper()
	if len(fileLine) == 0 || fileLine[len(fileLine)-1] != '\n' {
		tb.Fatal("benchmark file output is not JSONL")
	}
	var entry LogEntry
	if err := json.Unmarshal(bytes.TrimSuffix(fileLine, []byte("\n")), &entry); err != nil {
		tb.Fatalf("parse benchmark JSONL: %v", err)
	}
	if entry.Level != "INFO" || entry.Component != event.component || entry.Message != event.message || entry.Caller == "" {
		tb.Fatalf("unexpected benchmark JSONL entry: %+v", entry)
	}
	if entry.Fields["request_id"] != event.fields["request_id"] {
		tb.Fatalf("unexpected benchmark JSONL fields: %v", entry.Fields)
	}

	if len(stderrLine) < len("2006/01/02 15:04:05 ")+1 || stderrLine[len(stderrLine)-1] != '\n' {
		tb.Fatalf("unexpected benchmark stderr framing: %q", stderrLine)
	}
	if _, err := time.ParseInLocation("2006/01/02 15:04:05", string(stderrLine[:19]), time.Local); err != nil {
		tb.Fatalf("parse benchmark stderr timestamp: %v", err)
	}
	wantSuffix := fmt.Sprintf(" [%s] [INFO] %s: %s %s\n",
		entry.Timestamp,
		event.component,
		event.message,
		formatFields(event.fields),
	)
	if gotSuffix := string(stderrLine[19:]); gotSuffix != wantSuffix {
		tb.Fatalf("benchmark stderr suffix = %q, want %q", gotSuffix, wantSuffix)
	}
}

func validateBenchmarkBatchEquivalence(tb testing.TB, fileLine, stderrLine []byte) {
	tb.Helper()
	for _, line := range [][]byte{fileLine, stderrLine} {
		var immediate bytes.Buffer
		var batched bytes.Buffer
		for range benchmarkEventsPer250ms {
			if _, err := immediate.Write(line); err != nil {
				tb.Fatal(err)
			}
		}
		batch := bytes.Repeat(line, benchmarkEventsPer250ms)
		if _, err := batched.Write(batch); err != nil {
			tb.Fatal(err)
		}
		if sha256.Sum256(immediate.Bytes()) != sha256.Sum256(batched.Bytes()) {
			tb.Fatal("immediate and batched log output digests differ")
		}
	}
}

func newBenchmarkSinkHarness(b *testing.B, destination benchmarkDestination) *benchmarkSinkHarness {
	b.Helper()
	harness := &benchmarkSinkHarness{}
	if destination.file {
		path := filepath.Join(b.TempDir(), "benchmark.jsonl")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			b.Fatalf("open append benchmark file: %v", err)
		}
		harness.file = file
	}
	if destination.stderr {
		reader, writer, err := os.Pipe()
		if err != nil {
			b.Fatalf("open benchmark stderr pipe: %v", err)
		}
		harness.pipeWriter = writer
		harness.pipeDrain = startBenchmarkPipeDrain(reader)
	}
	b.Cleanup(func() {
		harness.close(b)
	})
	return harness
}

func startBenchmarkPipeDrain(reader *os.File) *benchmarkPipeDrain {
	drain := &benchmarkPipeDrain{
		reader:   reader,
		progress: make(chan struct{}, 1),
		done:     make(chan error, 1),
	}
	go func() {
		buffer := make([]byte, 32*1024)
		for {
			read, err := reader.Read(buffer)
			if read > 0 {
				drain.bytes.Add(int64(read))
				select {
				case drain.progress <- struct{}{}:
				default:
				}
			}
			if err != nil {
				if err == io.EOF {
					err = nil
				}
				drain.done <- err
				return
			}
		}
	}()
	return drain
}

func (harness *benchmarkSinkHarness) consumeAndReset(b *testing.B, fileBytes, stderrBytes int64) {
	b.Helper()
	if harness.file != nil {
		info, err := harness.file.Stat()
		if err != nil {
			b.Fatalf("stat benchmark file: %v", err)
		}
		if info.Size() != fileBytes {
			b.Fatalf("benchmark file bytes = %d, want %d", info.Size(), fileBytes)
		}
		if err := harness.file.Truncate(0); err != nil {
			b.Fatalf("truncate benchmark file: %v", err)
		}
	}
	if harness.pipeDrain != nil {
		harness.pipeBytes += stderrBytes
		harness.pipeDrain.waitFor(b, harness.pipeBytes)
	}
}

func (drain *benchmarkPipeDrain) waitFor(b *testing.B, target int64) {
	b.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for drain.bytes.Load() < target {
		select {
		case <-drain.progress:
		case err := <-drain.done:
			b.Fatalf("benchmark stderr drain stopped early: %v", err)
		case <-timer.C:
			b.Fatalf("timed out draining benchmark stderr pipe: got %d of %d bytes", drain.bytes.Load(), target)
		}
	}
}

func (harness *benchmarkSinkHarness) close(b *testing.B) {
	b.Helper()
	if harness.pipeWriter != nil {
		if err := harness.pipeWriter.Close(); err != nil {
			b.Errorf("close benchmark stderr pipe writer: %v", err)
		}
		if err := <-harness.pipeDrain.done; err != nil {
			b.Errorf("drain benchmark stderr pipe: %v", err)
		}
		if err := harness.pipeDrain.reader.Close(); err != nil {
			b.Errorf("close benchmark stderr pipe reader: %v", err)
		}
	}
	if harness.file != nil {
		if err := harness.file.Close(); err != nil {
			b.Errorf("close append benchmark file: %v", err)
		}
	}
}

func reportBenchmarkWork(b *testing.B, destination benchmarkDestination, batched bool, fileLineBytes, stderrLineBytes int) {
	b.Helper()
	bytesPerWindow := 0
	writesPerWindow := 0
	if destination.file {
		bytesPerWindow += fileLineBytes * benchmarkEventsPer250ms
		writesPerWindow += benchmarkEventsPer250ms
	}
	if destination.stderr {
		bytesPerWindow += stderrLineBytes * benchmarkEventsPer250ms
		writesPerWindow += benchmarkEventsPer250ms
	}
	if batched {
		writesPerWindow = 0
		if destination.file {
			writesPerWindow++
		}
		if destination.stderr {
			writesPerWindow++
		}
	}
	b.SetBytes(int64(bytesPerWindow))
	b.ReportMetric(benchmarkEventsPer250ms, "events/op")
	b.ReportMetric(float64(writesPerWindow), "sink-writes/op")
}

func writeBenchmarkString(writer io.Writer, value string) error {
	written, err := io.WriteString(writer, value)
	if err != nil {
		return fmt.Errorf("write benchmark log output: %w", err)
	}
	if written != len(value) {
		return fmt.Errorf("short benchmark log write: wrote %d of %d bytes", written, len(value))
	}
	return nil
}

func writeBenchmarkBytes(writer io.Writer, value []byte) error {
	if len(value) == 0 || writer == nil {
		return nil
	}
	written, err := writer.Write(value)
	if err != nil {
		return fmt.Errorf("write benchmark sink output: %w", err)
	}
	if written != len(value) {
		return fmt.Errorf("short benchmark sink write: wrote %d of %d bytes", written, len(value))
	}
	return nil
}
