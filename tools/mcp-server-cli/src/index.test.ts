import { expect, test } from "bun:test";
import { createServer } from "node:http";
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
