# Gateway active routing status

Set `SCICLAW_ROUTING_STATUS_DIR` to an **absolute**, nonexistent directory in a trusted parent before starting the gateway. The gateway creates it with mode `0700` and serves HTTP over `status.sock` inside it. For example:

```sh
curl --unix-socket "$SCICLAW_ROUTING_STATUS_DIR/status.sock" http://localhost/v1/routing
```

`GET /v1/routing` returns JSON with `enabled`, `unmapped_behavior`, and `mappings` (the routing configuration schema). It snapshots the resolver currently installed in the dispatcher, not the config file. Mappings are sorted by routing key; the default unmapped behavior is explicit. If the gateway started without a dispatcher, it returns `{"enabled":false,"unmapped_behavior":"","mappings":[]}` even if routing is subsequently enabled in config: that transition requires a gateway restart. A disabled resolver may retain mappings; they are not applied while disabled. New decisions after a successful resolver swap use the new snapshot; previously queued or in-flight work may still use the old one. Other HTTP methods return 405.

The admin `GET /api/routing/mappings` response includes `requireMention` as `true`, `false`, or `null` (unset, which defaults to required). The live endpoint uses `require_mention`, omitted when unset. The admin API also uses camelCase field names and displays an unset mapping mode as `default`; consumers must normalize the two schemas and routing defaults before comparison. The admin status and mappings are separate config reads, and a successful comparison does not prevent routing from changing before a share is issued.

Keep the path short enough for the OS Unix-socket path limit. The directory is exclusively created; an existing path causes status startup to fail without modifying it. When configured, failure to start the status endpoint aborts gateway startup. Clients must treat an absent or unreachable socket as unknown, never as disabled or verified. On shutdown cleanup is attempted, but process exit can leave the socket or directory behind; the owner must remove a stale directory before restarting. Only the gateway OS user (and privileged users) should be able to access the directory. The response contains routing identifiers and allowlists and must not be exposed over a public network.
