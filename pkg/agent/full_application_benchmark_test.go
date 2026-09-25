package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const benchmarkModelResponseJSON = `{"content":"The control comparison is complete.","tool_calls":[],"finish_reason":"stop","usage":{"prompt_tokens":512,"completion_tokens":9,"total_tokens":521}}`

type benchmarkJSONProvider struct {
	response providers.LLMResponse
}

func newBenchmarkJSONProvider(tb testing.TB) *benchmarkJSONProvider {
	tb.Helper()
	var response providers.LLMResponse
	if err := json.Unmarshal([]byte(benchmarkModelResponseJSON), &response); err != nil {
		tb.Fatalf("decode deterministic model response: %v", err)
	}
	return &benchmarkJSONProvider{response: response}
}

func (provider *benchmarkJSONProvider) Chat(_ context.Context, _ []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]interface{}) (*providers.LLMResponse, error) {
	response := provider.response
	return &response, nil
}

func (provider *benchmarkJSONProvider) GetDefaultModel() string {
	return "benchmark-model"
}

func BenchmarkFullApplicationDeterministicModel(b *testing.B) {
	b.StopTimer()
	workspace := b.TempDir()
	writeFullApplicationBenchmarkFixture(b, workspace)

	previousLogLevel := logger.GetLevel()
	logger.SetLevel(logger.INFO)
	logPath := filepath.Join(b.TempDir(), "sciclaw.jsonl")
	if err := logger.EnableFileLogging(logPath); err != nil {
		b.Fatalf("enable structured logging: %v", err)
	}
	b.Cleanup(func() {
		logger.DisableFileLogging()
		logger.SetLevel(previousLogLevel)
	})

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.SharedWorkspace = workspace
	cfg.Agents.Defaults.Model = "benchmark-model"
	provider := newBenchmarkJSONProvider(b)
	agentLoop := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	b.Cleanup(agentLoop.Stop)

	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		response, err := agentLoop.ProcessDirect(
			context.Background(),
			"Compare the latest result with the prior controls.",
			"benchmark-session-"+strconv.Itoa(iteration),
		)
		if err != nil {
			b.Fatalf("process deterministic full application turn: %v", err)
		}
		if response != "The control comparison is complete." {
			b.Fatalf("response = %q", response)
		}
	}
}

func writeFullApplicationBenchmarkFixture(tb testing.TB, workspace string) {
	tb.Helper()
	files := map[string]string{
		"AGENTS.md":             "# Project Guidance\n\nUse reproducible analyses.\n",
		"IDENTITY.md":           "# Identity\n\nYou are a paired analysis assistant.\n",
		"SOUL.md":               "# Working Style\n\nBe concise and explicit about uncertainty.\n",
		"TOOLS.md":              "# Tools\n\nPrefer typed scientific tools.\n",
		"USER.md":               "# Collaborator\n\nUse methods-first answers.\n",
		"memory/MEMORY.md":      "# Project Memory\n\nVehicle and untreated controls are canonical.\n",
		"hooks.yaml":            "enabled: false\naudit:\n  enabled: false\n",
		"skills/assay/SKILL.md": "---\nname: assay\ndescription: Review assay controls.\n---\n\n# Assay\n\nCheck controls and endpoints.\n",
	}
	for name, content := range files {
		path := filepath.Join(workspace, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			tb.Fatalf("create fixture directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			tb.Fatalf("write fixture %s: %v", name, err)
		}
	}
}
