---
name: gitea-move-work-item
description: Move an AtomicQMS Gitea work item between named organization-project phases through the sciclaw-mcp-server MCP tool. Use for requests to move, advance, return, or change the phase of a Gitea issue.
---

# Move Gitea Work Item

Use the `exec` tool to run `mcp-server-cli`, which calls the remote MCP tool `move_work_item_phase`. The CLI reads `SCICLAW_MCP_URL` and `SCICLAW_MCP_API_KEY` from its environment; never include either value in a command, chat message, or log.

If the executable is unavailable or reports missing configuration, explain that the operation cannot run and request that an administrator install the CLI and configure both environment variables in the SciClaw service. Do not attempt an unauthenticated request.

Collect `organization`, `projectId`, `owner`, `repository`, `issueNumber`, `fromPhase`, and `toPhase`. Require explicit confirmation of the issue and source-to-destination phase immediately before the call. Ensure the phase names differ.

Serialize the arguments as compact JSON, shell-quote them as one argument, and execute:

```sh
mcp-server-cli call move_work_item_phase '<json-arguments>'
```

Report the tool result. Do not claim a move succeeded when the tool fails.
