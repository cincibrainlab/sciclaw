---
name: gitea-move-file-phase-0-to-1
description: Move one AtomicQMS/Gitea repository task file from phase_0 to the matching path under phase_1 through the sciclaw-mcp-server MCP tool. Use when a user asks to advance or promote a repository file from phase 0 to phase 1.
---

# Move Gitea File From Phase 0 To Phase 1

Use the `exec` tool to run `mcp-server-cli`, which calls the remote MCP tool `move_file_to_phase_1`. The CLI reads its MCP URL and API key file from the service environment; never include credentials in a command, chat message, or log.

Collect only `branch` and the exact repository-relative `filePath`. The MCP server supplies the fixed owner and repository. Require `filePath` to start with `phase_0/` and identify one file, not a directory. Reject backslashes, empty path segments, `.`, and `..`.

Show the source path, matching `phase_1/` destination, and branch. Require explicit confirmation immediately before the call.

Serialize the arguments as compact JSON, shell-quote them as one argument, and execute:

```sh
mcp-server-cli call move_file_to_phase_1 '<json-arguments>'
```

Report the returned source path, destination path, branch, and move status. Do not claim success if creation or source deletion fails.
