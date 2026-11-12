---
name: gitea-create-change-request
description: Create an AtomicQMS change request as a Gitea pull request through the sciclaw-mcp-server MCP tool. Use when a user asks to open a pull request or change request from an existing branch.
---

# Create Gitea Change Request

Use the `exec` tool to run `mcp-server-cli`, which calls the remote MCP tool `create_change_request`. The CLI reads `SCICLAW_MCP_URL` and `SCICLAW_MCP_API_KEY` from its environment; never include either value in a command, chat message, or log.

If the executable is unavailable or reports missing configuration, explain that the operation cannot run and request that an administrator install the CLI and configure both environment variables in the SciClaw service. Do not attempt an unauthenticated request.

Collect `owner`, `repository`, `title`, `head`, and `base`. Collect optional `body` and `reviewers` only when requested. Require explicit confirmation immediately before the call.

Serialize the arguments as compact JSON, shell-quote them as one argument, and execute:

```sh
mcp-server-cli call create_change_request '<json-arguments>'
```

Report the created change-request number and URL returned by the tool. Do not claim it was created when the tool fails.
