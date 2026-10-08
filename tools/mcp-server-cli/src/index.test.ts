import { expect, spyOn, test } from "bun:test";
import { createServer } from "node:http";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { z } from "zod";
import { callTool, parseBatchRequest, parseCallArguments, resolveReferences, runBatch } from "./index.js";

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

test("validates batch requests", () => {
  expect(() => parseBatchRequest("nope")).toThrow("valid JSON");
  expect(() => parseBatchRequest('{"steps":[],"return":"missing"}')).toThrow("unknown step");
  expect(() => parseBatchRequest('{"steps":[{"id":"a","tool":"echo","arguments":[] }],"return":"a"}'))
    .toThrow("arguments must be a JSON object");
  expect(() => parseBatchRequest('{"steps":[{"id":"a","tool":"echo","arguments":{}},{"id":"a","tool":"echo","arguments":{}}],"return":"a"}'))
    .toThrow("duplicate batch step id");
});

test("resolves references to completed results", () => {
  const results = new Map<string, unknown>([["first", { value: { "a/b": { "~key": "connected" } } }]]);
  expect(resolveReferences({ nested: [{ $ref: "first", pointer: "/value/a~1b/~0key" }] }, results)).toEqual({
    nested: ["connected"],
  });
  expect(() => resolveReferences({ $ref: "later" }, results)).toThrow("not complete");
  expect(() => resolveReferences({ $ref: "first", pointer: "/missing" }, results)).toThrow("not found");
  expect(() => resolveReferences({ $ref: "first", pointer: "value" }, results)).toThrow("invalid JSON pointer");
  expect(() => resolveReferences({ $ref: "first", pointer: "/~2" }, results)).toThrow("invalid JSON pointer");
});

test("calls a Gitea MCP HTTP tool", async () => {
  const httpServer = createServer(async (request, response) => {
    if (request.headers.authorization !== "Bearer test-key") {
      response.writeHead(401).end();
      return;
    }

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
      GITEA_MCP_URL: `http://127.0.0.1:${address.port}/mcp`,
      GITEA_ACCESS_TOKEN: "test-key",
    });
    expect(result.content).toEqual([{ type: "text", text: "connected" }]);
  } finally {
    await new Promise<void>((resolve, reject) =>
      httpServer.close((error) => (error ? reject(error) : resolve())),
    );
  }
});

test("runs a sequential batch and stops on MCP errors", async () => {
  let afterCalls = 0;
  const httpServer = createServer(async (request, response) => {
    const mcpServer = new McpServer({ name: "test-server", version: "1.0.0" });
    mcpServer.registerTool("json", {}, async () => ({
      content: [{ type: "text", text: JSON.stringify({ result: "connected" }) }],
    }));
    mcpServer.registerTool(
      "echo",
      { inputSchema: { previousValue: z.string() } },
      async ({ previousValue }) => ({ content: [{ type: "text", text: JSON.stringify({ received: previousValue }) }] }),
    );
    mcpServer.registerTool("fail", {}, async () => ({ content: [{ type: "text", text: "failed" }], isError: true }));
    mcpServer.registerTool("after", {}, async () => {
      afterCalls++;
      return { content: [{ type: "text", text: "unexpected" }] };
    });
    const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined });
    await mcpServer.connect(transport);
    await transport.handleRequest(request, response);
  });

  await new Promise<void>((resolve) => httpServer.listen(0, "127.0.0.1", resolve));
  const connect = spyOn(StreamableHTTPClientTransport.prototype, "start");
  const close = spyOn(StreamableHTTPClientTransport.prototype, "close");
  try {
    const address = httpServer.address();
    if (!address || typeof address === "string") throw new Error("test server has no TCP address");
    const env = { GITEA_MCP_URL: `http://127.0.0.1:${address.port}/mcp`, GITEA_ACCESS_TOKEN: "test-key" };
    const result = await runBatch(parseBatchRequest(JSON.stringify({
      steps: [
        { id: "first", tool: "json", arguments: {} },
        { id: "second", tool: "echo", arguments: { previousValue: { $ref: "first", pointer: "/value/result" } } },
      ],
      return: "second",
    })), env);
    expect(result.value).toEqual({ received: "connected" });

    await expect(runBatch(parseBatchRequest(JSON.stringify({
      steps: [
        { id: "failure", tool: "fail", arguments: {} },
        { id: "after", tool: "after", arguments: {} },
      ],
      return: "after",
    })), env)).rejects.toThrow("batch step failure failed");
    expect(afterCalls).toBe(0);
    expect(connect).toHaveBeenCalledTimes(2);
    expect(close).toHaveBeenCalledTimes(2);
  } finally {
    connect.mockRestore();
    close.mockRestore();
    await new Promise<void>((resolve, reject) =>
      httpServer.close((error) => (error ? reject(error) : resolve())),
    );
  }
});
