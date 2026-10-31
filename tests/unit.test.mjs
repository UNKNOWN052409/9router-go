#!/usr/bin/env node
/**
 * Unit tests for the pure functions of the shim and worker.
 * No network, no CLI spawn, no listeners — safe for CI.
 *
 *   node tests/unit.test.mjs
 *
 * Exit 0 = all pass; 1 = at least one failure.
 */
import { createRequire } from "node:module";
import path from "node:path";
import os from "node:os";
import assert from "node:assert";

const require = createRequire(import.meta.url);
// The live scripts guard their startup behind `require.main === module`, so
// requiring them here starts no servers and no job sweeps.
const shim = require("../reset-lane/shim/oc-cli-api.js");
const worker = require("../reset-lane/worker/oc-cli-worker.js");

let failed = 0;
function test(name, fn) {
  try { fn(); console.log("PASS  " + name); }
  catch (e) { failed++; console.log("FAIL  " + name + " — " + String(e.message || e).slice(0, 120)); }
}

// ---- shim: jobFilePath (path boundary) ----
test("jobFilePath accepts a valid id + fixed suffix", () => {
  const p = shim.jobFilePath("abc-123", ".job");
  assert.ok(p && p.endsWith("abc-123.job"));
});
test("jobFilePath rejects path traversal", () => {
  assert.strictEqual(shim.jobFilePath("../evil", ".job"), null);
  assert.strictEqual(shim.jobFilePath("a/b", ".job"), null);
  assert.strictEqual(shim.jobFilePath("..", ".job"), null);
});
test("jobFilePath rejects bad id characters", () => {
  assert.strictEqual(shim.jobFilePath("ABC", ".job"), null);       // uppercase
  assert.strictEqual(shim.jobFilePath("a".repeat(65), ".job"), null); // too long
  assert.strictEqual(shim.jobFilePath("-lead", ".job"), null);     // must start alnum
});
test("jobFilePath rejects non-literal suffixes", () => {
  assert.strictEqual(shim.jobFilePath("abc", ".job.txt.extra-long-suffix"), null);
});

// ---- shim: attachmentPath (extension whitelist + boundary) ----
test("attachmentPath allows whitelisted extensions inside ATTACH_DIR", () => {
  const p = shim.attachmentPath("attachment-1", "png");
  assert.ok(p && p.endsWith("attachment-1.png"));
});
test("attachmentPath rejects non-whitelisted extensions", () => {
  assert.strictEqual(shim.attachmentPath("attachment-1", "exe"), null);
  assert.strictEqual(shim.attachmentPath("attachment-1", "sh"), null);
});
test("attachmentPath rejects traversal bases", () => {
  assert.strictEqual(shim.attachmentPath("..%2Fevil", "png"), null);
  assert.strictEqual(shim.attachmentPath("a-b/c", "png"), null);
});

// ---- shim: buildPrompt (context budget + tool protocol) ----
test("buildPrompt keeps conversation when tools table is huge", () => {
  const tools = [];
  for (let i = 1; i <= 339; i++) {
    tools.push({ type: "function", function: { name: "ide_tool_" + i, description: "Tool " + i + " with a reasonably long description string for size.", parameters: { type: "object", properties: { path: { type: "string" } } } } });
  }
  const history = [];
  for (let i = 1; i <= 20; i++) {
    history.push({ role: "user", content: "Message " + i + ": NIMBUS-ROCKET deployment." });
    history.push({ role: "assistant", content: "Continuing NIMBUS-ROCKET step " + i + "." });
  }
  history.push({ role: "user", content: "What project are we working on?" });
  const out = shim.buildPrompt(history, tools, null, false, []);
  assert.ok(out.includes("NIMBUS-ROCKET"), "conversation must survive the budget");
  assert.ok(out.includes("===AVAILABLE_TOOLS==="), "tool protocol must be present");
});

// ---- shim: parseToolCall / parseToolCalls ----
test("parseToolCall extracts a bare JSON tool call", () => {
  const c = shim.parseToolCall('{"tool_call":{"name":"get_weather","arguments":{"city":"Tokyo"}}}');
  assert.strictEqual(c.name, "get_weather");
  assert.deepStrictEqual(c.arguments, { city: "Tokyo" });
});
test("parseToolCall extracts a fenced tool call with prose around it", () => {
  const c = shim.parseToolCall('Sure! ```json\n{"tool_call":{"name":"a","arguments":{}}}\n``` done');
  assert.strictEqual(c.name, "a");
});
test("parseToolCall returns null for plain text", () => {
  assert.strictEqual(shim.parseToolCall("Just a normal answer."), null);
});
test("parseToolCalls returns [] for plain text and non-tool JSON", () => {
  assert.deepStrictEqual(shim.parseToolCalls("Just a normal answer."), []);
  assert.deepStrictEqual(shim.parseToolCalls('{"result":{"answer":42}}'), []);
});
test("parseToolCalls extracts the multi-call tool_calls array", () => {
  const calls = shim.parseToolCalls('{"tool_calls":[{"name":"read_file","arguments":{"path":"a.ts"}},{"name":"read_file","arguments":{"path":"b.ts"}}]}');
  assert.strictEqual(calls.length, 2);
  assert.deepStrictEqual(calls.map((c) => c.arguments.path), ["a.ts", "b.ts"]);
});
test("parseToolCalls extracts several sequential call objects wrapped in prose", () => {
  const calls = shim.parseToolCalls('I will check both files. {"tool_call":{"name":"read_file","arguments":{"path":"a.ts"}}} and then {"tool_call":{"name":"read_file","arguments":{"path":"b.ts"}}} done.');
  assert.strictEqual(calls.length, 2);
});
test("parseToolCalls survives braces inside JSON strings", () => {
  const c = shim.parseToolCall('{"tool_call":{"name":"run_code","arguments":{"code":"const o = {a: 1}; print(o)"}}}');
  assert.strictEqual(c.name, "run_code");
  assert.strictEqual(c.arguments.code, "const o = {a: 1}; print(o)");
});
test("parseToolCalls accepts the wrapper-less {name, arguments} form", () => {
  const c = shim.parseToolCall('{"name":"get_weather","arguments":{"city":"Pune"}}');
  assert.strictEqual(c.name, "get_weather");
});
test("parseToolCalls repairs single-quoted JSON", () => {
  const c = shim.parseToolCall("{'tool_call':{'name':'a','arguments':{'x':'y'}}}");
  assert.strictEqual(c.name, "a");
  assert.deepStrictEqual(c.arguments, { x: "y" });
});
test("parseToolCalls takes the OpenAI function-envelope entries of tool_calls", () => {
  const calls = shim.parseToolCalls('{"tool_calls":[{"function":{"name":"a","arguments":"{\\"x\\":1}"}},{"function":{"name":"b","arguments":"{}"}}]}');
  assert.strictEqual(calls.length, 2);
  assert.deepStrictEqual(calls[0].arguments, { x: 1 });
});

// ---- shim: reasoning effort directive ----
test("buildPrompt appends a high-effort directive", () => {
  const out = shim.buildPrompt([{ role: "user", content: "q" }], [], null, false, [], "high");
  assert.ok(out.includes("Reasoning effort: high"));
});
test("buildPrompt appends a low-effort directive", () => {
  const out = shim.buildPrompt([{ role: "user", content: "q" }], [], null, false, [], "low");
  assert.ok(out.includes("Reasoning effort: low"));
});
test("buildPrompt adds no directive for medium or unset effort", () => {
  assert.ok(!shim.buildPrompt([{ role: "user", content: "q" }], [], null, false, [], "medium").includes("Reasoning effort"));
  assert.ok(!shim.buildPrompt([{ role: "user", content: "q" }], [], null, false, [], undefined).includes("Reasoning effort"));
});
test("buildToolsProtocol advertises the multi-invocation form", () => {
  const proto = shim.buildToolsProtocol([{ function: { name: "a", description: "x", parameters: {} } }], null);
  assert.ok(proto.includes("tool_calls"));
});

// ---- worker: runDirFor / partialPathFor / selectArgv ----
test("runDirFor rejects traversal and returns paths inside WORK_ROOT", () => {
  assert.strictEqual(worker.runDirFor("../x"), null);
  const p = worker.runDirFor("r1");
  assert.ok(p && p.includes("work"));
});
test("selectArgv returns literal base args for known models and default for unknown", () => {
  const glm = worker.selectArgv("glm-5.3-flash-free");
  assert.deepStrictEqual(glm.slice(0, 2), ["run", "--format"]);
  assert.ok(glm.includes("opencode/glm-5.3-flash-free"));
  const unknown = worker.selectArgv("totally-unknown-model");
  assert.ok(unknown.includes("opencode/mimo-v2.6-flash-free"), "falls back to default model");
});
test("MODEL_RUN_ARGS keys are the six supported free models", () => {
  assert.deepStrictEqual(Object.keys(worker.MODEL_RUN_ARGS).sort(), [
    "deepseek-v4-flash-free", "deepseek-v4.1-flash-free", "glm-5.3-flash-free",
    "jev-1.13-free", "mimo-v2.6-flash-free", "muse-spark-1.3-contributor-free",
  ]);
});

console.log("\nunit: " + (failed ? failed + " FAILED" : "all passed"));
if (failed) process.exit(1);

// ---- shim: normalizeModel (prefixes fully optional, case-insensitive) ----
test("normalizeModel passes canonical names through", () => {
  assert.strictEqual(shim.normalizeModel("glm-5.3-flash-free"), "glm-5.3-flash-free");
  assert.strictEqual(shim.normalizeModel(""), "glm-5.3-flash-free");
});
test("normalizeModel strips known provider prefixes", () => {
  assert.strictEqual(shim.normalizeModel("oc/glm-5.3-flash-free"), "glm-5.3-flash-free");
  assert.strictEqual(shim.normalizeModel("opencode/deepseek-v4.1-flash-free"), "deepseek-v4.1-flash-free");
  assert.strictEqual(shim.normalizeModel("ocz2/mimo-v2.6-flash-free"), "mimo-v2.6-flash-free");
});
test("normalizeModel resolves ANY prefix by its last segment", () => {
  assert.strictEqual(shim.normalizeModel("9router/glm-5.3-flash-free"), "glm-5.3-flash-free");
  assert.strictEqual(shim.normalizeModel("my-proxy/org/deepseek-v4.1-flash-free"), "deepseek-v4.1-flash-free");
});
test("normalizeModel is case-insensitive and returns the canonical name", () => {
  assert.strictEqual(shim.normalizeModel("GLM-5.3-FLASH-FREE"), "glm-5.3-flash-free");
  assert.strictEqual(shim.normalizeModel("OC/DeepSeek-V4.1-Flash-Free"), "deepseek-v4.1-flash-free");
});
test("normalizeModel leaves unknown names verbatim for the worker gate", () => {
  assert.strictEqual(shim.normalizeModel("gpt-42"), "gpt-42");
});

// ---- worker: selectArgv resolves prefixed names by last segment ----
test("selectArgv resolves any-prefixed model names via the last segment", () => {
  const prefixed = worker.selectArgv("9router/glm-5.3-flash-free");
  assert.ok(prefixed.includes("opencode/glm-5.3-flash-free"));
  const multi = worker.selectArgv("my-proxy/org/deepseek-v4.1-flash-free");
  assert.ok(multi.includes("opencode/deepseek-v4.1-flash-free"));
});

// ---- shim: smart compaction for big agentic coding tasks ----
test("buildPrompt compacts old tool results but keeps the last 3 verbatim", () => {
  const msgs = [];
  for (let i = 1; i <= 10; i++) {
    msgs.push({ role: "user", content: "step " + i });
    msgs.push({ role: "assistant", content: null, tool_calls: [{ id: "c" + i, type: "function", function: { name: "read_file", arguments: "{}" } }] });
    msgs.push({ role: "tool", tool_call_id: "c" + i, name: "read_file", content: "data".repeat(5000) });
  }
  const p = shim.buildPrompt(msgs, [], null, false, [], undefined);
  const verbatim = (p.match(/datadata|datadatadata/g) || []).length;
  assert.ok(p.includes("older tool result truncated"), "old tool results must be compacted");
  assert.ok(p.includes("step 10"), "recent turns must survive");
});
test("buildPrompt truncates a gigantic system turn instead of dropping it", () => {
  const big = "SYS-PREAMBLE ".repeat(40000); // ~520k chars
  const p = shim.buildPrompt([{ role: "system", content: big }, { role: "user", content: "Reply EXT-OK" }], [], null, false, [], undefined);
  assert.ok(p.length <= 250000 + 64, "cap respected");
  assert.ok(p.includes("SYS-PREAMBLE"), "system preamble head survives");
  assert.ok(p.includes("EXT-OK"), "recent turn survives");
});
