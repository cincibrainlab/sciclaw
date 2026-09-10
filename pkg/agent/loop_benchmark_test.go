package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/skills"
)

var (
	benchmarkMessagesSink    []providers.Message
	benchmarkToolsSink       []providers.ToolDefinition
	benchmarkStringSink      string
	benchmarkMessagesLogSink string
	benchmarkToolsLogSink    string
)

type benchmarkWorkspace struct {
	path           string
	fileCount      int
	contentBytes   int
	bootstrapFiles int
	skillCount     int
	memoryBytes    int
	policyFiles    int
}

// benchmarkSentinelProvider is the only LLM provider used here. Its Chat method
// stops timing on entry, performs no HTTP, serialization, or network work, then
// validates the pre-provider request and returns errBenchmarkProviderBoundary.
type benchmarkSentinelProvider struct {
	benchmark          *testing.B
	calls              int
	expectedMessages   int
	expectedTools      int
	afterStop          func() error
	afterTimerBoundary func()
	boundaryWrites     int64
	boundaryBytes      int64
	validated          bool
}

type benchmarkCountingWriter struct {
	writer io.Writer
	writes int64
	bytes  int64
}

func (writer *benchmarkCountingWriter) Write(data []byte) (int, error) {
	n, err := writer.writer.Write(data)
	writer.writes++
	writer.bytes += int64(n)
	return n, err
}

func (writer *benchmarkCountingWriter) Reset() {
	writer.writes = 0
	writer.bytes = 0
}

var errBenchmarkProviderBoundary = errors.New("benchmark reached provider call boundary")

func (provider *benchmarkSentinelProvider) Chat(_ context.Context, messages []providers.Message, tools []providers.ToolDefinition, model string, options map[string]interface{}) (*providers.LLMResponse, error) {
	provider.benchmark.StopTimer()
	if provider.afterStop != nil {
		if err := provider.afterStop(); err != nil {
			return nil, err
		}
	}
	if provider.afterTimerBoundary != nil {
		provider.afterTimerBoundary()
	}
	provider.calls++
	if !provider.validated {
		if err := provider.validateCall(messages, tools, model, options); err != nil {
			return nil, err
		}
		provider.validated = true
	}
	return nil, errBenchmarkProviderBoundary
}

func (provider *benchmarkSentinelProvider) GetDefaultModel() string {
	return "gpt-5.4"
}

func (provider *benchmarkSentinelProvider) validateCall(messages []providers.Message, tools []providers.ToolDefinition, model string, options map[string]interface{}) error {
	maxTokens, _ := options["max_tokens"].(int)
	reasoningEffort, _ := options["reasoning_effort"].(string)
	if model != "openai/gpt-5.4" || maxTokens != 8192 || reasoningEffort != "low" {
		return fmt.Errorf("unexpected stable LLM call fields: model=%q max_tokens=%d reasoning_effort=%q", model, maxTokens, reasoningEffort)
	}
	if len(messages) != provider.expectedMessages || len(tools) != provider.expectedTools {
		return fmt.Errorf("unexpected LLM call sizes: messages=%d tools=%d", len(messages), len(tools))
	}
	if len(messages) == 0 || messages[0].Role != "system" || messages[len(messages)-1].Content != "Compare the latest result with the prior controls." {
		return fmt.Errorf("unexpected LLM message semantics")
	}
	return nil
}

func BenchmarkPreLLMStages(b *testing.B) {
	b.StopTimer()
	workspace := newBenchmarkWorkspace(b)
	history := benchmarkHistory()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace.path
	cfg.Agents.Defaults.SharedWorkspace = workspace.path
	cfg.Agents.Defaults.Model = "openai/gpt-5.4"

	previousLogLevel := logger.GetLevel()
	logger.SetLevel(logger.FATAL)
	b.Cleanup(func() {
		logger.SetLevel(previousLogLevel)
	})

	provider := &benchmarkSentinelProvider{benchmark: b}
	agentLoop := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	agentLoop.contextBuilder.skillsLoader = skills.NewSkillsLoader(workspace.path, "", "")
	b.Cleanup(agentLoop.Stop)
	if hooks := agentLoop.GetStartupInfo()["hooks"].(map[string]interface{}); hooks["enabled"].(bool) || hooks["audit_path"].(string) != "" {
		b.Fatalf("benchmark hooks were not disabled: %#v", hooks)
	}

	const sessionKey = "benchmark-session"
	agentLoop.sessions.ReplaceHistory(sessionKey, history)
	messages := agentLoop.contextBuilder.BuildMessages(
		history,
		"The assay compares treated samples with vehicle and untreated controls.",
		"Compare the latest result with the prior controls.",
		nil,
		"cli",
		"direct",
	)
	toolDefinitions := agentLoop.tools.ToProviderDefs()

	b.Run("SessionHistoryCopy", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.ReportMetric(float64(len(history)), "history-msgs")
		for range b.N {
			benchmarkMessagesSink = agentLoop.sessions.GetHistory(sessionKey)
		}
	})

	b.Run("SystemPromptConstructionColdRebuild", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		reportWorkspaceMetrics(b, workspace)
		for range b.N {
			agentLoop.contextBuilder.invalidateStaticPrompt()
			benchmarkStringSink = agentLoop.contextBuilder.BuildSystemPrompt()
		}
	})

	b.Run("SystemPromptConstructionWarmHit", func(b *testing.B) {
		agentLoop.contextBuilder.BuildSystemPrompt()
		b.ReportAllocs()
		b.ResetTimer()
		reportWorkspaceMetrics(b, workspace)
		for range b.N {
			benchmarkStringSink = agentLoop.contextBuilder.BuildSystemPrompt()
		}
	})

	b.Run("MessageAssembly", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		reportWorkspaceMetrics(b, workspace)
		b.ReportMetric(float64(len(history)), "history-msgs")
		for range b.N {
			benchmarkMessagesSink = agentLoop.contextBuilder.BuildMessages(
				history,
				"The assay compares treated samples with vehicle and untreated controls.",
				"Compare the latest result with the prior controls.",
				nil,
				"cli",
				"direct",
			)
		}
	})

	b.Run("ToolDefinitionConstruction", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.ReportMetric(float64(len(toolDefinitions)), "tool-defs")
		for range b.N {
			benchmarkToolsSink = agentLoop.tools.ToProviderDefs()
		}
	})

	b.Run("EagerRequestLogFormatting", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.ReportMetric(float64(len(messages)), "messages")
		b.ReportMetric(float64(len(toolDefinitions)), "tool-defs")
		for range b.N {
			benchmarkMessagesLogSink = formatMessagesForLog(messages)
			benchmarkToolsLogSink = formatToolsForLog(toolDefinitions)
		}
	})

	b.Run("EndToEndProcessDirectToSentinelProviderNoNetwork", func(b *testing.B) {
		benchmarkProcessDirectToProviderCall(b, agentLoop, provider, workspace, history, sessionKey, len(messages), len(toolDefinitions), nil, nil)
	})

	b.Run("EndToEndProcessDirectToSentinelProviderNoNetworkDebugPerEntry", func(b *testing.B) {
		sink := newBenchmarkLogSink(b)
		benchmarkProcessDirectToProviderCall(b, agentLoop, provider, workspace, history, sessionKey, len(messages), len(toolDefinitions), sink, func(provider *benchmarkSentinelProvider) func() {
			previousOutput := log.Writer()
			previousLogLevel := logger.GetLevel()
			log.SetOutput(sink)
			logger.SetLevel(logger.DEBUG)
			provider.afterTimerBoundary = func() {
				provider.boundaryWrites = sink.writes
				provider.boundaryBytes = sink.bytes
			}
			b.Cleanup(func() {
				logger.SetLevel(previousLogLevel)
				log.SetOutput(previousOutput)
			})
			return func() {
				sink.Reset()
			}
		})
	})

	b.Run("EndToEndProcessDirectToSentinelProviderNoNetworkDebugBuffered", func(b *testing.B) {
		sink := newBenchmarkLogSink(b)
		var buffer bytes.Buffer
		benchmarkProcessDirectToProviderCall(b, agentLoop, provider, workspace, history, sessionKey, len(messages), len(toolDefinitions), sink, func(provider *benchmarkSentinelProvider) func() {
			previousOutput := log.Writer()
			previousLogLevel := logger.GetLevel()
			log.SetOutput(&buffer)
			logger.SetLevel(logger.DEBUG)
			provider.afterStop = func() error {
				_, err := buffer.WriteTo(sink)
				if err == nil && sink.writes != 1 {
					return fmt.Errorf("buffered log flush writes = %d, want 1", sink.writes)
				}
				return err
			}
			provider.afterTimerBoundary = func() {
				provider.boundaryWrites = sink.writes
				provider.boundaryBytes = sink.bytes
			}
			b.Cleanup(func() {
				logger.SetLevel(previousLogLevel)
				log.SetOutput(previousOutput)
			})
			return func() {
				buffer.Reset()
				sink.Reset()
			}
		})
	})
}

func newBenchmarkLogSink(b *testing.B) *benchmarkCountingWriter {
	b.Helper()
	file, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatalf("open benchmark log sink: %v", err)
	}
	b.Cleanup(func() {
		if err := file.Close(); err != nil {
			b.Errorf("close benchmark log sink: %v", err)
		}
	})
	return &benchmarkCountingWriter{writer: file}
}

func benchmarkProcessDirectToProviderCall(
	b *testing.B,
	agentLoop *AgentLoop,
	provider *benchmarkSentinelProvider,
	workspace benchmarkWorkspace,
	history []providers.Message,
	sessionKey string,
	expectedMessages int,
	expectedTools int,
	sink *benchmarkCountingWriter,
	configure func(*benchmarkSentinelProvider) func(),
) {
	b.Helper()
	provider.benchmark = b
	provider.calls = 0
	provider.expectedMessages = expectedMessages
	provider.expectedTools = expectedTools
	provider.afterStop = nil
	provider.afterTimerBoundary = nil
	provider.validated = false
	agentLoop.sessions.ReplaceHistory(sessionKey, history)
	var reset func()
	if configure != nil {
		reset = configure(provider)
	}
	b.ReportAllocs()
	b.ResetTimer()
	reportWorkspaceMetrics(b, workspace)
	b.ReportMetric(float64(len(history)), "history-msgs")
	var totalWrites int64
	var totalBytes int64
	for range b.N {
		if reset != nil {
			reset()
		}
		b.StartTimer()
		response, err := processDirectToProviderCall(
			agentLoop,
			context.Background(),
			"Compare the latest result with the prior controls.",
			sessionKey,
			"cli",
			"direct",
			"benchmark-user",
		)
		if err != nil {
			b.Fatalf("ProcessDirectWithChannel: %v", err)
		}
		benchmarkStringSink = response
		agentLoop.sessions.ReplaceHistory(sessionKey, history)
		if sink != nil {
			totalWrites += provider.boundaryWrites
			totalBytes += provider.boundaryBytes
		}
	}
	if provider.calls != b.N {
		b.Fatalf("provider boundaries = %d, want %d", provider.calls, b.N)
	}
	if !provider.validated {
		b.Fatal("LLM call arguments were not validated")
	}
	if sink != nil {
		b.ReportMetric(float64(totalWrites)/float64(b.N), "writes/op")
		b.ReportMetric(float64(totalBytes)/float64(b.N), "bytes/op")
	}
}

func processDirectToProviderCall(agentLoop *AgentLoop, ctx context.Context, content, sessionKey, channel, chatID, senderID string) (string, error) {
	response, err := agentLoop.ProcessDirectWithChannel(ctx, content, sessionKey, channel, chatID, senderID)
	if errors.Is(err, errBenchmarkProviderBoundary) {
		return "", nil
	}
	return response, err
}

func newBenchmarkWorkspace(b *testing.B) benchmarkWorkspace {
	b.Helper()
	fixture := benchmarkWorkspace{path: b.TempDir()}
	bootstrap := map[string]string{
		"AGENTS.md":   "# Project Guidance\n\nUse reproducible analyses and record evidence paths.\n",
		"IDENTITY.md": "# Identity\n\nYou are the paired analysis assistant for this project.\n",
		"SOUL.md":     "# Working Style\n\nBe concise, skeptical, and explicit about uncertainty.\n",
		"TOOLS.md":    "# Tools\n\nPrefer typed scientific tools over ad hoc shell commands.\n",
		"USER.md":     "# Collaborator\n\nThe collaborator prefers compact methods-first answers.\n",
	}
	for name, content := range bootstrap {
		writeBenchmarkFixture(b, &fixture, name, content)
		fixture.bootstrapFiles++
	}

	skillFixtures := map[string]string{
		"skills/literature-check/SKILL.md": "---\nname: literature-check\ndescription: Verify literature claims against primary sources.\n---\n\n# Literature Check\n\nRecord identifiers and distinguish verified claims from hypotheses.\n",
		"skills/assay-review/SKILL.md":     "---\nname: assay-review\ndescription: Review assay design, controls, and endpoints.\n---\n\n# Assay Review\n\nCheck controls, replication, blinding, and endpoint definitions.\n",
	}
	for name, content := range skillFixtures {
		writeBenchmarkFixture(b, &fixture, name, content)
		fixture.skillCount++
	}

	memory := "# Project Memory\n\nThe canonical assay has vehicle and untreated controls. Blinding is not documented.\n"
	writeBenchmarkFixture(b, &fixture, "memory/MEMORY.md", memory)
	fixture.memoryBytes = len(memory)
	writeBenchmarkFixture(b, &fixture, "hooks.yaml", "enabled: false\naudit:\n  enabled: false\n")
	fixture.policyFiles++
	return fixture
}

func writeBenchmarkFixture(b *testing.B, fixture *benchmarkWorkspace, name, content string) {
	b.Helper()
	path := filepath.Join(fixture.path, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatalf("create benchmark fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		b.Fatalf("write benchmark fixture %s: %v", name, err)
	}
	fixture.fileCount++
	fixture.contentBytes += len(content)
}

func reportWorkspaceMetrics(b *testing.B, fixture benchmarkWorkspace) {
	b.Helper()
	b.ReportMetric(float64(fixture.bootstrapFiles), "bootstrap-files")
	b.ReportMetric(float64(fixture.contentBytes), "fixture-bytes")
	b.ReportMetric(float64(fixture.fileCount), "fixture-files")
	b.ReportMetric(float64(fixture.memoryBytes), "memory-bytes")
	b.ReportMetric(float64(fixture.policyFiles), "policy-files")
	b.ReportMetric(float64(fixture.skillCount), "skills")
}

func benchmarkHistory() []providers.Message {
	return []providers.Message{
		{Role: "user", Content: "Summarize the assay setup."},
		{Role: "assistant", Content: "The assay uses three treatment groups."},
		{Role: "user", Content: "What was the primary endpoint?"},
		{Role: "assistant", Content: "The primary endpoint was cell viability."},
		{Role: "user", Content: "List the controls."},
		{Role: "assistant", Content: "Vehicle and untreated controls were included."},
		{Role: "user", Content: "Was the analysis blinded?"},
		{Role: "assistant", Content: "The supplied methods do not state that it was blinded."},
		{Role: "user", Content: "Note that limitation."},
		{Role: "assistant", Content: "I will treat blinding as unreported."},
	}
}
