#!/usr/bin/env bun

import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import { readFile } from "node:fs/promises";

type JsonObject = Record<string, unknown>;
type BatchStep = { id: string; tool: string; arguments: JsonObject };
type BatchRequest = { steps: BatchStep[]; return: string };

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

export function parseBatchRequest(input: string): BatchRequest {
  let parsed: unknown;
  try {
    parsed = JSON.parse(input);
  } catch {
    throw new Error("batch input must be valid JSON");
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("batch input must be a JSON object");
  }

  const request = parsed as JsonObject;
  if (!Array.isArray(request.steps)) throw new Error("batch steps must be an array");
  if (typeof request.return !== "string" || !request.return) throw new Error("batch return must be a step id");

  const ids = new Set<string>();
  const steps = request.steps.map((value, index): BatchStep => {
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`batch step ${index} must be an object`);
    const step = value as JsonObject;
    if (typeof step.id !== "string" || !step.id) throw new Error(`batch step ${index} must have an id`);
    if (ids.has(step.id)) throw new Error(`duplicate batch step id: ${step.id}`);
    if (typeof step.tool !== "string" || !step.tool) throw new Error(`batch step ${step.id} must have a tool`);
    if (!step.arguments || typeof step.arguments !== "object" || Array.isArray(step.arguments)) {
      throw new Error(`batch step ${step.id} arguments must be a JSON object`);
    }
    ids.add(step.id);
    return { id: step.id, tool: step.tool, arguments: step.arguments as JsonObject };
  });
  if (!ids.has(request.return)) throw new Error(`batch return references unknown step: ${request.return}`);
  return { steps, return: request.return };
}

function resolvePointer(value: unknown, pointer: string): unknown {
  if (pointer === "") return value;
  if (!pointer.startsWith("/")) throw new Error(`invalid JSON pointer: ${pointer}`);

  return pointer.slice(1).split("/").reduce((current: unknown, token) => {
    if (/~(?![01])/u.test(token)) throw new Error(`invalid JSON pointer: ${pointer}`);
    const key = token.replaceAll("~1", "/").replaceAll("~0", "~");
    if (!current || typeof current !== "object" || !Object.hasOwn(current, key)) {
      throw new Error(`JSON pointer not found: ${pointer}`);
    }
    return (current as JsonObject)[key];
  }, value);
}

export function resolveReferences(value: unknown, results: Map<string, unknown>): unknown {
  if (Array.isArray(value)) return value.map((item) => resolveReferences(item, results));
  if (!value || typeof value !== "object") return value;

  const object = value as JsonObject;
  if ("$ref" in object) {
    if (Object.keys(object).some((key) => key !== "$ref" && key !== "pointer") ||
      typeof object.$ref !== "string" || ("pointer" in object && typeof object.pointer !== "string")) {
      throw new Error("invalid batch reference");
    }
    if (!results.has(object.$ref)) throw new Error(`batch reference is not complete: ${object.$ref}`);
    return resolvePointer(results.get(object.$ref), (object.pointer as string | undefined) ?? "");
  }
  return Object.fromEntries(Object.entries(object).map(([key, item]) => [key, resolveReferences(item, results)]));
}

function normalizeResult(result: JsonObject): JsonObject {
  const content = result.content;
  if (!Array.isArray(content) || content.length !== 1) return result;
  const item = content[0];
  if (!item || typeof item !== "object" || (item as JsonObject).type !== "text" || typeof (item as JsonObject).text !== "string") {
    return result;
  }
  try {
    return { ...result, value: JSON.parse((item as JsonObject).text as string) };
  } catch {
    return result;
  }
}

async function withClient<T>(
  run: (client: Client) => Promise<T>,
  env: Record<string, string | undefined> = process.env,
): Promise<T> {
  const serverUrl = env.GITEA_MCP_URL;
  const accessToken = env.GITEA_ACCESS_TOKEN ??
    (env.GITEA_ACCESS_TOKEN_FILE ? (await readFile(env.GITEA_ACCESS_TOKEN_FILE, "utf8")).trim() : undefined);
  if (!serverUrl) throw new Error("GITEA_MCP_URL is not set");
  if (!accessToken) throw new Error("GITEA_ACCESS_TOKEN is not set");

  const client = new Client({ name: "mcp-server-cli", version: "0.1.0" });
  const transport = new StreamableHTTPClientTransport(new URL(serverUrl), {
    requestInit: { headers: { Authorization: `Bearer ${accessToken}` } },
  });

  try {
    await client.connect(transport);
    return await run(client);
  } finally {
    await transport.close().catch(() => undefined);
  }
}

export async function callTool(
  tool: string,
  arguments_: JsonObject,
  env: Record<string, string | undefined> = process.env,
) {
  return await withClient((client) => client.callTool({ name: tool, arguments: arguments_ }), env);
}

export async function runBatch(request: BatchRequest, env: Record<string, string | undefined> = process.env) {
  return await withClient(async (client) => {
    const results = new Map<string, JsonObject>();
    for (const step of request.steps) {
      const arguments_ = resolveReferences(step.arguments, results) as JsonObject;
      const result = await client.callTool({ name: step.tool, arguments: arguments_ }) as JsonObject;
      if (result.isError) throw new Error(`batch step ${step.id} failed: ${JSON.stringify(result)}`);
      results.set(step.id, normalizeResult(result));
    }
    return results.get(request.return)!;
  }, env);
}

async function main() {
  const argv = process.argv.slice(2);
  let result: JsonObject;
  if (argv[0] === "batch") {
    if (argv.length !== 1) throw new Error("usage: mcp-server-cli batch < batch.json");
    result = await runBatch(parseBatchRequest(await Bun.stdin.text()));
  } else {
    const request = parseCallArguments(argv);
    result = await callTool(request.tool, request.arguments) as JsonObject;
  }
  console.log(JSON.stringify(result, null, 2));
  if ("isError" in result && result.isError) process.exitCode = 1;
}

if (import.meta.main) {
  main().catch((error: unknown) => {
    console.error(`mcp-server-cli: ${error instanceof Error ? error.message : String(error)}`);
    process.exitCode = 1;
  });
}
