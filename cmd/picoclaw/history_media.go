package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// Cache entries may be edited by agents, so exceeding the limit rejects new files.
const historyCacheMaxBytes int64 = 1024 * 1024 * 1024

var errHistoryCacheQuota = errors.New("history attachment cache quota exhausted")

// History uses a stable, separate cache so retries do not replace agent-edited files.
func stageHistoryAttachment(ctx context.Context, workspace, channel, message string, index int, mediaID, source string, rewrites map[string]string) (string, error) {
	return stageHistoryAttachmentWithQuota(ctx, workspace, channel, message, index, mediaID, source, rewrites, historyCacheMaxBytes)
}

func stageHistoryAttachmentWithQuota(ctx context.Context, workspace, channel, message string, index int, mediaID, source string, rewrites map[string]string, quota int64) (string, error) {
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
	// Hold the workspace lock through download so partial files count against the quota.
	lock, err := lockHistoryCache(ctx, root)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if info, err := os.Lstat(dest); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("history cache path is not a regular file")
		}
		return filepath.ToSlash(rel), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	var used int64
	if err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			used += info.Size()
		}
		return nil
	}); err != nil {
		return "", err
	}
	remaining := quota - used
	if remaining <= 0 {
		return "", errHistoryCacheQuota
	}
	temp, err := os.MkdirTemp(root, ".stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temp)
	inbound := bus.InboundMessage{Channel: "discord-history", Media: []string{source}, Metadata: map[string]string{"message_id": message}}
	if err := routing.PrepareInboundMediaWithLimit(ctx, temp, &inbound, rewrites, remaining); err != nil {
		var sizeErr *routing.InboundMediaSizeError
		if remaining < routing.InboundMediaMaxDownloadBytes && errors.As(err, &sizeErr) && (sizeErr.Size < 0 || sizeErr.Size <= routing.InboundMediaMaxDownloadBytes) {
			return "", errHistoryCacheQuota
		}
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
