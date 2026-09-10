package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestSystemPromptCacheKeepsUserContextDynamic(t *testing.T) {
	cb := NewContextBuilder(t.TempDir(), "")

	alice := cb.BuildMessagesForSender(nil, "", "hello", nil, "cli", "one", "alice", "Alice", "formal")[0].Content
	bob := cb.BuildMessagesForSender(nil, "", "hello", nil, "cli", "two", "bob", "Bob", "brief")[0].Content
	plain := cb.BuildMessagesForSender(nil, "", "hello", nil, "cli", "three", "", "", "")[0].Content

	for prompt, expected := range map[string][2]string{
		alice: {"Name: Alice", "Name: Bob"},
		bob:   {"Name: Bob", "Name: Alice"},
	} {
		if !strings.Contains(prompt, expected[0]) || strings.Contains(prompt, expected[1]) {
			t.Fatalf("dynamic user context mismatch: included=%q excluded=%q", expected[0], expected[1])
		}
	}
	if strings.Contains(plain, "## Active User") {
		t.Fatalf("user theme leaked into unthemed prompt: %q", plain)
	}
	if staticPromptPrefix(alice) != staticPromptPrefix(bob) || staticPromptPrefix(alice) != staticPromptPrefix(plain) {
		t.Fatal("per-turn user context changed the provider-cacheable prefix")
	}
	if strings.Contains(cb.staticPrompt, "## Current Time") || strings.Contains(cb.staticPrompt, "## Active User") {
		t.Fatal("dynamic current-time or user content entered the static prompt cache")
	}
}

func TestSystemPromptCacheInvalidatesBootstrapCandidates(t *testing.T) {
	workspace := t.TempDir()
	shared := t.TempDir()
	writePromptTestFile(t, filepath.Join(shared, "AGENTS.md"), "shared bootstrap")
	cb := NewContextBuilder(workspace, shared)

	assertPromptContains(t, cb.BuildSystemPrompt(), "shared bootstrap")
	writePromptTestFile(t, filepath.Join(shared, "AGENTS.md"), "shared bootstrap edited")
	assertPromptContains(t, cb.BuildSystemPrompt(), "shared bootstrap edited")
	primary := filepath.Join(workspace, "AGENTS.md")
	writePromptTestFile(t, primary, "primary bootstrap created")
	assertPromptContains(t, cb.BuildSystemPrompt(), "primary bootstrap created")
	writePromptTestFile(t, primary, "primary bootstrap edited with more bytes")
	assertPromptContains(t, cb.BuildSystemPrompt(), "primary bootstrap edited with more bytes")
	if err := os.Remove(primary); err != nil {
		t.Fatalf("remove primary bootstrap: %v", err)
	}
	assertPromptContains(t, cb.BuildSystemPrompt(), "shared bootstrap")
}

func TestSystemPromptCacheRefreshesSameMetadataEditsWithinBound(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "AGENTS.md")
	writePromptTestFile(t, path, "alpha")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat bootstrap: %v", err)
	}
	cb := NewContextBuilder(workspace, "")
	assertPromptContains(t, cb.BuildSystemPrompt(), "alpha")

	writePromptTestFile(t, path, "bravo")
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("preserve bootstrap mtime: %v", err)
	}
	assertPromptContains(t, cb.BuildSystemPrompt(), "alpha")
	cb.mu.Lock()
	cb.staticPromptBuiltAt = time.Now().Add(-staticPromptRefreshInterval)
	cb.mu.Unlock()
	assertPromptContains(t, cb.BuildSystemPrompt(), "bravo")
}

func TestSystemPromptCacheInvalidatesMemoryFiles(t *testing.T) {
	workspace := t.TempDir()
	cb := NewContextBuilder(workspace, "")
	longTerm := filepath.Join(workspace, "memory", "MEMORY.md")
	writePromptTestFile(t, longTerm, "long-term memory one")
	assertPromptContains(t, cb.BuildSystemPrompt(), "long-term memory one")
	writePromptTestFile(t, longTerm, "long-term memory two with more bytes")
	assertPromptContains(t, cb.BuildSystemPrompt(), "long-term memory two with more bytes")

	today := time.Now().Format("20060102")
	daily := filepath.Join(workspace, "memory", today[:6], today+".md")
	writePromptTestFile(t, daily, "daily memory created")
	assertPromptContains(t, cb.BuildSystemPrompt(), "daily memory created")
	if err := os.Remove(daily); err != nil {
		t.Fatalf("remove daily memory: %v", err)
	}
	if prompt := cb.BuildSystemPrompt(); strings.Contains(prompt, "daily memory created") {
		t.Fatal("deleted daily memory remained in cached prompt")
	}
}

func TestSystemPromptCacheInvalidatesOnDailyRollover(t *testing.T) {
	workspace := t.TempDir()
	cb := NewContextBuilder(workspace, "")
	now := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.Local)
	cb.memory.now = func() time.Time { return now }
	agingDate := now.AddDate(0, 0, -2).Format("20060102")
	agingPath := filepath.Join(workspace, "memory", agingDate[:6], agingDate+".md")
	writePromptTestFile(t, agingPath, "aging daily memory")
	assertPromptContains(t, cb.BuildSystemPrompt(), "aging daily memory")

	now = now.AddDate(0, 0, 1)
	if prompt := cb.BuildSystemPrompt(); strings.Contains(prompt, "aging daily memory") {
		t.Fatal("daily note remained cached after rolling outside the three-day window")
	}
}

func TestSystemPromptCacheInvalidatesSkillRoots(t *testing.T) {
	workspace := t.TempDir()
	globalRoot := t.TempDir()
	builtinRoot := t.TempDir()
	globalSkill := filepath.Join(globalRoot, "analysis", "SKILL.md")
	writePromptTestFile(t, globalSkill, "---\nname: analysis\ndescription: global skill one\n---\n")
	cb := NewContextBuilder(workspace, "")
	cb.skillsLoader = skills.NewSkillsLoader(workspace, globalRoot, builtinRoot)

	assertPromptContains(t, cb.BuildSystemPrompt(), "global skill one")
	writePromptTestFile(t, globalSkill, "---\nname: analysis\ndescription: global skill edited with more bytes\n---\n")
	assertPromptContains(t, cb.BuildSystemPrompt(), "global skill edited with more bytes")

	workspaceSkill := filepath.Join(workspace, "skills", "analysis", "SKILL.md")
	writePromptTestFile(t, workspaceSkill, "---\nname: analysis\ndescription: workspace skill created\n---\n")
	prompt := cb.BuildSystemPrompt()
	assertPromptContains(t, prompt, "workspace skill created")
	if strings.Contains(prompt, "global skill edited") {
		t.Fatal("workspace skill did not shadow cached global skill")
	}
	if err := os.Remove(workspaceSkill); err != nil {
		t.Fatalf("remove workspace skill: %v", err)
	}
	assertPromptContains(t, cb.BuildSystemPrompt(), "global skill edited with more bytes")
}

func TestSystemPromptCacheInvalidatesMutableInputs(t *testing.T) {
	workspace := t.TempDir()
	cb := NewContextBuilder(workspace, "")
	cb.SetVersion("one")
	assertPromptContains(t, cb.BuildSystemPrompt(), "# sciClaw vone")
	cb.SetVersion("two")
	assertPromptContains(t, cb.BuildSystemPrompt(), "# sciClaw vtwo")

	cb.SetHostedToolNotes("hosted note one")
	assertPromptContains(t, cb.BuildSystemPrompt(), "hosted note one")
	cb.SetHostedToolNotes("hosted note two")
	assertPromptContains(t, cb.BuildSystemPrompt(), "hosted note two")

	registry := tools.NewToolRegistry()
	registry.Register(tools.NewWordCountTool(workspace, true))
	cb.SetToolsRegistry(registry)
	cb.SetIncludePromptToolSummaries(true)
	assertPromptContains(t, cb.BuildSystemPrompt(), "`word_count`")
	registry.Register(tools.NewWeatherForecastTool())
	assertPromptContains(t, cb.BuildSystemPrompt(), "`weather_forecast`")
	cb.SetIncludePromptToolSummaries(false)
	if prompt := cb.BuildSystemPrompt(); strings.Contains(prompt, "`word_count`") {
		t.Fatal("disabled local tool summaries remained in cached prompt")
	}
}

func TestSystemPromptCacheConcurrentBuildsDoNotLeakUsers(t *testing.T) {
	cb := NewContextBuilder(t.TempDir(), "")
	const workers = 32
	var waitGroup sync.WaitGroup
	errors := make(chan error, workers)
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		for iteration := 0; iteration < 100; iteration++ {
			cb.SetVersion(fmt.Sprintf("concurrent-%d", iteration))
		}
	}()
	for worker := 0; worker < workers; worker++ {
		worker := worker
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			name := fmt.Sprintf("user-%d", worker)
			theme := fmt.Sprintf("theme-%d", worker)
			for iteration := 0; iteration < 20; iteration++ {
				prompt := cb.BuildMessagesForSender(nil, "", "hello", nil, "cli", name, name, name, theme)[0].Content
				if !strings.Contains(prompt, "Name: "+name+"\n") || !strings.Contains(prompt, "Answer Theme: "+theme+"\n") {
					errors <- fmt.Errorf("prompt for %s contained another sender", name)
					return
				}
			}
		}()
	}
	waitGroup.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	cb.SetVersion("final")
	assertPromptContains(t, cb.BuildSystemPrompt(), "# sciClaw vfinal")
}

func staticPromptPrefix(prompt string) string {
	prefix, _, _ := strings.Cut(prompt, "\n\n---\n\n## Current Time\n")
	return prefix
}

func writePromptTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir prompt source: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write prompt source: %v", err)
	}
}

func assertPromptContains(t *testing.T, prompt, expected string) {
	t.Helper()
	if !strings.Contains(prompt, expected) {
		t.Fatalf("prompt missing %q", expected)
	}
}
