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
const shim = require("../scripts/oc-cli-api.js");
const worker = require("../scripts/oc-cli-worker.js");

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

// ---- shim: parseToolCall ----
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
