---
name: gitea-create-change-request
description: Create an AtomicQMS change request as a Gitea pull request through the official Gitea MCP server. Use when a user asks to open a pull request or change request from an existing branch.
---

# Create Gitea Change Request

Use the `gitea_mcp` tool for every call to the configured official Gitea MCP server. Set `tool` to the MCP tool name and `arguments` to a JSON object containing its arguments. Never use `exec` or construct shell commands. The CLI reads `SCICLAW_MCP_URL`; never include credentials in arguments, chat messages, or logs. This requires the standalone CLI with stdin support (sciclaw-mcp-cli PR #2) installed by the service packaging (sciclaw-stack #12); the older bundled CLI does not support this invocation.

If the executable is unavailable or reports missing configuration, explain that the operation cannot run and request that an administrator install the CLI and configure `SCICLAW_MCP_URL` in the SciClaw service.

Collect `owner`, `repository`, `title`, `head`, and `base`. Collect optional `body` and `reviewers` only when requested. Require explicit confirmation immediately before the call.

Create the pull request:

Call `gitea_mcp` with `tool: "pull_request_write"` and `arguments` containing:

```json
{"method":"create","owner":"<owner>","repo":"<repository>","title":"<title>","head":"<head>","base":"<base>","body":"<body>"}
```

If creation fails, report the failure and do not claim the change request was created. If creation succeeds, report the created change-request number and URL. If reviewers were requested, take the returned `number` and call `pull_request_write` again with `method: "add_reviewers"`, the same `owner` and `repo`, `pull_number`, and `reviewers`. Report any reviewer-assignment failure separately without concealing the created change request.
