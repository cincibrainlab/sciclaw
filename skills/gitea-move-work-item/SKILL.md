---
name: gitea-move-work-item
description: Move an AtomicQMS Gitea work item between named organization-project phases through the official Gitea MCP server. Use for requests to move, advance, return, or change the phase of a Gitea issue.
---

# Move Gitea Work Item

Use the `exec` tool to run `mcp-server-cli`, which calls the configured official Gitea MCP server. The CLI reads `SCICLAW_MCP_URL`; never include credentials in a command, chat message, or log.

If the executable is unavailable or reports missing configuration, explain that the operation cannot run and request that an administrator install the CLI and configure `SCICLAW_MCP_URL` in the SciClaw service.

Collect `organization`, `projectId`, `owner`, `repository`, `issueNumber`, `fromPhase`, and `toPhase`. Require explicit confirmation of the issue and source-to-destination phase immediately before the call. Ensure the phase names differ.

Call `issue_read` with `method: "get"`, `owner`, `repo`, and `issue_number`; retain its internal `id`. Call `project_read` with `method: "list_columns"`, `org: organization`, and `project_id: projectId`; identify the exact source and destination column IDs by title. Call `project_read` again with `method: "list_column_issues"` and the source `column_id`; fail if the internal issue ID is absent. Finally move it with:

```sh
mcp-server-cli call project_write '{"method":"add_issue","org":"<organization>","project_id":<project-id>,"column_id":<destination-column-id>,"issue_id":<issue-id>}'
```

Report the issue, source phase, and destination phase. Do not claim a move succeeded when any call fails.
