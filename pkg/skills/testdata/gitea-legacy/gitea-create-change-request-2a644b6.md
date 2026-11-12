---
name: gitea-create-change-request
description: Create an AtomicQMS change request as a Gitea pull request through the official Gitea MCP server. Use when a user asks to open a pull request or change request from an existing branch.
---

# Create Gitea Change Request

Use the `exec` tool to run `mcp-server-cli`, which calls the configured official Gitea MCP server. The CLI reads `SCICLAW_MCP_URL`; never include credentials in a command, chat message, or log.

If the executable is unavailable or reports missing configuration, explain that the operation cannot run and request that an administrator install the CLI and configure `SCICLAW_MCP_URL` in the SciClaw service.

Collect `owner`, `repository`, `title`, `head`, and `base`. Collect optional `body` and `reviewers` only when requested. Require explicit confirmation immediately before the call.

Create the pull request:

```sh
mcp-server-cli call pull_request_write '{"method":"create","owner":"<owner>","repo":"<repository>","title":"<title>","head":"<head>","base":"<base>","body":"<body>"}'
```

If reviewers were requested, take the returned `number` and call `pull_request_write` again with `method: "add_reviewers"`, the same `owner` and `repo`, `pull_number`, and `reviewers`. Report the created change-request number and URL. Do not claim it was created when either requested call fails.
