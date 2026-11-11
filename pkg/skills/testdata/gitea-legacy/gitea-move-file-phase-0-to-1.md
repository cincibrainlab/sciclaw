---
name: gitea-move-file-phase-0-to-1
description: Move one AtomicQMS/Gitea repository task file from phase_0 to the matching path under phase_1 through the official Gitea MCP server. Use when a user asks to advance or promote a repository file from phase 0 to phase 1.
---

# Move Gitea File From Phase 0 To Phase 1

Use the `exec` tool to run `mcp-server-cli`, which calls the configured official Gitea MCP server. The CLI reads `SCICLAW_MCP_URL`; never include credentials in a command, chat message, or log.

Collect `owner`, `repository`, `branch`, and the exact repository-relative `filePath`. Require `filePath` to start with `phase_0/` and identify one file, not a directory. Reject backslashes, empty path segments, `.`, and `..`.

Show the source path, matching `phase_1/` destination, and branch. Require explicit confirmation immediately before the call.

First call `get_file_contents` with `owner`, `repo`, `ref: branch`, and `path: filePath`; retain the returned `sha`. Then rename the source atomically with `create_or_update_file`:

```sh
mcp-server-cli call create_or_update_file '{"owner":"<owner>","repo":"<repository>","branch_name":"<branch>","message":"Move <source> to <destination>","files":[{"path":"<destination>","from_path":"<source>","sha":"<source-sha>"}]}'
```

Report the source path, destination path, and branch. Do not claim success if either call fails.
