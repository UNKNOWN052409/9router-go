#!/usr/bin/env node
/**
 * opencode CLI -> OpenAI-compatible API shim (HTTP front-end).
 *
 * No child processes here: this server writes job files into
 * D:\scratch\oc-cli-api\jobs\ and the oc-cli-worker.js runner (a separate
 * process that spawns the REAL opencode CLI) picks them up. Each job is a
 * brand-new CLI session, which is the free-tier's fresh-quota unit.
 *
 * POST /v1/chat/completions  — waits (max 180s) for the worker's result and
 *                              answers in OpenAI format (SSE when asked).
 * GET  /v1/models            — lists glm-5.3-flash-free
 * GET  /health               — liveness
 *
 * Binds 127.0.0.1 only.
 */
const http = require("http");
const https = require("https");
const fs = require("fs");
const path = require("path");
const os = require("os");

const PORT = 20142;
const HOST = "127.0.0.1";
// Data root matches the worker's derivation (OC_DATA_DIR env overrides);
// keeping both sides on the same root is what makes the file-queue work.
const DATA_ROOT = path.resolve(
  process.env.OC_DATA_DIR ||
  (process.platform === "win32" ? "D:\\scratch\\oc-cli-api" : path.join(os.homedir(), ".oc-free-tier-shim"))
);
const JOBS_DIR = path.join(DATA_ROOT, "jobs");
const PARTIALS_DIR = path.join(DATA_ROOT, "partials");
const MODEL_ID = "glm-5.3-flash-free";
// Known free-model names — the whitelist used by model normalization below
// and advertised by GET /v1/models.
const MODELS = [
  "glm-5.3-flash-free",
  "mimo-v2.6-flash-free",
  "deepseek-v4-flash-free",
  "deepseek-v4.1-flash-free",
  "muse-spark-1.3-contributor-free",
  "jev-1.13-free",
];
const KNOWN_MODELS = new Set(MODELS);

// Resolves a client-supplied model name to the model that should run: exact
// known name wins (case-insensitive, canonical form returned), then a known
// provider prefix is stripped, then the last path segment after ANY prefix is
// tried (9router/x, my-proxy/org/x, …). Unknown names pass through verbatim
// for the worker whitelist to judge.
function normalizeModel(raw) {
  let m = String(raw || "").trim();
  if (!m) return MODEL_ID;
  const canonical = (s) => {
    for (const k of MODELS) if (k.toLowerCase() === s.toLowerCase()) return k;
    return null;
  };
  const hit = canonical(m);
  if (hit) return hit;
  m = m.replace(/^(opencode|oc|ocz2|ocz)\/+/i, "").trim();
  const hit2 = canonical(m);
  if (hit2) return hit2;
  const last = m.slice(m.lastIndexOf("/") + 1).trim();
  const hit3 = canonical(last);
  if (hit3) return hit3;
  return m;
}
const WAIT_MS = 180000;
const POLL_MS = 150;
const MAX_PROMPT_CHARS = 250000;

const LOG_FILE = path.join(__dirname, "oc-cli-api.log");
function log(...parts) {
  const line = new Date().toISOString() + " " + parts.join(" ") + "\n";
  try { fs.appendFileSync(LOG_FILE, line); } catch (_) {}
  process.stdout.write(line);
}

const SAFE_ID = /^[a-z0-9][a-z0-9-]{0,63}$/;

function jobFilePath(name, suffix) {
  if (!SAFE_ID.test(name)) return null;
  if (!/^\.[a-z0-9.]{1,10}$/.test(suffix)) return null; // suffix is a fixed literal like ".job" / ".out.json"
  const target = path.resolve(JOBS_DIR, name + suffix);
  const root = path.resolve(JOBS_DIR) + path.sep;
  if (!target.startsWith(root)) return null;
  return target;
}

// Image attachments: decoded data-URL image parts are written to ATTACH_DIR
// under generated names; the prompt references those absolute paths and the
// CLI (an agent with file-read tools) reads them itself — verified: "Describe
// the image at <path>" makes glm describe the image correctly.
const ATTACH_DIR = path.join(DATA_ROOT, "attachments");
// Per-request attachment index: files are named attachment-1..N so the
// worker's staging loop (i = 1..attachCount) finds exactly this request's
// files. A process-global growing counter would leak old indices into new
// requests (worker would look for attachment-8342.png and stage nothing).
let attachIndex = 0;

// Resolves an attachment filename with the same boundary rules as
// jobFilePath: the base must be lowercase alphanumeric + dash, the
// extension must come from the fixed four-image-type whitelist, and the
// resolved path must stay inside ATTACH_DIR.
function attachmentPath(base, ext) {
  if (!/^[a-z0-9][a-z0-9-]{0,63}$/.test(base)) return null;
  let okExt;
  if (ext === "png") okExt = ".png";
  else if (ext === "jpg") okExt = ".jpg";
  else if (ext === "gif") okExt = ".gif";
  else if (ext === "webp") okExt = ".webp";
  else return null;
  const target = path.resolve(ATTACH_DIR, base + okExt);
  const root = ATTACH_DIR + path.sep;
  if (!target.startsWith(root)) return null;
  return target;
}

// Extracts data-URL image parts from a message array, saves them, and
// returns their absolute paths. Remote http URLs are skipped (the CLI can
// fetch those itself).
function saveImageAttachments(messages) {
  const saved = [];
  attachIndex = 0;
  try {
    fs.mkdirSync(ATTACH_DIR, { recursive: true });
    // Sweep leftovers from earlier requests: with per-request names
    // attachment-1..N, an un-pruned old attachment-1.png would be staged
    // into the next request even when that request sends no images.
    for (const f of fs.readdirSync(ATTACH_DIR)) {
      if (/^attachment-[0-9]+\.(png|jpg|gif|webp)$/i.test(f)) {
        try { fs.unlinkSync(path.join(ATTACH_DIR, f)); } catch (_) {}
      }
    }
  } catch (_) {}
  for (const msg of Array.isArray(messages) ? messages : []) {
    if (!Array.isArray(msg.content)) continue;
    for (const part of msg.content) {
      if (!part || part.type !== "image_url" || !part.image_url) continue;
      const url = String(part.image_url.url || "");
      const m = url.match(/^data:image\/(png|jpeg|gif|webp);base64,([A-Za-z0-9+\/=\s]+)$/);
      if (!m) continue;
      let ext;
      if (m[1] === "png") ext = "png";
      else if (m[1] === "jpeg") ext = "jpg";
      else if (m[1] === "gif") ext = "gif";
      else if (m[1] === "webp") ext = "webp";
      else continue;
      attachIndex = attachIndex + 1;
      const target = attachmentPath("attachment-" + attachIndex, ext);
      if (!target) continue;
      try {
        fs.writeFileSync(target, Buffer.from(m[2].replace(/\s+/g, ""), "base64"));
        saved.push(target);
      } catch (_) {}
      if (saved.length >= 4) return saved;
    }
  }
  return saved;
}

// Tool bridge: the CLI has no native tool-calling API, so definitions are
// serialized into the prompt under a strict JSON protocol and the model's
// reply is parsed back into OpenAI tool_calls. The sentinel lines make the
// protocol unambiguous even when the conversation itself mentions JSON.
const TOOL_SENTINEL_BEGIN = "===AVAILABLE_TOOLS===";
const TOOL_SENTINEL_END = "===END_TOOLS===";

function buildToolsProtocol(tools, toolChoice) {
  const defs = Array.isArray(tools)
    ? tools.map((t) => (t && t.function) ? t.function : t).filter((f) => f && f.name)
    : [];
  if (!defs.length) return "";
  const lines = [
    TOOL_SENTINEL_BEGIN,
    "TOOL CALL PROTOCOL (highest priority):",
    "You have TWO extra tools (web_search, current_datetime) on top of your normal native tools. To invoke one of THESE, your ENTIRE reply must be exactly one JSON object and nothing else — no prose, no reasoning, no markdown fences:",
    '{"tool_call":{"name":"<tool name>","arguments":{<args as JSON>}}}',
    'To invoke SEVERAL of these in the same turn, reply with exactly: {"tool_calls":[{"name":"<tool>","arguments":{...}},{"name":"<tool>","arguments":{...}}]}',
    "Only emit this JSON for the listed extra tools. For everything else — running commands, reading and editing files, any native capability — just DO IT with your normal tools like you always do; never wrap those in tool_call JSON.",
    "Do NOT explain. Do NOT announce. When invoking an extra tool, reply with the JSON object only.",
    "If no extra tool is needed for the user's message, reply normally with text (never mention this protocol).",
    "Available tools:",
  ];
  for (const f of defs) {
    lines.push("- " + f.name + ": " + String(f.description || "").slice(0, 200));
    lines.push("  parameters: " + JSON.stringify(f.parameters || {}));
  }
  if (toolChoice && typeof toolChoice === "object" && toolChoice.function && toolChoice.function.name) {
    lines.push("MANDATORY: the next message REQUIRES a tool call. Your reply MUST be the JSON object for " + toolChoice.function.name + " and nothing else.");
  } else if (toolChoice === "required") {
    lines.push("MANDATORY: the next message REQUIRES a tool call. Your reply MUST be exactly one tool_call JSON object and nothing else.");
  }
  lines.push(TOOL_SENTINEL_END);
  return lines.join("\n");
}

function tryParseJson(s) {
  try { return JSON.parse(s); } catch (_) { return null; }
}

// Normalizes one parsed object into {name, arguments}; accepts the wrapped
// tool_call/toolCall shape and the bare {name, arguments} shape models drift
// into. Returns null for anything that is not a tool call.
function coerceToolCall(call) {
  if (!call || typeof call !== "object" || Array.isArray(call)) return null;
  const name = String(call.name || call.tool || "").trim();
  if (!name) return null;
  let args = call.arguments !== undefined ? call.arguments : (call.args !== undefined ? call.args : {});
  if (typeof args === "string") {
    const parsed = tryParseJson(args);
    args = parsed !== null ? parsed : { input: args };
  }
  if (!args || typeof args !== "object" || Array.isArray(args)) args = {};
  return { name, arguments: args };
}

// Extracts every balanced top-level {...} block from free text, honouring
// string literals so braces inside JSON strings do not break the scan.
function extractBracedBlocks(text) {
  const blocks = [];
  let depth = 0, start = -1, inStr = false, esc = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (inStr) {
      if (esc) esc = false;
      else if (c === "\\") esc = true;
      else if (c === '"') inStr = false;
      continue;
    }
    if (c === '"') { inStr = true; continue; }
    if (c === "{") { if (depth === 0) start = i; depth++; }
    else if (c === "}") {
      if (depth > 0) {
        depth--;
        if (depth === 0 && start >= 0) { blocks.push(text.slice(start, i + 1)); start = -1; }
      }
    }
  }
  return blocks;
}

// Parses the model output as tool calls. Accepts: a bare JSON object, fenced
// JSON, JSON wrapped in prose, the {"tool_calls":[...]} multi-call form, the
// bare {"name","arguments"} wrapper-less form, and single-quoted JSON. The
// balanced-block scan collects every candidate so prose around the call or
// trailing text after it no longer breaks extraction. Returns an array
// (0..n); an empty array means the text is a normal answer.
function parseToolCalls(text) {
  if (!text) return [];
  let raw = String(text).trim();
  const fenced = raw.match(/```(?:json)?\s*([\s\S]*?)```/);
  if (fenced && fenced[1].trim().startsWith("{")) raw = fenced[1].trim();
  const calls = [];
  const accept = (obj) => {
    if (!obj || typeof obj !== "object") return;
    if (Array.isArray(obj.tool_calls)) {
      for (const c of obj.tool_calls) {
        const k = coerceToolCall(c && typeof c === "object" && c.function ? c.function : c);
        if (k) calls.push(k);
      }
      return;
    }
    const inner = obj.tool_call || obj.toolCall;
    if (inner && typeof inner === "object") { const k = coerceToolCall(inner); if (k) calls.push(k); return; }
    if (typeof obj.name === "string" && (obj.arguments !== undefined || obj.args !== undefined)) {
      const k = coerceToolCall(obj);
      if (k) calls.push(k);
    }
  };
  const direct = tryParseJson(raw);
  if (direct) { accept(direct); if (calls.length) return calls; }
  for (const block of extractBracedBlocks(raw)) {
    let obj = tryParseJson(block);
    if (!obj) {
      const repaired = block
        .replace(/([{,]\s*)'([^'\\]+)'(\s*:)/g, '$1"$2"$3')
        .replace(/:\s*'([^'\\]*)'/g, ': "$1"');
      obj = tryParseJson(repaired);
    }
    if (obj) accept(obj);
  }
  return calls;
}

// Back-compat single-call view of parseToolCalls.
function parseToolCall(text) {
  const calls = parseToolCalls(text);
  return calls.length ? calls[0] : null;
}

// ---------------------------------------------------------------------------
// PASSIVE TOOLS LAYER — built-in tools the model can use even when the client
// sent none. The tool loop runs entirely inside the shim: the model's call is
// executed HERE (web search hits DuckDuckGo's HTML endpoint; datetime is
// local), the result is appended to the conversation, and a fresh CLI pass
// answers with it. Client-supplied tools always take precedence: when the
// request carries tools, the passive layer stays out entirely. Non-stream
// requests only (streaming with a hidden first pass would leak the protocol
// JSON into content deltas). OC_PASSIVE_TOOLS=0 disables the layer.
// ---------------------------------------------------------------------------
const PASSIVE_TOOLS = [
  { type: "function", function: {
    name: "web_search",
    description: "Search the public web and return the top results (title, url, snippet). Use for anything current, factual or beyond your knowledge.",
    parameters: { type: "object", properties: { query: { type: "string", description: "Search query" } }, required: ["query"] },
  } },
  { type: "function", function: {
    name: "current_datetime",
    description: "Returns today's date and the current local time.",
    parameters: { type: "object", properties: {} },
  } },
];
const PASSIVE_LOOP_MAX = 3;
const passiveEnabled = () => process.env.OC_PASSIVE_TOOLS !== "0";

function stripTags(html) {
  return String(html || "").replace(/<[^>]*>/g, " ").replace(/&amp;/g, "&").replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">").replace(/&quot;/g, '"').replace(/&#x27;|&#39;/g, "'").replace(/&nbsp;/g, " ")
    .replace(/\s+/g, " ").trim();
}

async function executeWebSearch(query) {
  const url = "https://html.duckduckgo.com/html/?q=" + encodeURIComponent(String(query || "").slice(0, 400));
  const res = await fetch(url, { signal: AbortSignal.timeout(12000), headers: { "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64)", "Accept": "text/html" } });
  if (!res.ok) return "web_search error: upstream " + res.status;
  const html = await res.text();
  const results = [];
  const re = /<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]*)"[^>]*>([\s\S]*?)<\/a>/g;
  let m;
  while ((m = re.exec(html)) && results.length < 5) {
    let href = m[1];
    const uddg = href.match(/uddg=([^&]+)/);
    if (uddg) href = decodeURIComponent(uddg[1]);
    results.push({ title: stripTags(m[2]), url: href });
  }
  const snips = [...html.matchAll(/<a[^>]*class="[^"]*result__snippet[^"]*"[^>]*>([\s\S]*?)<\/a>/g)].slice(0, results.length);
  snips.forEach((s, i) => { results[i].snippet = stripTags(s[1]).slice(0, 300); });
  if (!results.length) return "web_search: no results for " + JSON.stringify(String(query).slice(0, 120));
  return results.map((r, i) => (i + 1) + ". " + r.title + "\n   " + r.url + (r.snippet ? "\n   " + r.snippet : "")).join("\n");
}

async function executePassiveTool(call) {
  if (call.name === "current_datetime") {
    return new Date().toString();
  }
  if (call.name === "web_search") {
    try { return await executeWebSearch(call.arguments.query); }
    catch (e) { return "web_search error: " + String(e && e.message || e).slice(0, 160); }
  }
  return null;
}

const isPassiveTool = (name) => name === "web_search" || name === "current_datetime";

function buildPrompt(messages, tools, toolChoice, jsonMode, imagePaths, reasoningEffort) {
  const turns = [];
  for (const msg of Array.isArray(messages) ? messages : []) {
    const role = String(msg.role || "user").toLowerCase();
    let text = "";
    if (typeof msg.content === "string") text = msg.content;
    else if (Array.isArray(msg.content))
      text = msg.content
        .filter((c) => c && c.type === "text" && c.text)
        .map((c) => c.text)
        .join("\n");
    // Tool-result turns carry the outcome the model needs to answer with;
    // assistant turns may carry the tool_call the model made previously.
    if (role === "tool") {
      const name = String(msg.name || msg.tool_call_id || "tool");
      if (String(text).trim()) turns.push("[Tool result for " + name + "]\n" + text);
      continue;
    }
    if (role === "assistant" && Array.isArray(msg.tool_calls) && msg.tool_calls.length) {
      const calls = msg.tool_calls.map((tc) => {
        const f = (tc && tc.function) || {};
        return '{"tool_call":{"name":"' + String(f.name || "") + '","arguments":' + (f.arguments || "{}") + "}}";
      }).join(" ");
      const pre = String(text || "").trim();
      turns.push("[Assistant]" + (pre ? "\n" + pre : "") + (pre ? "\n" : "") + calls);
      continue;
    }
    if (!String(text).trim()) continue;
    if (role === "system") turns.push("[System]\n" + text);
    else if (role === "assistant") turns.push("[Assistant]\n" + text);
    else turns.push("[User]\n" + text);
  }
  // Budget first: overflow drops the OLDEST turns, keeps the leading
  // [System] turn and the most recent conversation (measured live: a
  // 599,969-char IDE request sailed through the old head-keeping cap at
  // 444k tokens and the CLI timed out on every attempt).
  const protoEarly = buildToolsProtocol(
    (!Array.isArray(tools) || !tools.length) && passiveEnabled() ? PASSIVE_TOOLS : tools,
    toolChoice
  );
  const budget = Math.max(MAX_PROMPT_CHARS - protoEarly.length - 512, 40000);
  let kept = [];
  let keptCount = 0;
  let used = 0;
  const head = turns.length && turns[0].startsWith("[System]") ? turns[0] : null;
  const pool = head ? turns.slice(1) : turns;
  if (head && head.length + 8 <= budget) { kept.push(head); used = head.length + 8; }
  for (let i = pool.length - 1; i >= 0; i--) {
    const cost = pool[i].length + 8;
    if (used + cost > budget) break;
    kept.unshift(pool[i]);
    used += cost;
    keptCount++;
  }
  let out = kept.join("\n\n").trim();
  if (!out) out = "hello";
  const dropped = turns.length - keptCount - (head ? 1 : 0);
  if (dropped > 0) out = "[older messages truncated to fit the model context]\n\n" + out;
  // Reasoning effort: the CLI exposes no sampling knobs through this path,
  // so the OpenAI request's effort is honored as a directive. Medium (the
  // model default) adds nothing; only low/high change behavior.
  const effort = String(reasoningEffort || "").toLowerCase();
  if (effort === "high") {
    out += "\n\n[SYSTEM] Reasoning effort: high. Think the problem through step by step before answering — weigh alternatives, check your work, then answer thoroughly.";
  } else if (effort === "low") {
    out += "\n\n[SYSTEM] Reasoning effort: low. Minimize deliberation — answer concisely and directly.";
  }
  const proto = protoEarly;
  if (proto) {
    out = proto + "\n\n" + out;
    // Repeat the mandate closest to the end — recency wins with agentic models.
    if (toolChoice === "required" || (toolChoice && typeof toolChoice === "object" && toolChoice.function)) {
      const forced = toolChoice.function ? toolChoice.function.name : null;
      out += "\n\n[SYSTEM] This turn requires a tool call. Reply with ONLY the " + (forced ? '"' + forced + '" ' : "") + "tool_call JSON object — no prose.";
    }
  }
  if (out.length > MAX_PROMPT_CHARS) out = out.slice(0, MAX_PROMPT_CHARS) + "\n...[truncated]";
  return out;
}

let counter = 0;

const WORKER_RUN_URL = "http://127.0.0.1:20194/run";

function submitJob(prompt, model, attachCount, stage) {
  counter = (counter + 1) % 100000;
  const id = "r" + Date.now().toString(36) + "-" + counter;
  const jobPath = jobFilePath(id, ".job");
  if (!jobPath) throw new Error("bad job id");
  let n = Math.floor(Number(attachCount));
  if (!Number.isFinite(n) || n < 0) n = 0;
  if (n > 4) n = 4;
  const payload = JSON.stringify({ id, prompt, model: String(model || ""), attach: n, stage: stage || undefined });
  fs.writeFileSync(jobPath, payload);
  // DIRECT path: hand the job to the worker the same millisecond instead of
  // waiting for the queue sweep. Fire-and-forget — the file stays on disk,
  // so a failed POST simply means the sweep picks it up (fallback, not a
  // second run: the worker unlinks whichever copy it sees first).
  const req = http.request(WORKER_RUN_URL, { method: "POST", headers: { "content-type": "application/json", "content-length": Buffer.byteLength(payload) }, timeout: 1500 }, (res) => { res.resume(); });
  req.on("error", () => {});
  req.on("timeout", () => req.destroy());
  req.end(payload);
  return { id, jobPath };
}

function waitForResult(id) {
  const outPath = jobFilePath(id, ".out.json");
  return new Promise((resolve) => {
    const start = Date.now();
    const poll = () => {
      try {
        if (outPath && fs.existsSync(outPath)) {
          let result;
          try { result = JSON.parse(fs.readFileSync(outPath, "utf8")); } catch (e) { result = { error: String(e) }; }
          try { fs.unlinkSync(outPath); } catch (_) {}
          return resolve(result);
        }
      } catch (_) {}
      if (Date.now() - start > WAIT_MS) return resolve({ text: "", error: "timeout waiting for worker" });
      setTimeout(poll, POLL_MS);
    };
    poll();
  });
}

function sendJson(res, status, obj) {
  const body = JSON.stringify(obj);
  res.writeHead(status, { "Content-Type": "application/json", "Content-Length": Buffer.byteLength(body) });
  res.end(body);
}

async function handleChat(res, body) {
  let parsed;
  try { parsed = JSON.parse(body || "{}"); } catch (_) { parsed = {}; }
  // Strip markdown fences from a JSON-mode answer so clients parsing
  // response_format:"json_object" get pure JSON.
  function stripJsonFences(s) {
    let raw = String(s || "").trim();
    const m = raw.match(/^```(?:json)?\s*([\s\S]*?)\s*```$/);
    if (m) raw = m[1].trim();
    return raw;
  }

  const jsonMode = parsed.response_format && parsed.response_format.type === "json_object";
  const reasoningEffort = String(parsed.reasoning_effort || (parsed.reasoning && parsed.reasoning.effort) || "").toLowerCase();
  const imagePaths = saveImageAttachments(parsed.messages);
  if (imagePaths.length) log("images attached:", imagePaths.length);
  const prompt = buildPrompt(parsed.messages, parsed.tools, parsed.tool_choice, jsonMode, imagePaths, reasoningEffort);
  // Tool-call parsing runs ONLY when the request carried tools: a plain chat
  // answer that happens to contain JSON must stay a text answer.
  const toolsPresent = Array.isArray(parsed.tools) && parsed.tools.length > 0;
  const stream = parsed.stream === true;
  const created = Math.floor(Date.now() / 1000);
  const id = "chatcmpl-occli-" + Date.now().toString(36);
  const reqStart = Date.now();
  log("chat request, promptChars=" + prompt.length, stream ? "(stream)" : "", parsed.tools ? ("tools=" + parsed.tools.length) : "", reasoningEffort ? ("effort=" + reasoningEffort) : "");

  let job;
  // Prefixes are fully optional and ANY prefix is accepted: the model runs by
  // its name, never by how it was addressed. Resolution order: exact known
  // name → known prefix stripped → last path segment after ANY prefix
  // (9router/x, my-proxy/org/x, …) → verbatim (the worker whitelist then
  // decides; unknown names fall back to the default model, documented).
  const reqModel = normalizeModel(parsed.model);
  // Optional project staging: point "stage" at an absolute folder and the
  // worker copies it into the fresh run dir (native agent access) and syncs
  // changed files back afterwards — the OpenCode-direct workflow.
  let stage = typeof parsed.stage === "string" ? parsed.stage.trim() : "";
  if (stage && (!path.isAbsolute(stage) || !fs.existsSync(stage) || !fs.statSync(stage).isDirectory())) {
    return sendJson(res, 400, { error: { message: "stage must be an existing absolute directory path" } });
  }
  try { job = submitJob(prompt, reqModel, imagePaths.length, stage); } catch (e) {
    return sendJson(res, 500, { error: { message: "job submit failed: " + String(e) } });
  }

  const resultPath = jobFilePath(job.id, ".out.json");
  // The worker appends text deltas to PARTIALS_DIR\<id>.txt; tail exactly
  // that file (the old shim built a JOBS_DIR .partial.txt the worker never
  // wrote, so streaming fell back to one final chunk).
  let partialSafe = null;
  if (SAFE_ID.test(job.id)) {
    const pp = path.resolve(PARTIALS_DIR, job.id + ".txt");
    if (pp.startsWith(path.resolve(PARTIALS_DIR) + path.sep)) partialSafe = pp;
  }

  if (stream) {
    // TRUE incremental streaming: tail the worker's partial file and forward
    // every delta as an SSE chunk as the CLI generates it; the result file
    // marks completion. When the request carried tools, incremental text is
    // SUPPRESSED instead: the model may be composing tool-call JSON, and
    // protocol JSON that leaks into content deltas cannot be retracted at
    // completion — buffered text is emitted once the shape is known.
    try {
      res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache", Connection: "keep-alive" });
      res.write("data: " + JSON.stringify({ id, object: "chat.completion.chunk", created, model: reqModel,
        choices: [{ index: 0, delta: { role: "assistant" }, finish_reason: null }] }) + "\n\n");
      let offset = 0;
      const start = Date.now();
      const chunk = (delta, finish) => {
        res.write("data: " + JSON.stringify({ id, object: "chat.completion.chunk", created, model: reqModel,
          choices: [{ index: 0, delta, finish_reason: finish === undefined ? null : finish }] }) + "\n\n");
      };
      const poll = () => {
        try {
          if (!toolsPresent && partialSafe && fs.existsSync(partialSafe)) {
            const stat = fs.statSync(partialSafe);
            if (stat.size > offset) {
              const fd = fs.openSync(partialSafe, "r");
              const buf = Buffer.alloc(stat.size - offset);
              fs.readSync(fd, buf, 0, buf.length, offset);
              fs.closeSync(fd);
              offset = stat.size;
              chunk({ content: buf.toString("utf8") });
            }
          }
          if (resultPath && fs.existsSync(resultPath)) {
            let result;
            try { result = JSON.parse(fs.readFileSync(resultPath, "utf8")); } catch (_) { result = {}; }
            try { fs.unlinkSync(resultPath); } catch (_) {}
            // Flush whatever text was not already streamed via partials —
            // short answers can land in a single CLI event that the poll
            // never catches mid-flight.
            const full = result.text || "";
            const calls = toolsPresent ? parseToolCalls(full) : [];
            if (calls.length) {
              calls.forEach((call, i) => {
                chunk({ tool_calls: [{ index: i, id: "call_" + Date.now().toString(36) + "-" + i, type: "function",
                  function: { name: call.name, arguments: JSON.stringify(call.arguments) } }] });
              });
              chunk({}, "tool_calls");
            } else {
              if (result.reasoning) chunk({ reasoning_content: result.reasoning });
              if (full.length > offset) chunk({ content: full.slice(offset) });
              if (result.error && !full) chunk({ content: "upstream error: " + result.error });
              chunk({}, "stop");
            }
            res.write("data: [DONE]\n\n");
            res.end();
            recordMetrics(reqModel, Date.now() - reqStart, (result.error && !full) ? (/429|FreeUsageLimit|limit/i.test(result.error) ? "limit" : "error") : "ok");
            return;
          }
          if (Date.now() - start > WAIT_MS) {
            chunk({}, "stop");
            res.write("data: [DONE]\n\n");
            res.end();
            recordMetrics(reqModel, Date.now() - reqStart, "error");
            return;
          }
          setTimeout(poll, 100);
        } catch (e) {
          try { res.end(); } catch (_) {}
        }
      };
      poll();
    } catch (e) { try { res.end(); } catch (_) {} }
    return;
  }

  // Non-stream: wait for the final result. When the client sent NO tools and
  // the passive layer is on, a built-in tool call (web_search, …) is executed
  // right here and the conversation is re-run with the result — the client
  // just sees the final answer.
  let result = await waitForResult(job.id);
  if (passiveEnabled() && !toolsPresent) {
    for (let hop = 0; hop < PASSIVE_LOOP_MAX; hop++) {
      const calls = parseToolCalls(result.text || "").filter((c) => isPassiveTool(c.name));
      if (!calls.length) break;
      log("passive tool hop " + (hop + 1) + ":", calls.map((c) => c.name + JSON.stringify(c.arguments)).join(" | ").slice(0, 200));
      const turns = [];
      for (const call of calls) {
        const out = await executePassiveTool(call);
        turns.push("[Tool result for " + call.name + "]" + String.fromCharCode(10) + String(out));
      }
      const followup = buildPrompt(
        parsed.messages.concat([{ role: "assistant", content: result.text }, { role: "tool", name: calls[0].name, content: turns.join(String.fromCharCode(10) + String.fromCharCode(10)) },
          { role: "user", content: "Continue using the tool result above. Answer the user's original question now — if you have enough information, answer in plain text (no tool JSON)." }]),
        null, null, jsonMode, imagePaths, reasoningEffort);
      let hopJob;
      try { hopJob = submitJob(followup, reqModel, 0); } catch (e) { break; }
      const next = await waitForResult(hopJob.id);
      if (next.error && !next.text) break;
      result = next;
    }
  }
  const errText = result.error || "";
  const text = result.text || "";
  recordMetrics(reqModel, Date.now() - reqStart, errText && !text ? (/429|FreeUsageLimit|limit/i.test(errText) ? "limit" : "error") : "ok");
  // The worker forwards the CLI's step-finish token counts when present; map
  // them onto OpenAI usage (reasoning tokens ride completion_tokens_details
  // per the OpenAI convention). All zeros when the CLI reported nothing.
  const tk = result.tokens && typeof result.tokens === "object" ? result.tokens : null;
  const usage = {
    prompt_tokens: tk && Number.isFinite(tk.input) ? tk.input : 0,
    completion_tokens: tk && Number.isFinite(tk.output) ? tk.output : 0,
    total_tokens: 0,
  };
  usage.total_tokens = usage.prompt_tokens + usage.completion_tokens;
  if (tk && Number.isFinite(tk.reasoning) && tk.reasoning > 0) {
    usage.completion_tokens_details = { reasoning_tokens: tk.reasoning };
  }
  if (errText && !text) {
    sendJson(res, 502, { error: { message: errText, type: "upstream_error" } });
  } else {
    const calls = toolsPresent ? parseToolCalls(text) : [];
    if (calls.length) {
      sendJson(res, 200, {
        id, object: "chat.completion", created, model: reqModel,
        choices: [{ index: 0, message: { role: "assistant", content: null,
          tool_calls: calls.map((call, i) => ({ id: "call_" + Date.now().toString(36) + "-" + i, type: "function",
            function: { name: call.name, arguments: JSON.stringify(call.arguments) } })) },
          finish_reason: "tool_calls" }],
        usage,
      });
    } else {
      const out = jsonMode ? stripJsonFences(text) : text;
      const message = { role: "assistant", content: out };
      if (result.reasoning) message.reasoning_content = result.reasoning;
      sendJson(res, 200, {
        id, object: "chat.completion", created, model: reqModel,
        choices: [{ index: 0, message, finish_reason: "stop" }],
        usage,
      });
    }
  }
}

// ---------------------------------------------------------------------------
// Lightweight metrics: per-model request counts, errors, limit hits and
// latency percentiles. Dependency-free, in-memory, bounded (500-sample ring).
// Exposed at GET /v1/metrics so any deployment (Render, VPS, local) can
// scrape it and watch performance without extra tooling.
// ---------------------------------------------------------------------------
const METRICS = {
  startedAt: Date.now(),
  total: 0, ok: 0, errors: 0, limitHits: 0,
  byModel: new Map(),          // model -> { total, ok, errors, limitHits, latencies: [] }
  ring: [],                    // recent latencies, capped
  RING_CAP: 500,
};

function recordMetrics(model, ms, kind) {
  // kind: "ok" | "error" | "limit"
  METRICS.total++;
  if (kind === "ok") METRICS.ok++;
  else if (kind === "limit") { METRICS.errors++; METRICS.limitHits++; }
  else METRICS.errors++;
  let m = METRICS.byModel.get(model);
  if (!m) { m = { total: 0, ok: 0, errors: 0, limitHits: 0, latencies: [] }; METRICS.byModel.set(model, m); }
  m.total++;
  m[kind === "ok" ? "ok" : (kind === "limit" ? "limitHits" : "errors")]++;
  m.latencies.push(ms);
  if (m.latencies.length > 200) m.latencies.shift();
  METRICS.ring.push(ms);
  if (METRICS.ring.length > METRICS.RING_CAP) METRICS.ring.shift();
}

function pct(sorted, p) {
  if (!sorted.length) return 0;
  const idx = Math.min(sorted.length - 1, Math.floor((p / 100) * sorted.length));
  return sorted[idx];
}

function metricsSummary() {
  const all = [...METRICS.ring].sort((a, b) => a - b);
  const byModel = {};
  for (const [model, m] of METRICS.byModel) {
    const lat = [...m.latencies].sort((a, b) => a - b);
    byModel[model] = {
      total: m.total, ok: m.ok, errors: m.errors, limitHits: m.limitHits,
      latencyMs: { avg: Math.round(lat.reduce((a, b) => a + b, 0) / (lat.length || 1)), p50: pct(lat, 50), p95: pct(lat, 95) },
    };
  }
  return {
    uptimeSec: Math.round((Date.now() - METRICS.startedAt) / 1000),
    requests: { total: METRICS.total, ok: METRICS.ok, errors: METRICS.errors, limitHits: METRICS.limitHits },
    latencyMs: { avg: Math.round(all.reduce((a, b) => a + b, 0) / (all.length || 1)), p50: pct(all, 50), p95: pct(all, 95) },
    byModel,
  };
}

const server = http.createServer((req, res) => {
  if (req.method === "GET" && /^\/v1\/metrics$/.test(req.url || "")) {
    return sendJson(res, 200, metricsSummary());
  }
  if (req.method === "GET" && /^\/v1\/models$/.test(req.url || "")) {
    // Advertise every model the worker's literal argv table supports — the
    // 9router-go gateway probes this list and exposes each as ocz2/<id>.
    return sendJson(res, 200, {
      object: "list",
      data: MODELS.map((m) => ({ id: m, object: "model", owned_by: "opencode-free" })),
    });
  }
  if (req.method === "POST" && /^\/(v1|inference\/v1|inference\/openai\/v1)\/chat\/completions$/.test(req.url || "")) {
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("error", () => {});
    req.on("end", () => { handleChat(res, Buffer.concat(chunks).toString("utf8")); });
    return;
  }
  // Keyed zen lane pass-through: the gateway's opencode-zen connection
  // points its base URL here so its FREE models get the fresh-session reset
  // from this shim, while its PAID (zen-key) requests arrive as /zen/v1/...
  // and are forwarded to the real upstream - same key header, same body,
  // streamed both ways.
  //
  // SSRF-safe by construction: destination host/port/scheme are
  // compile-time literals (https://opencode.ai:443). The request path must
  // match the /zen/v1/ prefix and a strict character whitelist (no scheme
  // switches, no userinfo, no double slashes). Only an explicit allowlist
  // of header names is copied - caller headers cannot inject Host or
  // override the destination.
  if (/^\/zen\/v1\/[a-z0-9\/._-]*$/i.test(req.url || "")) {
    const ZEN_HOST = "opencode.ai";
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("error", () => {});
    req.on("end", () => {
      const body = Buffer.concat(chunks);
      const headers = { host: ZEN_HOST };
      const allow = ["content-type", "accept", "user-agent"];
      // Upstream authenticates the keyed zen lane via Authorization: Bearer.
      // The gateway sends the key as x-api-key (zenAuthHeaders); map it.
      if (req.headers["x-api-key"]) headers["authorization"] = "Bearer " + req.headers["x-api-key"];
      else if (req.headers["authorization"]) headers["authorization"] = req.headers["authorization"];
      for (const name of allow) {
        const v = req.headers[name];
        if (v !== undefined) headers[name] = v;
      }
      const fwd = https.request({ host: ZEN_HOST, port: 443, path: req.url, method: req.method, headers }, (up) => {
        res.writeHead(up.statusCode, up.headers);
        up.pipe(res);
      });
      fwd.on("error", (e) => sendJson(res, 502, { error: { message: "zen upstream failed: " + String(e).slice(0, 120) } }));
      fwd.end(body);
      log("zen passthrough:", req.method, req.url, body.length + "b");
    });
    return;
  }
  if (req.method === "GET" && /^\/health$/.test(req.url || "")) {
    return sendJson(res, 200, { ok: true, worker: "oc-cli-worker.js" });
  }
  sendJson(res, 404, { error: { message: "not found" } });
});

// Exports for unit tests (CI): pure functions only — no listeners started.
module.exports = { jobFilePath, attachmentPath, buildPrompt, parseToolCall, parseToolCalls, buildToolsProtocol, normalizeModel, MODELS };

// Start the HTTP server only when run directly (not when required by tests).
if (require.main === module) {
  fs.mkdirSync(JOBS_DIR, { recursive: true });
  server.listen(PORT, HOST, () => {
    log("oc-cli-api listening on http://" + HOST + ":" + PORT + "/v1 (file-queue front-end, model " + MODEL_ID + ")");
  });
}
