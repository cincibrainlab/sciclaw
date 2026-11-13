package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const localPathRawLimit = 20 << 20
const localPathJSONLimit = 31 << 20

var localPathBranchRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

func localPathUploadResult(output []byte, arguments map[string]interface{}, callErr error) *ToolResult {
	var response struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if callErr != nil || json.Unmarshal(output, &response) != nil || response.IsError {
		return ErrorResult("local_path upload failed or its outcome is unknown. Reconcile remote state before retrying; the write may have completed.")
	}
	branch, _ := arguments["new_branch_name"].(string)
	if branch == "" {
		branch, _ = arguments["branch_name"].(string)
	}
	files, _ := arguments["files"].([]interface{})
	count := len(files)
	if path, _ := arguments["path"].(string); path != "" {
		count++
	}
	// CreateOrUpdateFileFn returns a JSON-encoded string via to.TextResult,
	// not a commit object. Match its entire confirmation against the request;
	// expose only a bounded branch token, never arbitrary response text or URLs.
	if count > 0 && localPathBranchRef.MatchString(branch) {
		expected := fmt.Sprintf("Committed %d file(s) to branch %s", count, branch)
		for _, content := range response.Content {
			var text string
			if content.Type == "text" && json.Unmarshal([]byte(content.Text), &text) == nil && text == expected {
				return NewToolResult(fmt.Sprintf("Server confirmed %d file(s) committed to branch %q. Commit SHA verification unavailable (server returned only a branch confirmation).", count, branch))
			}
		}
	}
	return NewToolResult("Gitea MCP local file upload completed; saved-reference verification unavailable.")
}

// Copies only the containers being changed. Encoded bytes never enter caller arguments.
func (t *GiteaMCPTool) prepareLocalPaths(ctx context.Context, arguments map[string]interface{}) (map[string]interface{}, bool, error) {
	total, upload := 0, false
	prepare := func(entry map[string]interface{}) (map[string]interface{}, error) {
		value, exists := entry["local_path"]
		if !exists {
			return entry, nil
		}
		upload = true
		path, ok := value.(string)
		if !ok || path == "" || strings.ContainsRune(path, 0) {
			return nil, errors.New("local_path must be a nonempty path without NUL")
		}
		_, content := entry["content"]
		_, edits := entry["edits"]
		encoding, encoded := entry["encoding"]
		if content || edits || entry["delete"] == true || (encoded && encoding != "base64") {
			return nil, errors.New("local_path conflicts with content, edits, delete:true or a non-base64 encoding")
		}
		if ctx.Err() != nil {
			return nil, errors.New("local_path upload canceled")
		}
		data, err := t.readLocalPath(path, localPathRawLimit-total)
		if err != nil {
			// Filesystem errors include private paths; expose only the constraint.
			return nil, errors.New("local_path must be a readable regular file within allowed roots (no final symlinks or root escapes), within the 20 MiB aggregate limit")
		}
		total += len(data)
		copy := maps.Clone(entry)
		delete(copy, "local_path")
		copy["content"], copy["encoding"] = base64.StdEncoding.EncodeToString(data), "base64"
		return copy, nil
	}
	prepared, err := prepare(arguments)
	if err != nil {
		return nil, upload, err
	}
	if files, ok := prepared["files"].([]interface{}); ok {
		entries := make([]interface{}, len(files))
		for i, value := range files {
			entries[i] = value
			if entry, ok := value.(map[string]interface{}); ok {
				entries[i], err = prepare(entry)
				if err != nil {
					return nil, upload, err
				}
			}
		}
		if upload {
			prepared = maps.Clone(prepared)
			prepared["files"] = entries
		}
	}
	return prepared, upload, nil
}

func (t *GiteaMCPTool) readLocalPath(path string, remaining int) ([]byte, error) {
	if t.restrict && t.workspace == "" {
		return nil, os.ErrPermission
	}
	abs, err := validatePathWithPolicy(path, t.workspace, t.restrict, AccessRead, t.sharedWorkspace, t.sharedWorkspaceReadOnly)
	if err != nil {
		return nil, err
	}
	abs, err = filepath.Abs(abs)
	if err != nil {
		return nil, err
	}
	rootPath := filepath.VolumeName(abs) + string(filepath.Separator)
	if t.restrict {
		rootPath = ""
		for _, candidate := range []string{t.workspace, strings.TrimSpace(t.sharedWorkspace)} {
			if candidate == "" {
				continue
			}
			candidate, err = filepath.Abs(candidate)
			if err != nil {
				return nil, err
			}
			if isWithinWorkspace(abs, candidate) {
				rootPath = candidate
				break
			}
		}
		if rootPath == "" {
			return nil, os.ErrPermission
		}
	}
	// Configured root locations are trusted. Pin the directory during traversal/read;
	// unlike a realpath check followed by open, Root cannot follow an escaping link.
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := filepath.Rel(rootPath, abs)
	if err != nil {
		return nil, err
	}
	before, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > int64(remaining) {
		return nil, os.ErrInvalid
	}
	f, err := openLocalPath(root, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() > int64(remaining) {
		return nil, os.ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(remaining)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > remaining {
		return nil, os.ErrInvalid
	}
	return data, nil
}
