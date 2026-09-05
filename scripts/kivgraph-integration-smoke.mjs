// Opt-in live checks against the installed service. No paid agent is launched.
// A query may trigger a registered-only rebuild when that standing permission
// is explicitly enabled in the local Atenea configuration.
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join } from "node:path";

const binary = process.env.ATENEA_BIN || "atenea";
const clients = ["codex", "claude-code", "opencode", "omp", "claude-desktop"];
async function connect(client) {
  const child = spawn(binary, ["mcp"], { stdio: ["pipe", "pipe", "pipe"] });
  let next = 0;
  const pending = new Map();
  const lines = createInterface({ input: child.stdout });
  lines.on("line", (line) => {
    let message;
    try { message = JSON.parse(line); } catch { return; }
    const waiter = pending.get(message.id);
    if (!waiter) return;
    pending.delete(message.id);
    if (message.error) waiter.reject(new Error(JSON.stringify(message.error)));
    else waiter.resolve(message.result);
  });
  child.stderr.resume();
  child.on("exit", (code) => {
    for (const waiter of pending.values()) waiter.reject(new Error("MCP bridge exited: " + code));
    pending.clear();
  });
  function request(method, params = {}) {
    const id = ++next;
    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => { pending.delete(id); reject(new Error("MCP query deadline exceeded")); }, method === "tools/call" && params.name?.includes("ensure_fresh") ? 31*60*1000 : 100*1000);
      pending.set(id, { resolve: value => { clearTimeout(timeout); resolve(value); }, reject: error => { clearTimeout(timeout); reject(error); } });
      child.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n");
    });
  }
  try {
    const init = await request("initialize", { protocolVersion: "2024-11-05", capabilities: {}, clientInfo: { name: client, version: "integration-smoke" } });
    assert.match(init.instructions, /atenea_graph_evidence/);
    child.stdin.write(JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }) + "\n");
    const tools = [];
    let cursor;
    do {
      const page = await request("tools/list", cursor ? { cursor } : {});
      tools.push(...page.tools);
      cursor = page.nextCursor;
    } while (cursor);
    return { request, tools, close: () => { child.stdin.end(); child.kill("SIGTERM"); } };
  } catch (error) { child.kill("SIGTERM"); throw error; }
}

function toolName(tools, id) {
  const tool = tools.find((tool) => tool.name === id || tool.name === id.replaceAll(".", "_"));
  assert.ok(tool, "missing offered tool " + id);
  return tool.name;
}

for (const client of clients) {
  const session = await connect(client);
  try {
    for (const id of ["symbol.implementations", "symbol.search", "symbol.source", "symbol.impact", "graph.repositories", "code.context"]) toolName(session.tools, id);
    assert.ok(!session.tools.some((tool) => /serena|symbol[._]unresolved$/.test(tool.name)));
    console.log(client + ": live initialize/tools/list passed");
    if (client !== "codex" || !process.argv.includes("--queries")) continue;
    async function call(id, args = {}, repository = "taxiprime-backend") {
      console.log("Atenea · " + id + " — " + repository);
      const result = await session.request("tools/call", { name: toolName(session.tools, id), arguments: { repository, _atenea_prefer: "kivgraph", ...args }, _meta: {"atenea.origin":"synthetic"} });
      assert.ok(!result.isError, JSON.stringify(result));
      const receipts = (result.content || []).filter((block) => block.type === "text").flatMap((block) => {
        try { const parsed = JSON.parse(block.text); return parsed.atenea_usage ? [parsed] : []; } catch { return []; }
      });
      assert.equal(receipts.length, 1, "one invocation receipt");
      assert.equal(receipts[0].atenea_usage.provider, "kivgraph");
      assert.equal(receipts[0].atenea_usage.invoked, true);
      console.log(JSON.stringify({ tool: id, result: result.structuredContent, evidence: receipts[0].atenea_graph_evidence }));
      return result.structuredContent;
    }
    await call("graph.status");
    if (process.argv.includes("--repair")) await call("graph.ensure_fresh");
    const repositories = await call("graph.repositories", { limit: 50 });
    const result = await call("symbol.search", { query: "UserType", mode: "exact", limit: 10 });
    assert.ok(result.matches.length, "UserType declaration");
    const first = result.matches[0];
    assert.ok(first.stable_key, "identity is available to symbol.get");
    await call("symbol.get", { stable_key: first.stable_key });
    await call("symbol.source", { symbols: [{ path: first.path, qualified_name: first.name }] });
    await call("symbol.overview", { file: first.path, limit: 20 });
    await call("symbol.intent_search", { intent: "tipos de usuarios", keywords: ["UserType", "user", "role"], limit: 5 });
    await call("code.context", { task: "tipos de usuarios", keywords: ["UserType", "user"], limit: 2, include_snippet: true, snippet_lines: 30 });
    await call("symbol.impact", { file: first.path, line: first.line, column: 1, depth: 1, limit: 10 });
    for (const id of ["symbol.references", "symbol.dependencies", "symbol.consumers", "symbol.implementations"]) {
      await call(id, { file: first.path, line: first.line, column: 1 });
    }
    if (process.argv.includes("--repair")) await call("graph.ensure_fresh", {}, "taxiprime-app");
    const codes = await call("symbol.search", { query: "UserTypeCode", mode: "exact", limit: 10 }, "taxiprime-app");
    const declaration = codes.matches.find((row) => row.kind === "class");
    assert.ok(declaration, "UserTypeCode class located structurally");
    const source = await call("symbol.source", { symbols: [{ path: declaration.path, qualified_name: declaration.name }] }, "taxiprime-app");
    const app = repositories.repositories.find((row) => row.name === "taxiprime-app");
    const local = await readFile(join(app.path, declaration.path), "utf8");
    for (const value of ["client", "company", "driver", "admin"]) {
      assert.ok(source.source.includes("'" + value + "'"), "source role: " + value);
      assert.ok(local.includes("'" + value + "'"), "independent local role: " + value);
    }
    console.log("TaxiPrime: graph located UserTypeCode; current source and independent local read agree on client/company/driver/admin. No database queried.");
  } finally { session.close(); }
}
