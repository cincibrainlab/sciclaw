import { expect, test } from "bun:test";
import { createServer } from "node:http";
import { copyFile, mkdtemp, rm, symlink } from "node:fs/promises";
import { existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { z } from "zod";
import { callTool, parseCallArguments } from "./index.js";

test("parses a tool call", () => {
  expect(parseCallArguments(["call", "example", "{\"value\":1}"])).toEqual({
    tool: "example",
    arguments: { value: 1 },
  });
});

test("rejects invalid arguments", () => {
  expect(() => parseCallArguments(["call", "example", "[]"])).toThrow("JSON object");
  expect(() => parseCallArguments(["call", "example", "nope"])).toThrow("valid JSON");
});

test("calls a Streamable HTTP MCP tool", async () => {
  const httpServer = createServer(async (request, response) => {
    expect(request.headers.authorization).toBeUndefined();

    const mcpServer = new McpServer({ name: "test-server", version: "1.0.0" });
    mcpServer.registerTool(
      "echo",
      { description: "Echo a value", inputSchema: { value: z.string() } },
      async ({ value }) => ({ content: [{ type: "text", text: value }] }),
    );
    const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined });
    await mcpServer.connect(transport);
    await transport.handleRequest(request, response);
  });

  await new Promise<void>((resolve) => httpServer.listen(0, "127.0.0.1", resolve));
  try {
    const address = httpServer.address();
    if (!address || typeof address === "string") throw new Error("test server has no TCP address");
    const result = await callTool("echo", { value: "connected" }, {
      SCICLAW_MCP_URL: `http://127.0.0.1:${address.port}/mcp`,
      SCICLAW_MCP_API_KEY: "legacy-key",
    });
    expect(result.content).toEqual([{ type: "text", text: "connected" }]);
  } finally {
    await new Promise<void>((resolve, reject) =>
      httpServer.close((error) => (error ? reject(error) : resolve())),
    );
  }
});

test("CLI transmits hostile values and nested integer IDs and reports MCP errors", async () => {
  const dir = await mkdtemp(join(tmpdir(), "mcp-cli-test-"));
  const binary = join(dir, "mcp-server-cli");
  await copyFile(join(import.meta.dir, "index.ts"), join(dir, "index.ts"));
  await symlink(join(import.meta.dir, "..", "node_modules"), join(dir, "node_modules"));
  const build = Bun.spawn([process.execPath, "build", "--compile", "--outfile", binary, "index.ts"], { cwd: dir, stdout: "pipe", stderr: "pipe" });
  const buildError = await new Response(build.stderr).text();
  if (await build.exited !== 0) throw new Error(`CLI build failed: ${buildError}`);
  const received: unknown[] = [];
  const httpServer = createServer(async (request, response) => {
    const server = new McpServer({ name: "test-server", version: "1.0.0" });
    server.registerTool("echo", { inputSchema: { value: z.string(), nested: z.object({ id: z.number() }), fail: z.boolean() } }, async (args) => {
      received.push(args);
      return { content: [{ type: "text", text: args.fail ? "rejected" : "ok" }], isError: args.fail };
    });
    const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined });
    await server.connect(transport);
    await transport.handleRequest(request, response);
  });
  await new Promise<void>((resolve) => httpServer.listen(0, "127.0.0.1", resolve));
  try {
    const address = httpServer.address();
    if (!address || typeof address === "string") throw new Error("no address");
    const marker = join(dir, "injected");
    const args = { value: `O'Brien\n$(touch ${marker}); \`touch ${marker}\` & |`, nested: { id: 9007199254740991 }, fail: false };
    for (const fail of [false, true]) {
      const proc = Bun.spawn([binary, "call", "echo", JSON.stringify({ ...args, fail })], {
        env: { ...process.env, SCICLAW_MCP_URL: `http://127.0.0.1:${address.port}/mcp` }, stdout: "pipe", stderr: "pipe",
      });
      const output = await new Response(proc.stdout).text();
      expect(await proc.exited).toBe(fail ? 1 : 0);
      expect(JSON.parse(output).content[0].text).toBe(fail ? "rejected" : "ok");
    }
    expect(received).toEqual([args, { ...args, fail: true }]);
    expect(existsSync(marker)).toBe(false);
  } finally {
    await new Promise<void>((resolve, reject) => httpServer.close((error) => error ? reject(error) : resolve()));
    await rm(dir, { recursive: true, force: true });
  }
});
