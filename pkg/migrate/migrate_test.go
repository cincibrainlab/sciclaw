package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/paths"
)

func TestCamelToSnake(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "apiKey", "api_key"},
		{"two words", "apiBase", "api_base"},
		{"three words", "maxToolIterations", "max_tool_iterations"},
		{"already snake", "api_key", "api_key"},
		{"single word", "enabled", "enabled"},
		{"all lower", "model", "model"},
		{"consecutive caps", "apiURL", "api_url"},
		{"starts upper", "Model", "model"},
		{"bridge url", "bridgeUrl", "bridge_url"},
		{"client id", "clientId", "client_id"},
		{"app secret", "appSecret", "app_secret"},
		{"verification token", "verificationToken", "verification_token"},
		{"allow from", "allowFrom", "allow_from"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := camelToSnake(tt.input)
			if got != tt.want {
				t.Errorf("camelToSnake(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestConvertKeysToSnake(t *testing.T) {
	input := map[string]interface{}{
		"apiKey":  "test-key",
		"apiBase": "https://example.com",
		"nested": map[string]interface{}{
			"maxTokens": float64(8192),
			"allowFrom": []interface{}{"user1", "user2"},
			"deeperLevel": map[string]interface{}{
				"clientId": "abc",
			},
		},
	}

	result := convertKeysToSnake(input)
	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("expected map[string]interface{}")
	}

	if _, ok := m["api_key"]; !ok {
		t.Error("expected key 'api_key' after conversion")
	}
	if _, ok := m["api_base"]; !ok {
		t.Error("expected key 'api_base' after conversion")
	}

	nested, ok := m["nested"].(map[string]interface{})
	if !ok {
		t.Fatal("expected nested map")
	}
	if _, ok := nested["max_tokens"]; !ok {
		t.Error("expected key 'max_tokens' in nested map")
	}
	if _, ok := nested["allow_from"]; !ok {
		t.Error("expected key 'allow_from' in nested map")
	}

	deeper, ok := nested["deeper_level"].(map[string]interface{})
	if !ok {
		t.Fatal("expected deeper_level map")
	}
	if _, ok := deeper["client_id"]; !ok {
		t.Error("expected key 'client_id' in deeper level")
	}
}

func TestLoadOpenClawConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "openclaw.json")

	openclawConfig := map[string]interface{}{
		"providers": map[string]interface{}{
			"anthropic": map[string]interface{}{
				"apiKey":  "sk-ant-test123",
				"apiBase": "https://api.anthropic.com",
			},
		},
		"agents": map[string]interface{}{
			"defaults": map[string]interface{}{
				"maxTokens": float64(4096),
				"model":     "claude-3-opus",
			},
		},
	}

	data, err := json.Marshal(openclawConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	result, err := LoadOpenClawConfig(configPath)
	if err != nil {
		t.Fatalf("LoadOpenClawConfig: %v", err)
	}

	providers, ok := result["providers"].(map[string]interface{})
	if !ok {
		t.Fatal("expected providers map")
	}
	anthropic, ok := providers["anthropic"].(map[string]interface{})
	if !ok {
		t.Fatal("expected anthropic map")
	}
	if anthropic["api_key"] != "sk-ant-test123" {
		t.Errorf("api_key = %v, want sk-ant-test123", anthropic["api_key"])
	}

	agents, ok := result["agents"].(map[string]interface{})
	if !ok {
		t.Fatal("expected agents map")
	}
	defaults, ok := agents["defaults"].(map[string]interface{})
	if !ok {
		t.Fatal("expected defaults map")
	}
	if defaults["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens = %v, want 4096", defaults["max_tokens"])
	}
}

func TestConvertConfig(t *testing.T) {
	t.Run("providers mapping", func(t *testing.T) {
		data := map[string]interface{}{
			"providers": map[string]interface{}{
				"anthropic": map[string]interface{}{
					"api_key":  "sk-ant-test",
					"api_base": "https://api.anthropic.com",
				},
				"openrouter": map[string]interface{}{
					"api_key": "sk-or-test",
				},
				"groq": map[string]interface{}{
					"api_key": "gsk-test",
				},
			},
		}

		cfg, warnings, err := ConvertConfig(data)
		if err != nil {
			t.Fatalf("ConvertConfig: %v", err)
		}
		if len(warnings) != 0 {
			t.Errorf("expected no warnings, got %v", warnings)
		}
		if cfg.Providers.Anthropic.APIKey != "sk-ant-test" {
			t.Errorf("Anthropic.APIKey = %q, want %q", cfg.Providers.Anthropic.APIKey, "sk-ant-test")
		}
		if cfg.Providers.OpenRouter.APIKey != "sk-or-test" {
			t.Errorf("OpenRouter.APIKey = %q, want %q", cfg.Providers.OpenRouter.APIKey, "sk-or-test")
		}
		if cfg.Providers.Groq.APIKey != "gsk-test" {
			t.Errorf("Groq.APIKey = %q, want %q", cfg.Providers.Groq.APIKey, "gsk-test")
		}
	})

	t.Run("unsupported provider warning", func(t *testing.T) {
		data := map[string]interface{}{
			"providers": map[string]interface{}{
				"deepseek": map[string]interface{}{
					"api_key": "sk-deep-test",
				},
			},
		}

		_, warnings, err := ConvertConfig(data)
		if err != nil {
			t.Fatalf("ConvertConfig: %v", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("expected 1 warning, got %d", len(warnings))
		}
		if warnings[0] != "Provider 'deepseek' not supported in PicoClaw, skipping" {
			t.Errorf("unexpected warning: %s", warnings[0])
		}
	})

	t.Run("channels mapping", func(t *testing.T) {
		data := map[string]interface{}{
			"channels": map[string]interface{}{
				"telegram": map[string]interface{}{
					"enabled":    true,
					"token":      "tg-token-123",
					"allow_from": []interface{}{"user1"},
				},
				"discord": map[string]interface{}{
					"enabled": true,
					"token":   "disc-token-456",
				},
			},
		}

		cfg, _, err := ConvertConfig(data)
		if err != nil {
			t.Fatalf("ConvertConfig: %v", err)
		}
		if !cfg.Channels.Telegram.Enabled {
			t.Error("Telegram should be enabled")
		}
		if cfg.Channels.Telegram.Token != "tg-token-123" {
			t.Errorf("Telegram.Token = %q, want %q", cfg.Channels.Telegram.Token, "tg-token-123")
		}
		if len(cfg.Channels.Telegram.AllowFrom) != 1 || cfg.Channels.Telegram.AllowFrom[0] != "user1" {
			t.Errorf("Telegram.AllowFrom = %v, want [user1]", cfg.Channels.Telegram.AllowFrom)
		}
		if !cfg.Channels.Discord.Enabled {
			t.Error("Discord should be enabled")
		}
	})

	t.Run("unsupported channel warning", func(t *testing.T) {
		data := map[string]interface{}{
			"channels": map[string]interface{}{
				"email": map[string]interface{}{
					"enabled": true,
				},
			},
		}

		_, warnings, err := ConvertConfig(data)
		if err != nil {
			t.Fatalf("ConvertConfig: %v", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("expected 1 warning, got %d", len(warnings))
		}
		if warnings[0] != "Channel 'email' not supported in PicoClaw, skipping" {
			t.Errorf("unexpected warning: %s", warnings[0])
		}
	})

	t.Run("agent defaults", func(t *testing.T) {
		data := map[string]interface{}{
			"agents": map[string]interface{}{
				"defaults": map[string]interface{}{
					"model":               "claude-3-opus",
					"max_tokens":          float64(4096),
					"temperature":         0.5,
					"max_tool_iterations": float64(10),
					"workspace":           "~/.openclaw/workspace",
				},
			},
		}

		cfg, _, err := ConvertConfig(data)
		if err != nil {
			t.Fatalf("ConvertConfig: %v", err)
		}
		if cfg.Agents.Defaults.Model != "claude-3-opus" {
			t.Errorf("Model = %q, want %q", cfg.Agents.Defaults.Model, "claude-3-opus")
		}
		if cfg.Agents.Defaults.MaxTokens != 4096 {
			t.Errorf("MaxTokens = %d, want %d", cfg.Agents.Defaults.MaxTokens, 4096)
		}
		if cfg.Agents.Defaults.Temperature != 0.5 {
			t.Errorf("Temperature = %f, want %f", cfg.Agents.Defaults.Temperature, 0.5)
		}
		if cfg.Agents.Defaults.Workspace != "~/sciclaw/workspaces/default" {
			t.Errorf("Workspace = %q, want %q", cfg.Agents.Defaults.Workspace, "~/sciclaw/workspaces/default")
		}
	})

	t.Run("empty config", func(t *testing.T) {
		data := map[string]interface{}{}

		cfg, warnings, err := ConvertConfig(data)
		if err != nil {
			t.Fatalf("ConvertConfig: %v", err)
		}
		if len(warnings) != 0 {
			t.Errorf("expected no warnings, got %v", warnings)
		}
		if cfg.Agents.Defaults.Model != "gpt-5.2" {
			t.Errorf("default model should be gpt-5.2, got %q", cfg.Agents.Defaults.Model)
		}
	})
}

func TestMergeConfig(t *testing.T) {
	t.Run("fills empty fields", func(t *testing.T) {
		existing := config.DefaultConfig()
		incoming := config.DefaultConfig()
		incoming.Providers.Anthropic.APIKey = "sk-ant-incoming"
		incoming.Providers.OpenRouter.APIKey = "sk-or-incoming"

		result := MergeConfig(existing, incoming)
		if result.Providers.Anthropic.APIKey != "sk-ant-incoming" {
			t.Errorf("Anthropic.APIKey = %q, want %q", result.Providers.Anthropic.APIKey, "sk-ant-incoming")
		}
		if result.Providers.OpenRouter.APIKey != "sk-or-incoming" {
			t.Errorf("OpenRouter.APIKey = %q, want %q", result.Providers.OpenRouter.APIKey, "sk-or-incoming")
		}
	})

	t.Run("preserves existing non-empty fields", func(t *testing.T) {
		existing := config.DefaultConfig()
		existing.Providers.Anthropic.APIKey = "sk-ant-existing"

		incoming := config.DefaultConfig()
		incoming.Providers.Anthropic.APIKey = "sk-ant-incoming"
		incoming.Providers.OpenAI.APIKey = "sk-oai-incoming"

		result := MergeConfig(existing, incoming)
		if result.Providers.Anthropic.APIKey != "sk-ant-existing" {
			t.Errorf("Anthropic.APIKey should be preserved, got %q", result.Providers.Anthropic.APIKey)
		}
		if result.Providers.OpenAI.APIKey != "sk-oai-incoming" {
			t.Errorf("OpenAI.APIKey should be filled, got %q", result.Providers.OpenAI.APIKey)
		}
	})

	t.Run("merges enabled channels", func(t *testing.T) {
		existing := config.DefaultConfig()
		incoming := config.DefaultConfig()
		incoming.Channels.Telegram.Enabled = true
		incoming.Channels.Telegram.Token = "tg-token"

		result := MergeConfig(existing, incoming)
		if !result.Channels.Telegram.Enabled {
			t.Error("Telegram should be enabled after merge")
		}
		if result.Channels.Telegram.Token != "tg-token" {
			t.Errorf("Telegram.Token = %q, want %q", result.Channels.Telegram.Token, "tg-token")
		}
	})

	t.Run("preserves existing enabled channels", func(t *testing.T) {
		existing := config.DefaultConfig()
		existing.Channels.Telegram.Enabled = true
		existing.Channels.Telegram.Token = "existing-token"

		incoming := config.DefaultConfig()
		incoming.Channels.Telegram.Enabled = true
		incoming.Channels.Telegram.Token = "incoming-token"

		result := MergeConfig(existing, incoming)
		if result.Channels.Telegram.Token != "existing-token" {
			t.Errorf("Telegram.Token should be preserved, got %q", result.Channels.Telegram.Token)
		}
	})
}

func TestPlanWorkspaceMigration(t *testing.T) {
	t.Run("copies available files", func(t *testing.T) {
		srcDir := t.TempDir()
		dstDir := t.TempDir()

		os.WriteFile(filepath.Join(srcDir, "AGENTS.md"), []byte("# Agents"), 0644)
		os.WriteFile(filepath.Join(srcDir, "SOUL.md"), []byte("# Soul"), 0644)
		os.WriteFile(filepath.Join(srcDir, "USER.md"), []byte("# User"), 0644)

		actions, err := PlanWorkspaceMigration(srcDir, dstDir, filepath.Join(dstDir, "profile"), false)
		if err != nil {
			t.Fatalf("PlanWorkspaceMigration: %v", err)
		}

		copyCount := 0
		skipCount := 0
		for _, a := range actions {
			if a.Type == ActionCopy {
				copyCount++
			}
			if a.Type == ActionSkip {
				skipCount++
			}
		}
		if copyCount != 3 {
			t.Errorf("expected 3 copies, got %d", copyCount)
		}
		if skipCount != 4 {
			t.Errorf("expected 4 skips (HOOKS.md, IDENTITY.md, TOOLS.md, HEARTBEAT.md), got %d", skipCount)
		}
	})

	t.Run("plans backup for existing destination files", func(t *testing.T) {
		srcDir := t.TempDir()
		dstDir := t.TempDir()
		profileDir := filepath.Join(dstDir, "profile")
		os.MkdirAll(profileDir, 0755)

		os.WriteFile(filepath.Join(srcDir, "AGENTS.md"), []byte("# Agents from OpenClaw"), 0644)
		os.WriteFile(filepath.Join(profileDir, "AGENTS.md"), []byte("# Existing Agents"), 0644)

		actions, err := PlanWorkspaceMigration(srcDir, dstDir, profileDir, false)
		if err != nil {
			t.Fatalf("PlanWorkspaceMigration: %v", err)
		}

		backupCount := 0
		for _, a := range actions {
			if a.Type == ActionBackup && filepath.Base(a.Destination) == "AGENTS.md" {
				backupCount++
			}
		}
		if backupCount != 1 {
			t.Errorf("expected 1 backup action for AGENTS.md, got %d", backupCount)
		}
	})

	t.Run("force still plans backup", func(t *testing.T) {
		srcDir := t.TempDir()
		dstDir := t.TempDir()
		profileDir := filepath.Join(dstDir, "profile")
		os.MkdirAll(profileDir, 0755)

		os.WriteFile(filepath.Join(srcDir, "AGENTS.md"), []byte("# Agents"), 0644)
		os.WriteFile(filepath.Join(profileDir, "AGENTS.md"), []byte("# Existing"), 0644)

		actions, err := PlanWorkspaceMigration(srcDir, dstDir, profileDir, true)
		if err != nil {
			t.Fatalf("PlanWorkspaceMigration: %v", err)
		}

		backupCount := 0
		for _, a := range actions {
			if a.Type == ActionBackup {
				backupCount++
			}
		}
		if backupCount != 1 {
			t.Fatalf("force must not suppress backups, got %d", backupCount)
		}
	})

	t.Run("handles memory directory", func(t *testing.T) {
		srcDir := t.TempDir()
		dstDir := t.TempDir()

		memDir := filepath.Join(srcDir, "memory")
		os.MkdirAll(memDir, 0755)
		os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("# Memory"), 0644)

		actions, err := PlanWorkspaceMigration(srcDir, dstDir, filepath.Join(dstDir, "profile"), false)
		if err != nil {
			t.Fatalf("PlanWorkspaceMigration: %v", err)
		}

		hasCopy := false
		hasDir := false
		for _, a := range actions {
			if a.Type == ActionCopy && filepath.Base(a.Source) == "MEMORY.md" {
				hasCopy = true
			}
			if a.Type == ActionCreateDir {
				hasDir = true
			}
		}
		if !hasCopy {
			t.Error("expected copy action for memory/MEMORY.md")
		}
		if !hasDir {
			t.Error("expected create dir action for memory/")
		}
	})

	t.Run("handles skills directory", func(t *testing.T) {
		srcDir := t.TempDir()
		dstDir := t.TempDir()

		skillDir := filepath.Join(srcDir, "skills", "weather")
		os.MkdirAll(skillDir, 0755)
		os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Weather"), 0644)

		actions, err := PlanWorkspaceMigration(srcDir, dstDir, filepath.Join(dstDir, "profile"), false)
		if err != nil {
			t.Fatalf("PlanWorkspaceMigration: %v", err)
		}

		hasCopy := false
		for _, a := range actions {
			if a.Type == ActionCopy && filepath.Base(a.Source) == "SKILL.md" {
				hasCopy = true
			}
		}
		if !hasCopy {
			t.Error("expected copy action for skills/weather/SKILL.md")
		}
	})

	t.Run("does not copy sessions into visible workspace", func(t *testing.T) {
		srcDir := t.TempDir()
		dstDir := t.TempDir()

		sessionDir := filepath.Join(srcDir, "sessions")
		os.MkdirAll(sessionDir, 0755)
		os.WriteFile(filepath.Join(sessionDir, "2026-02-12.json"), []byte(`{"messages":[]}`), 0644)

		actions, err := PlanWorkspaceMigration(srcDir, dstDir, filepath.Join(dstDir, "profile"), false)
		if err != nil {
			t.Fatalf("PlanWorkspaceMigration: %v", err)
		}

		for _, a := range actions {
			if a.Type == ActionCopy && filepath.Base(a.Source) == "2026-02-12.json" {
				t.Fatalf("unexpected visible session copy: %#v", a)
			}
		}
	})

	t.Run("handles cron directory", func(t *testing.T) {
		srcDir := t.TempDir()
		dstDir := t.TempDir()

		cronDir := filepath.Join(srcDir, "cron")
		os.MkdirAll(cronDir, 0755)
		os.WriteFile(filepath.Join(cronDir, "jobs.json"), []byte(`{"jobs":[]}`), 0644)

		actions, err := PlanWorkspaceMigration(srcDir, dstDir, filepath.Join(dstDir, "profile"), false)
		if err != nil {
			t.Fatalf("PlanWorkspaceMigration: %v", err)
		}

		hasCopy := false
		for _, a := range actions {
			if a.Type == ActionCopy && filepath.Base(a.Source) == "jobs.json" {
				hasCopy = true
			}
		}
		if !hasCopy {
			t.Error("expected copy action for cron/jobs.json")
		}
	})
}

func TestFindOpenClawConfig(t *testing.T) {
	t.Run("finds openclaw.json", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "openclaw.json")
		os.WriteFile(configPath, []byte("{}"), 0644)

		found, err := findOpenClawConfig(tmpDir)
		if err != nil {
			t.Fatalf("findOpenClawConfig: %v", err)
		}
		if found != configPath {
			t.Errorf("found %q, want %q", found, configPath)
		}
	})

	t.Run("falls back to config.json", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.json")
		os.WriteFile(configPath, []byte("{}"), 0644)

		found, err := findOpenClawConfig(tmpDir)
		if err != nil {
			t.Fatalf("findOpenClawConfig: %v", err)
		}
		if found != configPath {
			t.Errorf("found %q, want %q", found, configPath)
		}
	})

	t.Run("prefers openclaw.json over config.json", func(t *testing.T) {
		tmpDir := t.TempDir()
		openclawPath := filepath.Join(tmpDir, "openclaw.json")
		os.WriteFile(openclawPath, []byte("{}"), 0644)
		os.WriteFile(filepath.Join(tmpDir, "config.json"), []byte("{}"), 0644)

		found, err := findOpenClawConfig(tmpDir)
		if err != nil {
			t.Fatalf("findOpenClawConfig: %v", err)
		}
		if found != openclawPath {
			t.Errorf("should prefer openclaw.json, got %q", found)
		}
	})

	t.Run("error when no config found", func(t *testing.T) {
		tmpDir := t.TempDir()

		_, err := findOpenClawConfig(tmpDir)
		if err == nil {
			t.Fatal("expected error when no config found")
		}
	})
}

func TestRewriteWorkspacePath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"default path", "~/.openclaw/workspace", "~/sciclaw/workspaces/default"},
		{"custom path", "/custom/path", "/custom/path"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteWorkspacePath(tt.input)
			if got != tt.want {
				t.Errorf("rewriteWorkspacePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRunDryRun(t *testing.T) {
	openclawHome := t.TempDir()
	picoClawHome := t.TempDir()
	t.Setenv("SCICLAW_HOME", picoClawHome)
	t.Setenv("SCICLAW_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	wsDir := filepath.Join(openclawHome, "workspace")
	os.MkdirAll(wsDir, 0755)
	os.WriteFile(filepath.Join(wsDir, "SOUL.md"), []byte("# Soul"), 0644)
	os.WriteFile(filepath.Join(wsDir, "AGENTS.md"), []byte("# Agents"), 0644)

	configData := map[string]interface{}{
		"providers": map[string]interface{}{
			"anthropic": map[string]interface{}{
				"apiKey": "test-key",
			},
		},
	}
	data, _ := json.Marshal(configData)
	os.WriteFile(filepath.Join(openclawHome, "openclaw.json"), data, 0644)

	opts := Options{
		DryRun:       true,
		OpenClawHome: openclawHome,
	}

	result, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	picoWs := filepath.Join(picoClawHome, "workspaces", "default")
	if _, err := os.Stat(filepath.Join(picoWs, "SOUL.md")); !os.IsNotExist(err) {
		t.Error("dry run should not create files")
	}
	if _, err := os.Stat(paths.ConfigPath()); !os.IsNotExist(err) {
		t.Error("dry run should not create config")
	}

	_ = result
}

func TestSciClawDestinationIgnoresPicoClawHome(t *testing.T) {
	visible := filepath.Join(t.TempDir(), "visible")
	t.Setenv("SCICLAW_HOME", visible)
	t.Setenv("PICOCLAW_HOME", filepath.Join(t.TempDir(), "pico"))

	got := paths.UserHome()
	if got != visible {
		t.Fatalf("UserHome()=%q want %q", got, visible)
	}
}

func TestRunMigratesPicoClawConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SCICLAW_HOME", filepath.Join(home, "sciclaw"))
	t.Setenv("SCICLAW_STATE_HOME", filepath.Join(home, ".sciclaw"))
	legacy := filepath.Join(home, ".picoclaw")
	legacyConfig := config.DefaultConfig()
	legacyConfig.Agents.Defaults.Workspace = filepath.Join(legacy, "workspace")
	legacyConfig.Agents.Defaults.SharedWorkspace = "~/.picoclaw/workspace"
	legacyConfig.Providers.Anthropic.APIKey = "legacy-key"
	if err := config.SaveConfig(filepath.Join(legacy, "config.json"), legacyConfig); err != nil {
		t.Fatal(err)
	}

	result, err := Run(Options{PicoClawHome: legacy, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ConfigMigrated {
		t.Fatal("expected PicoClaw config to migrate")
	}
	migrated, err := config.LoadConfig(paths.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Providers.Anthropic.APIKey != "legacy-key" {
		t.Fatalf("provider credential = %q", migrated.Providers.Anthropic.APIKey)
	}
	if migrated.WorkspacePath() != paths.DefaultWorkspace() || migrated.SharedWorkspacePath() != paths.DefaultWorkspace() {
		t.Fatalf("workspace defaults = %q / %q, want %q", migrated.WorkspacePath(), migrated.SharedWorkspacePath(), paths.DefaultWorkspace())
	}
}

func TestRunPicoClawMigrationRefusesExistingConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SCICLAW_HOME", filepath.Join(home, "sciclaw"))
	t.Setenv("SCICLAW_STATE_HOME", filepath.Join(home, ".sciclaw"))
	legacy := filepath.Join(home, ".picoclaw")
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.json"), []byte(`{"legacy":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ConfigPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigPath(), []byte(`{"authoritative":true}`), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(Options{OpenClawHome: legacy, Force: true}); err == nil || !strings.Contains(err.Error(), "will not overwrite") {
		t.Fatalf("expected destination conflict, got %v", err)
	}
	data, err := os.ReadFile(paths.ConfigPath())
	if err != nil || string(data) != `{"authoritative":true}` {
		t.Fatalf("destination config changed: %q, %v", data, err)
	}
}

func TestRunPicoClawMigrationPreservesSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SCICLAW_HOME", filepath.Join(home, "sciclaw"))
	t.Setenv("SCICLAW_STATE_HOME", filepath.Join(home, ".sciclaw"))
	legacy := filepath.Join(home, ".picoclaw")
	if err := os.MkdirAll(filepath.Join(legacy, "workspace"), 0755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(legacy, "config.json")
	contents := []byte(`{"agents":{"defaults":{"workspace":"~/.picoclaw/workspace"}}}`)
	if err := os.WriteFile(source, contents, 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(Options{PicoClawHome: legacy, Force: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil || string(data) != string(contents) {
		t.Fatalf("PicoClaw config changed: %q, %v", data, err)
	}
	info, err := os.Lstat(legacy)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("PicoClaw home was replaced: %#v, %v", info, err)
	}
}

func TestRunValidatesDestinationRootsBeforeSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	overlap := filepath.Join(home, "sciclaw")
	t.Setenv("SCICLAW_HOME", overlap)
	t.Setenv("SCICLAW_STATE_HOME", overlap)
	if _, err := Run(Options{OpenClawHome: filepath.Join(home, "missing")}); err == nil || !strings.Contains(err.Error(), "destination roots") {
		t.Fatalf("expected destination-root validation before source lookup, got %v", err)
	}
}

func TestRunFullMigration(t *testing.T) {
	openclawHome := t.TempDir()
	picoClawHome := t.TempDir()
	t.Setenv("SCICLAW_HOME", picoClawHome)
	t.Setenv("SCICLAW_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	wsDir := filepath.Join(openclawHome, "workspace")
	os.MkdirAll(wsDir, 0755)
	os.WriteFile(filepath.Join(wsDir, "SOUL.md"), []byte("# Soul from OpenClaw"), 0644)
	os.WriteFile(filepath.Join(wsDir, "AGENTS.md"), []byte("# Agents from OpenClaw"), 0644)
	os.WriteFile(filepath.Join(wsDir, "USER.md"), []byte("# User from OpenClaw"), 0644)
	os.MkdirAll(filepath.Join(wsDir, "sessions"), 0755)
	os.WriteFile(filepath.Join(wsDir, "sessions", "chat.json"), []byte(`{"key":"chat"}`), 0644)

	memDir := filepath.Join(wsDir, "memory")
	os.MkdirAll(memDir, 0755)
	os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("# Memory notes"), 0644)

	configData := map[string]interface{}{
		"providers": map[string]interface{}{
			"anthropic": map[string]interface{}{
				"apiKey": "sk-ant-migrate-test",
			},
			"openrouter": map[string]interface{}{
				"apiKey": "sk-or-migrate-test",
			},
		},
		"channels": map[string]interface{}{
			"telegram": map[string]interface{}{
				"enabled": true,
				"token":   "tg-migrate-test",
			},
		},
	}
	data, _ := json.Marshal(configData)
	os.WriteFile(filepath.Join(openclawHome, "openclaw.json"), data, 0644)

	opts := Options{
		Force:        true,
		OpenClawHome: openclawHome,
	}

	result, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	picoWs := filepath.Join(picoClawHome, "workspaces", "default")

	profileDir := filepath.Join(picoClawHome, "profile")
	soulData, err := os.ReadFile(filepath.Join(profileDir, "SOUL.md"))
	if err != nil {
		t.Fatalf("reading SOUL.md: %v", err)
	}
	if string(soulData) != "# Soul from OpenClaw" {
		t.Errorf("SOUL.md content = %q, want %q", string(soulData), "# Soul from OpenClaw")
	}

	agentsData, err := os.ReadFile(filepath.Join(profileDir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.md: %v", err)
	}
	if string(agentsData) != "# Agents from OpenClaw" {
		t.Errorf("AGENTS.md content = %q", string(agentsData))
	}

	memData, err := os.ReadFile(filepath.Join(picoWs, "memory", "MEMORY.md"))
	if err != nil {
		t.Fatalf("reading memory/MEMORY.md: %v", err)
	}
	if string(memData) != "# Memory notes" {
		t.Errorf("MEMORY.md content = %q", string(memData))
	}
	managedSession := filepath.Join(paths.WorkspaceStateDir(picoWs), "sessions", "chat.json")
	if _, err := os.Stat(managedSession); err != nil {
		t.Fatalf("reading migrated managed session: %v", err)
	}
	if info, err := os.Stat(managedSession); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0600 {
		t.Fatalf("managed session mode=%#o want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(picoWs, "sessions")); !os.IsNotExist(err) {
		t.Fatal("sessions must not be imported into the visible workspace")
	}

	picoConfig, err := config.LoadConfig(paths.ConfigPath())
	if err != nil {
		t.Fatalf("loading PicoClaw config: %v", err)
	}
	if picoConfig.Providers.Anthropic.APIKey != "sk-ant-migrate-test" {
		t.Errorf("Anthropic.APIKey = %q, want %q", picoConfig.Providers.Anthropic.APIKey, "sk-ant-migrate-test")
	}
	if picoConfig.Providers.OpenRouter.APIKey != "sk-or-migrate-test" {
		t.Errorf("OpenRouter.APIKey = %q, want %q", picoConfig.Providers.OpenRouter.APIKey, "sk-or-migrate-test")
	}
	if !picoConfig.Channels.Telegram.Enabled {
		t.Error("Telegram should be enabled")
	}
	if picoConfig.Channels.Telegram.Token != "tg-migrate-test" {
		t.Errorf("Telegram.Token = %q, want %q", picoConfig.Channels.Telegram.Token, "tg-migrate-test")
	}
	if picoConfig.WorkspacePath() != picoWs || picoConfig.SharedWorkspacePath() != picoWs {
		t.Fatalf("imported workspace defaults = %q / %q, want %q", picoConfig.WorkspacePath(), picoConfig.SharedWorkspacePath(), picoWs)
	}

	if result.FilesCopied < 3 {
		t.Errorf("expected at least 3 files copied, got %d", result.FilesCopied)
	}
	if !result.ConfigMigrated {
		t.Error("config should have been migrated")
	}
	if len(result.Errors) > 0 {
		t.Errorf("expected no errors, got %v", result.Errors)
	}
}

func TestRunRefusesExistingAuthoritativeConfig(t *testing.T) {
	openclawHome := t.TempDir()
	visibleHome := t.TempDir()
	t.Setenv("SCICLAW_HOME", visibleHome)
	t.Setenv("SCICLAW_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	if err := os.WriteFile(filepath.Join(openclawHome, "openclaw.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	destination := paths.ConfigPath()
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte(`{"authoritative":true}`), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(Options{Force: true, OpenClawHome: openclawHome}); err == nil {
		t.Fatal("expected existing authoritative config to block import")
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != `{"authoritative":true}` {
		t.Fatalf("authoritative config changed: %q, %v", data, err)
	}
}

func TestRunReturnsErrorWhenExecutionHasCopyFailure(t *testing.T) {
	openclawHome := t.TempDir()
	visibleHome := t.TempDir()
	t.Setenv("SCICLAW_HOME", visibleHome)
	t.Setenv("SCICLAW_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	workspace := filepath.Join(openclawHome, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("source"), 0644); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(visibleHome, "profile", "AGENTS.md")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}

	result, err := Run(Options{WorkspaceOnly: true, OpenClawHome: openclawHome, Force: true})
	if err == nil {
		t.Fatal("expected copy failure to produce Run error")
	}
	if result == nil || len(result.Errors) == 0 {
		t.Fatalf("expected partial result errors, got %#v", result)
	}
}

func TestRunOpenClawNotFound(t *testing.T) {
	t.Setenv("SCICLAW_HOME", t.TempDir())
	opts := Options{
		OpenClawHome: "/nonexistent/path/to/openclaw",
	}

	_, err := Run(opts)
	if err == nil {
		t.Fatal("expected error when OpenClaw not found")
	}
}

func TestRunMutuallyExclusiveFlags(t *testing.T) {
	opts := Options{
		ConfigOnly:    true,
		WorkspaceOnly: true,
	}

	_, err := Run(opts)
	if err == nil {
		t.Fatal("expected error for mutually exclusive flags")
	}
}

func TestBackupFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.md")
	os.WriteFile(filePath, []byte("original content"), 0644)

	if err := backupFile(filePath); err != nil {
		t.Fatalf("backupFile: %v", err)
	}

	bakPath := filePath + ".bak"
	bakData, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}
	if string(bakData) != "original content" {
		t.Errorf("backup content = %q, want %q", string(bakData), "original content")
	}
}

func TestCopyFile(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "src.md")
	dstPath := filepath.Join(tmpDir, "dst.md")

	os.WriteFile(srcPath, []byte("file content"), 0644)

	if err := copyFile(srcPath, dstPath); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	data, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("reading copy: %v", err)
	}
	if string(data) != "file content" {
		t.Errorf("copy content = %q, want %q", string(data), "file content")
	}
}

func TestRunConfigOnly(t *testing.T) {
	openclawHome := t.TempDir()
	picoClawHome := t.TempDir()
	t.Setenv("SCICLAW_HOME", picoClawHome)

	wsDir := filepath.Join(openclawHome, "workspace")
	os.MkdirAll(wsDir, 0755)
	os.WriteFile(filepath.Join(wsDir, "SOUL.md"), []byte("# Soul"), 0644)

	configData := map[string]interface{}{
		"providers": map[string]interface{}{
			"anthropic": map[string]interface{}{
				"apiKey": "sk-config-only",
			},
		},
	}
	data, _ := json.Marshal(configData)
	os.WriteFile(filepath.Join(openclawHome, "openclaw.json"), data, 0644)

	opts := Options{
		Force:        true,
		ConfigOnly:   true,
		OpenClawHome: openclawHome,
	}

	result, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !result.ConfigMigrated {
		t.Error("config should have been migrated")
	}

	picoWs := filepath.Join(picoClawHome, "workspaces", "default")
	if _, err := os.Stat(filepath.Join(picoWs, "SOUL.md")); !os.IsNotExist(err) {
		t.Error("config-only should not copy workspace files")
	}
}

func TestRunWorkspaceOnly(t *testing.T) {
	openclawHome := t.TempDir()
	picoClawHome := t.TempDir()
	t.Setenv("SCICLAW_HOME", picoClawHome)

	wsDir := filepath.Join(openclawHome, "workspace")
	os.MkdirAll(wsDir, 0755)
	os.WriteFile(filepath.Join(wsDir, "SOUL.md"), []byte("# Soul"), 0644)

	configData := map[string]interface{}{
		"providers": map[string]interface{}{
			"anthropic": map[string]interface{}{
				"apiKey": "sk-ws-only",
			},
		},
	}
	data, _ := json.Marshal(configData)
	os.WriteFile(filepath.Join(openclawHome, "openclaw.json"), data, 0644)

	opts := Options{
		Force:         true,
		WorkspaceOnly: true,
		OpenClawHome:  openclawHome,
	}

	result, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.ConfigMigrated {
		t.Error("workspace-only should not migrate config")
	}

	soulData, err := os.ReadFile(filepath.Join(picoClawHome, "profile", "SOUL.md"))
	if err != nil {
		t.Fatalf("reading SOUL.md: %v", err)
	}
	if string(soulData) != "# Soul" {
		t.Errorf("SOUL.md content = %q", string(soulData))
	}
}
