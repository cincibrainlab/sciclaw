package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// History uses a stable, separate cache so retries do not replace agent-edited files.
func stageHistoryAttachment(ctx context.Context, workspace, channel, message string, index int, mediaID, source string, rewrites map[string]string) (string, error) {
	identity := source
	if mediaID != "" {
		identity = mediaID
		index = 0
	}
	filename := filepath.Base(source)
	if u, err := url.Parse(source); err == nil && u.Host != "" {
		filename = path.Base(u.Path)
	}
	filename = strings.TrimSpace(utils.SanitizeFilename(filename))
	if filename == "" || filename == "." {
		filename = "attachment.bin"
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%q\x00%q\x00%d\x00%q", channel, message, index, identity)))
	root := filepath.Join(workspace, ".sciclaw", "inbound", "discord-history")
	dest := filepath.Join(root, hex.EncodeToString(digest[:]), filename)
	rel, err := filepath.Rel(workspace, dest)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(dest); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("history cache path is not a regular file")
		}
		return filepath.ToSlash(rel), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return "", err
	}
	temp, err := os.MkdirTemp(root, ".stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temp)
	inbound := bus.InboundMessage{Channel: "discord-history", Media: []string{source}, Metadata: map[string]string{"message_id": message}}
	if err := routing.PrepareInboundMedia(ctx, temp, &inbound, rewrites); err != nil {
		return "", err
	}
	if len(inbound.Media) != 1 {
		return "", fmt.Errorf("attachment was not staged")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", err
	}
	if err := os.Link(inbound.Media[0], dest); err != nil {
		if !os.IsExist(err) {
			return "", err
		}
		info, statErr := os.Lstat(dest)
		if statErr != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("history cache path is not a regular file")
		}
	}
	return filepath.ToSlash(rel), nil
}
