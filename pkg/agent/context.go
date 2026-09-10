package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/paths"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type ContextBuilder struct {
	mu                         sync.RWMutex
	workspace                  string
	sharedWorkspace            string
	version                    string
	skillsLoader               *skills.SkillsLoader
	memory                     *MemoryStore
	tools                      *tools.ToolRegistry // Direct reference to tool registry
	includePromptToolSummaries bool
	// hostedToolNotes are provider-hosted capabilities that are not local Go
	// tools (for example Codex Responses image_generation). They still need to
	// appear in the system prompt so the model knows they exist.
	hostedToolNotes []string

	// Per-turn sender context (set before each BuildMessages call)
	senderID    string
	senderName  string
	answerTheme string

	staticPrompt         string
	staticPromptSnapshot promptSnapshot
	staticPromptCached   bool
	staticPromptBuiltAt  time.Time
}

const staticPromptRefreshInterval = time.Second

type promptFileMetadata struct {
	path    string
	exists  bool
	size    int64
	modTime int64
}

type promptSnapshot struct {
	files                      []promptFileMetadata
	version                    string
	includePromptToolSummaries bool
	hostedToolNotes            []string
	toolSummaries              []string
}

// SetSenderContext sets sender identity and answer theme for BuildMessages.
// Deprecated: use BuildMessagesForSender to keep per-turn values isolated.
func (cb *ContextBuilder) SetSenderContext(senderID, displayName, theme string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.setSenderContextLocked(senderID, displayName, theme)
}

func (cb *ContextBuilder) setSenderContextLocked(senderID, displayName, theme string) {
	cb.senderID = senderID
	cb.senderName = sanitizeSenderName(displayName)
	cb.answerTheme = theme
}

func sanitizeSenderName(displayName string) string {
	name := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r < 32 {
			return ' '
		}
		return r
	}, strings.TrimSpace(displayName))
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

func getGlobalConfigDir() string {
	return paths.AppHome()
}

func defaultSharedWorkspaceDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "sciclaw")
}

func resolveGlobalSkillsDir(sharedWorkspace string) string {
	sharedRoot := strings.TrimSpace(sharedWorkspace)
	if sharedRoot != "" {
		sharedSkills := filepath.Join(sharedRoot, "global-skills")
		if hasSkillsDir(sharedSkills) {
			return sharedSkills
		}
		// Default to shared workspace path even if skills are missing;
		// onboard/bootstrap should populate this location.
		return sharedSkills
	}

	base := strings.TrimSpace(getGlobalConfigDir())
	if base == "" {
		return ""
	}
	workspaceSkills := filepath.Join(base, "workspace", "skills")
	legacySkills := paths.GlobalSkillsDir()

	// Legacy fallback only when shared workspace is unspecified.
	if hasSkillsDir(workspaceSkills) {
		return workspaceSkills
	}
	if hasSkillsDir(legacySkills) {
		return legacySkills
	}

	// Default to workspace-based legacy path when nothing is present.
	return workspaceSkills
}

func hasSkillsDir(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(path, entry.Name(), "SKILL.md")); err == nil {
			return true
		}
	}
	return false
}

func NewContextBuilder(workspace string, sharedWorkspace ...string) *ContextBuilder {
	// builtin skills: skills directory in current project
	// Use the skills/ directory under the current working directory
	wd, _ := os.Getwd()
	builtinSkillsDir := filepath.Join(wd, "skills")
	sharedRoot := ""
	if len(sharedWorkspace) > 0 {
		sharedRoot = strings.TrimSpace(sharedWorkspace[0])
	}
	if sharedRoot == "" {
		sharedRoot = defaultSharedWorkspaceDir()
	}
	globalSkillsDir := resolveGlobalSkillsDir(sharedRoot)

	return &ContextBuilder{
		workspace:                  workspace,
		sharedWorkspace:            strings.TrimSpace(sharedRoot),
		skillsLoader:               skills.NewSkillsLoader(workspace, globalSkillsDir, builtinSkillsDir),
		memory:                     NewMemoryStore(workspace),
		includePromptToolSummaries: true,
	}
}

// SetToolsRegistry sets the tools registry for dynamic tool summary generation.
func (cb *ContextBuilder) SetToolsRegistry(registry *tools.ToolRegistry) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.tools = registry
	cb.staticPromptCached = false
}

// SetHostedToolNotes records provider-hosted tools that are not in the local
// ToolRegistry but are available on the current provider path.
func (cb *ContextBuilder) SetHostedToolNotes(notes ...string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.hostedToolNotes = nil
	for _, note := range notes {
		note = strings.TrimSpace(note)
		if note == "" {
			continue
		}
		cb.hostedToolNotes = append(cb.hostedToolNotes, note)
	}
	cb.staticPromptCached = false
}

// SetIncludePromptToolSummaries controls whether human-readable tool summaries
// are embedded into the system prompt. When the provider already receives native
// tool schemas, duplicating the tool list in prose wastes prompt tokens.
func (cb *ContextBuilder) SetIncludePromptToolSummaries(include bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.includePromptToolSummaries = include
	cb.staticPromptCached = false
}

// SetVersion sets the runtime version for inclusion in the system prompt.
func (cb *ContextBuilder) SetVersion(v string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.version = v
	cb.staticPromptCached = false
}

func (cb *ContextBuilder) getStaticIdentity() string {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	snapshot := cb.promptSnapshotLocked()
	return cb.buildStaticIdentity(snapshot)
}

func (cb *ContextBuilder) buildStaticIdentity(snapshot promptSnapshot) string {
	workspacePath, _ := filepath.Abs(filepath.Join(cb.workspace))
	runtime := fmt.Sprintf("%s %s, Go %s", runtime.GOOS, runtime.GOARCH, runtime.Version())
	toolsSection := buildToolsSection(snapshot.toolSummaries, snapshot.hostedToolNotes)

	versionStr := snapshot.version
	if versionStr == "" {
		versionStr = "dev"
	}

	return fmt.Sprintf(`# sciClaw v%s

You are sciClaw v%s, an autonomous paired-scientist execution assistant.

## Runtime
%s

## Workspace
Your workspace is at: %s
- Memory: %s/memory/MEMORY.md
- Skills: %s/skills/{skill-name}/SKILL.md

%s

## Important Rules

1. **ALWAYS use tools** - When you need to perform an action (execute commands, read/write files, run searches, etc.), you MUST call the appropriate tool. Do NOT just say you'll do it or pretend to do it.

2. **Be scientifically rigorous** - Distinguish hypotheses from verified findings and cite evidence paths.

3. **Memory** - Use the remember tool for curated long-term memory. Do not write %s/memory/MEMORY.md with generic file tools. Long-term memory is only for durable project knowledge that changes future work: method decisions, data provenance, project conventions, canonical artifacts, known recurring issues, open questions, and collaborator preferences. Routine successful runs, file sizes, row counts, validation summaries, and command transcripts belong in session history, artifacts, jobs, hook audit logs, or output files.
   - **Long-term** (%s/memory/MEMORY.md): curated durable context injected across sessions.
   - **Daily notes** (%s/memory/YYYYMM/YYYYMMDD.md): sparse short-lived context that is still useful for the next few days. Do not use daily notes as a substitute execution log; they are loaded for only the last 3 days.

4. **Reproducibility** - Prefer idempotent actions and report assumptions/uncertainty.

5. **PubMed-first verification** - For citation checks, PMID lookup, and PubMed literature verification, start with the dedicated `+"`pubmed_search`"+` and `+"`pubmed_fetch`"+` tools. Use raw `+"`exec`"+` with the installed `+"`pubmed`"+` CLI only for advanced PubMed flags not covered by the typed tools. Do not start with `+"`web_fetch`"+` on PubMed or publisher pages when the task is bibliographic verification.`,
		versionStr, versionStr, runtime, workspacePath, workspacePath, workspacePath, toolsSection, workspacePath, workspacePath, workspacePath)
}

func buildToolsSection(localSummaries, hostedToolNotes []string) string {
	if len(localSummaries) == 0 && len(hostedToolNotes) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Available Tools\n\n")
	sb.WriteString("**CRITICAL**: You MUST use tools to perform actions. Do NOT pretend to execute commands or schedule tasks.\n\n")
	if len(localSummaries) > 0 {
		sb.WriteString("You have access to the following tools:\n\n")
		for _, s := range localSummaries {
			sb.WriteString(s)
			sb.WriteString("\n")
		}
	}
	if len(hostedToolNotes) > 0 {
		if len(localSummaries) > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("### Provider-hosted tools\n\n")
		for _, note := range hostedToolNotes {
			sb.WriteString("- ")
			sb.WriteString(note)
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

func (cb *ContextBuilder) BuildSystemPrompt() string {
	cb.mu.RLock()
	senderName := cb.senderName
	answerTheme := cb.answerTheme
	cb.mu.RUnlock()
	return cb.buildSystemPrompt(senderName, answerTheme)
}

func (cb *ContextBuilder) buildSystemPrompt(senderName, answerTheme string) string {
	staticPrompt := cb.getStaticPrompt()
	dynamicParts := []string{currentTimeSection()}
	if answerTheme != "" {
		userSection := "## Active User\n"
		if senderName != "" {
			userSection += fmt.Sprintf("Name: %s\n", senderName)
		}
		userSection += fmt.Sprintf("Answer Theme: %s\n\nIMPORTANT: Follow the %s theme rules from the Answer Themes section above.",
			answerTheme, answerTheme)
		dynamicParts = append(dynamicParts, userSection)
	}
	return staticPrompt + "\n\n---\n\n" + strings.Join(dynamicParts, "\n\n---\n\n")
}

func (cb *ContextBuilder) getStaticPrompt() string {
	now := time.Now()
	cb.mu.RLock()
	if cb.staticPromptCached && now.Sub(cb.staticPromptBuiltAt) < staticPromptRefreshInterval {
		snapshot := cb.promptSnapshotLocked()
		if promptSnapshotsEqual(cb.staticPromptSnapshot, snapshot) {
			prompt := cb.staticPrompt
			cb.mu.RUnlock()
			return prompt
		}
	}
	cb.mu.RUnlock()

	cb.mu.Lock()
	defer cb.mu.Unlock()
	now = time.Now()
	if cb.staticPromptCached && now.Sub(cb.staticPromptBuiltAt) < staticPromptRefreshInterval {
		snapshot := cb.promptSnapshotLocked()
		if promptSnapshotsEqual(cb.staticPromptSnapshot, snapshot) {
			return cb.staticPrompt
		}
	}
	snapshot := cb.promptSnapshotLocked()
	cb.staticPrompt = cb.buildStaticPromptLocked(snapshot)
	cb.staticPromptSnapshot = snapshot
	cb.staticPromptCached = true
	cb.staticPromptBuiltAt = now
	return cb.staticPrompt
}

func currentTimeSection() string {
	return "## Current Time\n" + time.Now().Format("2006-01-02 15:04 (Monday)")
}

func (cb *ContextBuilder) buildStaticPromptLocked(snapshot promptSnapshot) string {
	parts := []string{}
	parts = append(parts, cb.buildStaticIdentity(snapshot))

	// Bootstrap files
	bootstrapContent := cb.LoadBootstrapFiles()
	if bootstrapContent != "" {
		parts = append(parts, bootstrapContent)
	}

	// Skills - show summary, AI can read full content with read_file tool
	skillsSummary := cb.skillsLoader.BuildSkillsSummary()
	if skillsSummary != "" {
		parts = append(parts, fmt.Sprintf(`# Skills

The following skills extend your capabilities. To use a skill, read its SKILL.md file using the read_file tool.

%s`, skillsSummary))
	}

	// Memory context
	memoryContext := cb.memory.GetMemoryContext()
	if memoryContext != "" {
		parts = append(parts, "# Memory\n\n"+memoryContext)
	}

	// Join with "---" separator
	return strings.Join(parts, "\n\n---\n\n")
}

func (cb *ContextBuilder) promptSnapshotLocked() promptSnapshot {
	snapshot := promptSnapshot{
		version:                    cb.version,
		includePromptToolSummaries: cb.includePromptToolSummaries,
		hostedToolNotes:            append([]string(nil), cb.hostedToolNotes...),
	}
	if cb.includePromptToolSummaries && cb.tools != nil {
		snapshot.toolSummaries = cb.tools.GetSummaries()
	}

	primaryWorkspace := filepath.Clean(cb.workspace)
	fallbackWorkspace := filepath.Clean(cb.sharedWorkspace)
	for _, filename := range []string{"AGENTS.md", "SOUL.md", "USER.md", "IDENTITY.md", "TOOLS.md"} {
		snapshot.files = append(snapshot.files, statPromptFile(filepath.Join(primaryWorkspace, filename)))
		if fallbackWorkspace != "" && fallbackWorkspace != "." && fallbackWorkspace != primaryWorkspace {
			snapshot.files = append(snapshot.files, statPromptFile(filepath.Join(fallbackWorkspace, filename)))
		}
	}
	snapshot.files = append(snapshot.files, statPromptFile(cb.memory.memoryFile))
	for _, dailyFile := range cb.memory.recentDailyFiles(3) {
		snapshot.files = append(snapshot.files, statPromptFile(dailyFile))
	}
	for _, root := range cb.skillsLoader.Roots() {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		snapshot.files = append(snapshot.files, statPromptFile(root))
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				snapshot.files = append(snapshot.files, statPromptFile(filepath.Join(root, entry.Name(), "SKILL.md")))
			}
		}
	}
	sort.Slice(snapshot.files, func(i, j int) bool { return snapshot.files[i].path < snapshot.files[j].path })
	return snapshot
}

func statPromptFile(path string) promptFileMetadata {
	metadata := promptFileMetadata{path: filepath.Clean(path)}
	info, err := os.Stat(path)
	if err == nil {
		metadata.exists = true
		metadata.size = info.Size()
		metadata.modTime = info.ModTime().UnixNano()
	}
	return metadata
}

func promptSnapshotsEqual(left, right promptSnapshot) bool {
	if left.version != right.version || left.includePromptToolSummaries != right.includePromptToolSummaries ||
		!stringSlicesEqual(left.hostedToolNotes, right.hostedToolNotes) ||
		!stringSlicesEqual(left.toolSummaries, right.toolSummaries) || len(left.files) != len(right.files) {
		return false
	}
	for i := range left.files {
		if left.files[i] != right.files[i] {
			return false
		}
	}
	return true
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (cb *ContextBuilder) invalidateStaticPrompt() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.staticPromptCached = false
}

func (cb *ContextBuilder) LoadBootstrapFiles() string {
	bootstrapFiles := []string{
		"AGENTS.md",
		"SOUL.md",
		"USER.md",
		"IDENTITY.md",
		"TOOLS.md",
	}

	primaryWorkspace := filepath.Clean(cb.workspace)
	fallbackWorkspace := filepath.Clean(cb.sharedWorkspace)

	var result strings.Builder
	for _, filename := range bootstrapFiles {
		candidates := []string{filepath.Join(primaryWorkspace, filename)}
		if fallbackWorkspace != "" && fallbackWorkspace != "." && fallbackWorkspace != primaryWorkspace {
			candidates = append(candidates, filepath.Join(fallbackWorkspace, filename))
		}

		for _, filePath := range candidates {
			if data, err := os.ReadFile(filePath); err == nil {
				result.WriteString(fmt.Sprintf("## %s\n\n%s\n\n", filename, string(data)))
				break
			}
		}
	}

	return result.String()
}

func (cb *ContextBuilder) BuildMessages(history []providers.Message, summary string, currentMessage string, media []string, channel, chatID string) []providers.Message {
	cb.mu.RLock()
	senderName := cb.senderName
	answerTheme := cb.answerTheme
	cb.mu.RUnlock()
	return cb.buildMessages(history, summary, currentMessage, media, channel, chatID, senderName, answerTheme)
}

// BuildMessagesForSender builds messages with isolated per-turn sender values.
func (cb *ContextBuilder) BuildMessagesForSender(history []providers.Message, summary string, currentMessage string, media []string, channel, chatID, senderID, displayName, theme string) []providers.Message {
	return cb.buildMessages(history, summary, currentMessage, media, channel, chatID, sanitizeSenderName(displayName), theme)
}

func (cb *ContextBuilder) buildMessages(history []providers.Message, summary string, currentMessage string, media []string, channel, chatID, senderName, answerTheme string) []providers.Message {
	messages := []providers.Message{}

	systemPrompt := cb.buildSystemPrompt(senderName, answerTheme)

	// Add Current Session info if provided
	if channel != "" && chatID != "" {
		systemPrompt += fmt.Sprintf("\n\n## Current Session\nChannel: %s\nChat ID: %s", channel, chatID)
	}

	// Log system prompt summary for debugging (debug mode only)
	logger.DebugCF("agent", "System prompt built",
		map[string]interface{}{
			"total_chars":   len(systemPrompt),
			"total_lines":   strings.Count(systemPrompt, "\n") + 1,
			"section_count": strings.Count(systemPrompt, "\n\n---\n\n") + 1,
		})

	// Log preview of system prompt (avoid logging huge content)
	preview := systemPrompt
	if len(preview) > 500 {
		preview = preview[:500] + "... (truncated)"
	}
	logger.DebugCF("agent", "System prompt preview",
		map[string]interface{}{
			"preview": preview,
		})

	if summary != "" {
		systemPrompt += "\n\n## Summary of Previous Conversation\n\n" + summary
	}

	history, removed := trimLeadingOrphanedToolMessages(history)
	for i := 0; i < removed; i++ {
		logger.DebugCF("agent", "Removing orphaned tool message from history to prevent LLM error",
			map[string]interface{}{"role": "tool"})
	}

	messages = append(messages, providers.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	messages = append(messages, history...)

	messages = append(messages, providers.Message{
		Role:    "user",
		Content: currentMessage,
	})

	return messages
}

func trimLeadingOrphanedToolMessages(history []providers.Message) ([]providers.Message, int) {
	removed := 0
	for len(history) > 0 && history[0].Role == "tool" {
		history = history[1:]
		removed++
	}
	return history, removed
}

func (cb *ContextBuilder) AddToolResult(messages []providers.Message, toolCallID, toolName, result string) []providers.Message {
	messages = append(messages, providers.Message{
		Role:       "tool",
		Content:    result,
		ToolCallID: toolCallID,
	})
	return messages
}

func (cb *ContextBuilder) AddAssistantMessage(messages []providers.Message, content string, toolCalls []map[string]interface{}) []providers.Message {
	msg := providers.Message{
		Role:    "assistant",
		Content: content,
	}
	// Always add assistant message, whether or not it has tool calls
	messages = append(messages, msg)
	return messages
}

func (cb *ContextBuilder) loadSkills() string {
	allSkills := cb.skillsLoader.ListSkills()
	if len(allSkills) == 0 {
		return ""
	}

	var skillNames []string
	for _, s := range allSkills {
		skillNames = append(skillNames, s.Name)
	}

	content := cb.skillsLoader.LoadSkillsForContext(skillNames)
	if content == "" {
		return ""
	}

	return "# Skill Definitions\n\n" + content
}

// GetSkillsInfo returns information about loaded skills.
func (cb *ContextBuilder) GetSkillsInfo() map[string]interface{} {
	allSkills := cb.skillsLoader.ListSkills()
	skillNames := make([]string, 0, len(allSkills))
	for _, s := range allSkills {
		skillNames = append(skillNames, s.Name)
	}
	return map[string]interface{}{
		"total":     len(allSkills),
		"available": len(allSkills),
		"names":     skillNames,
	}
}
