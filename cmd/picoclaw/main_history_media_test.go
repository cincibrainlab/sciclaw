package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestHistoryStagesProxyAttachmentsWithoutOverwriting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "token=1" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte("attachment"))
	}))
	defer server.Close()
	fetch := discordHistoryCallback(func(string, int, string) ([]*discordgo.Message, error) {
		return []*discordgo.Message{{ID: "1", Attachments: []*discordgo.MessageAttachment{{ProxyURL: "http://public.invalid/file.txt?token=1"}}}}, nil
	})
	workspace := t.TempDir()
	callback := historyStagingCallback(workspace, fetch, map[string]string{"http://public.invalid": server.URL})
	for i := 0; i < 2; i++ {
		messages, err := callback(context.Background(), "room", 10, "")
		if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Content, "Attachments staged locally") {
			t.Fatalf("messages = %+v, err = %v", messages, err)
		}
		paths, err := filepath.Glob(filepath.Join(workspace, ".sciclaw", "inbound", "discord-history", "1-*", "file.txt"))
		if err != nil || len(paths) != i+1 {
			t.Fatalf("paths = %v, err = %v", paths, err)
		}
		if i == 0 {
			if err := os.WriteFile(paths[0], []byte("edited"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	paths, _ := filepath.Glob(filepath.Join(workspace, ".sciclaw", "inbound", "discord-history", "1-*", "file.txt"))
	preserved := false
	for _, p := range paths {
		data, _ := os.ReadFile(p)
		if string(data) == "edited" {
			preserved = true
		}
	}
	if !preserved {
		t.Fatal("edited attachment overwritten")
	}
}

func TestHistoryStagingCancellation(t *testing.T) {
	fetch := discordHistoryCallback(func(string, int, string) ([]*discordgo.Message, error) {
		return []*discordgo.Message{{ID: "1", Attachments: []*discordgo.MessageAttachment{{URL: "http://example.invalid/file"}}}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := historyStagingCallback(t.TempDir(), fetch, nil)(ctx, "room", 10, "")
	if err != context.Canceled {
		t.Fatalf("error = %v", err)
	}
}
