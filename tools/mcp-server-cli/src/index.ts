#!/usr/bin/env bun

import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

export function parseCallArguments(argv: string[]): { tool: string; arguments: Record<string, unknown> } {
  if (argv.length !== 3 || argv[0] !== "call") {
    throw new Error("usage: mcp-server-cli call <tool-name> '<json-arguments>'");
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(argv[2]);
  } catch {
    throw new Error("json-arguments must be valid JSON");
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("json-arguments must be a JSON object");
  }

  return { tool: argv[1], arguments: parsed as Record<string, unknown> };
}

export async function callTool(
  tool: string,
  arguments_: Record<string, unknown>,
  env: Record<string, string | undefined> = process.env,
) {
  const serverUrl = env.SCICLAW_MCP_URL;
  if (!serverUrl) throw new Error("SCICLAW_MCP_URL is not set");

  const client = new Client({ name: "mcp-server-cli", version: "0.1.0" });
  const transport = new StreamableHTTPClientTransport(new URL(serverUrl));

  try {
    await client.connect(transport);
    return await client.callTool({ name: tool, arguments: arguments_ });
  } finally {
    await transport.close().catch(() => undefined);
  }
}

async function main() {
  const request = parseCallArguments(process.argv.slice(2));
  const result = await callTool(request.tool, request.arguments);
  console.log(JSON.stringify(result, null, 2));
  if ("isError" in result && result.isError) process.exitCode = 1;
}

if (import.meta.main) {
  main().catch((error: unknown) => {
    console.error(`mcp-server-cli: ${error instanceof Error ? error.message : String(error)}`);
    process.exitCode = 1;
  });
}
