---
name: gitea-move-work-item
description: Move an AtomicQMS Gitea work item between named organization-project phases through the official Gitea MCP server. Use for requests to move, advance, return, or change the phase of a Gitea issue.
---

# Move Gitea Work Item

Use the `gitea_mcp` tool for every call to the configured official Gitea MCP server. Set `tool` to the MCP tool name and `arguments` to a JSON object containing its arguments. Never use `exec` or construct shell commands. The CLI reads `SCICLAW_MCP_URL`; never include credentials in arguments, chat messages, or logs. This requires the standalone CLI with stdin support (sciclaw-mcp-cli PR #2) installed by the service packaging (sciclaw-stack #12); the older bundled CLI does not support this invocation.

If the executable is unavailable or reports missing configuration, explain that the operation cannot run and request that an administrator install the CLI and configure `SCICLAW_MCP_URL` in the SciClaw service.

Collect `organization`, `projectId`, `owner`, `repository`, `issueNumber`, `fromPhase`, and `toPhase`. Require explicit confirmation of the issue and source-to-destination phase immediately before the call. Ensure the phase names differ.

Call `issue_read` with `method: "get"`, `owner`, `repo`, and `issue_number`; retain its internal `id`. Call `project_read` with `method: "list_columns"`, `org: organization`, and `project_id: projectId`; search subsequent pages until both exact source and destination column IDs are found by title or results are exhausted. On any page error, report it and stop without calling `project_write`. If either column is absent after exhausting results, report it and stop without calling `project_write`. Otherwise, call `project_read` again with `method: "list_column_issues"` and the source `column_id`; search subsequent pages until the internal issue ID is found or results are exhausted. On any page error or if the issue is absent after exhausting results, report it and stop without calling `project_write`. Only when both columns and the issue are found, move it with:

Call `gitea_mcp` with `tool: "project_write"` and `arguments` containing:

```json
{"method":"add_issue","org":"<organization>","project_id":<project-id>,"column_id":<destination-column-id>,"issue_id":<issue-id>}
```

Report the issue, source phase, and destination phase. Do not claim a move succeeded when any call fails.
