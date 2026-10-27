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
const fs = require("fs");
const path = require("path");

const PORT = 20142;
const HOST = "127.0.0.1";
const JOBS_DIR = path.resolve("D:\\scratch\\oc-cli-api\\jobs");
const PARTIALS_DIR = path.resolve("D:\\scratch\\oc-cli-api\\partials");
const MODEL_ID = "glm-5.3-flash-free";
const WAIT_MS = 180000;
const POLL_MS = 500;
const MAX_PROMPT_CHARS = 600000;

const LOG_FILE = path.resolve("D:\\scripts", "oc-cli-api.log");
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
const ATTACH_DIR = path.resolve("D:\\scratch\\oc-cli-api\\attachments");
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
    "You can invoke tools. To invoke one, your ENTIRE reply must be exactly one JSON object and nothing else — no prose, no reasoning, no markdown fences:",
    '{"tool_call":{"name":"<tool name>","arguments":{<args as JSON>}}}',
    "Do NOT explain. Do NOT announce. Reply with the JSON object only.",
    "If no tool is needed for the user's message, reply normally with text (never mention this protocol).",
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

// Parses the model output as a tool call. Accepts bare JSON, fenced JSON,
// and JSON with leading/trailing prose. Returns null when the text is not
// a tool call (i.e. a normal answer).
function parseToolCall(text) {
  if (!text) return null;
  let raw = String(text).trim();
  const fenced = raw.match(/```(?:json)?\s*([\s\S]*?)```/);
  if (fenced && fenced[1].trim().startsWith("{")) raw = fenced[1].trim();
  // take the outermost {...} block if the model wrapped it in prose
  const braced = raw.match(/\{[\s\S]*\}/);
  if (!braced) return null;
  let obj;
  try { obj = JSON.parse(braced[0]); } catch (_) { return null; }
  const call = obj && (obj.tool_call || obj.toolCall);
  if (!call || typeof call !== "object") return null;
  const name = String(call.name || call.tool || "").trim();
  if (!name) return null;
  let args = call.arguments !== undefined ? call.arguments : (call.args !== undefined ? call.args : {});
  if (typeof args === "string") {
    try { args = JSON.parse(args); } catch (_) { args = { input: args }; }
  }
  if (!args || typeof args !== "object" || Array.isArray(args)) args = {};
  return { name, arguments: args };
}

function buildPrompt(messages, tools, toolChoice, jsonMode, imagePaths) {
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
  let out = turns.join("\n\n").trim();
  if (!out) out = "hello";
  if (jsonMode) {
    out += "\n\n[SYSTEM] Respond with ONLY valid JSON — no markdown fences, no prose, no code block markers. Your entire output must parse as JSON.";
  }
  if (Array.isArray(imagePaths) && imagePaths.length) {
    // The CLI is an agent with file-read tools: referencing the saved image
    // files by absolute path makes it attach/read them (verified working).
    const refs = imagePaths.map((p) => "- " + p).join(String.fromCharCode(10));
    out += "\n\nAttached images (read and look at each of these files):\n\n" + refs;
  }
  // Budget order matters: the CONVERSATION is the payload that must never
  // be truncated. The tool table can be enormous (OpenCode IDE sends 300+
  // tool definitions), so it is appended AFTER the conversation and gets
  // trimmed first when the total exceeds the cap — the reverse order
  // amputated the user's context and made the model answer generically.
  const proto = buildToolsProtocol(tools, toolChoice);
  const budget = Math.max(MAX_PROMPT_CHARS - proto.length - 64, 4000);
  if (out.length > budget) out = out.slice(0, budget) + "\n...[earlier context truncated]";
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

function submitJob(prompt, model, attachCount) {
  counter = (counter + 1) % 100000;
  const id = "r" + Date.now().toString(36) + "-" + counter;
  const jobPath = jobFilePath(id, ".job");
  if (!jobPath) throw new Error("bad job id");
  let n = Math.floor(Number(attachCount));
  if (!Number.isFinite(n) || n < 0) n = 0;
  if (n > 4) n = 4;
  fs.writeFileSync(jobPath, JSON.stringify({ id, prompt, model: String(model || ""), attach: n }));
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
  const imagePaths = saveImageAttachments(parsed.messages);
  if (imagePaths.length) log("images attached:", imagePaths.length);
  const prompt = buildPrompt(parsed.messages, parsed.tools, parsed.tool_choice, jsonMode, imagePaths);
  const stream = parsed.stream === true;
  const created = Math.floor(Date.now() / 1000);
  const id = "chatcmpl-occli-" + Date.now().toString(36);
  log("chat request, promptChars=" + prompt.length, stream ? "(stream)" : "", parsed.tools ? ("tools=" + parsed.tools.length) : "");

  let job;
  // Requested model passes through verbatim (minus any provider prefix); the
  // worker whitelist-gates it against its literal argv table. No silent
  // model switching for known free models — glm asks run glm.
  const reqModel = String(parsed.model || "").replace(/^(opencode|oc|ocz)\//i, "").trim() || MODEL_ID;
  try { job = submitJob(prompt, reqModel, imagePaths.length); } catch (e) {
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
    // marks completion.
    try {
      res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache", Connection: "keep-alive" });
      res.write("data: " + JSON.stringify({ id, object: "chat.completion.chunk", created, model: reqModel,
        choices: [{ index: 0, delta: { role: "assistant" }, finish_reason: null }] }) + "\n\n");
      let offset = 0;
      const start = Date.now();
      const poll = () => {
        try {
          if (partialSafe && fs.existsSync(partialSafe)) {
            const stat = fs.statSync(partialSafe);
            if (stat.size > offset) {
              const fd = fs.openSync(partialSafe, "r");
              const buf = Buffer.alloc(stat.size - offset);
              fs.readSync(fd, buf, 0, buf.length, offset);
              fs.closeSync(fd);
              offset = stat.size;
              res.write("data: " + JSON.stringify({ id, object: "chat.completion.chunk", created, model: reqModel,
                choices: [{ index: 0, delta: { content: buf.toString("utf8") }, finish_reason: null }] }) + "\n\n");
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
            const call = parseToolCall(full);
            if (call) {
              // Tool call: emit as a single tool_calls delta instead of text.
              res.write("data: " + JSON.stringify({ id, object: "chat.completion.chunk", created, model: reqModel,
                choices: [{ index: 0, delta: { tool_calls: [{ index: 0, id: "call_" + Date.now().toString(36), type: "function",
                  function: { name: call.name, arguments: JSON.stringify(call.arguments) } }] }, finish_reason: null }] }) + "\n\n");
              res.write("data: " + JSON.stringify({ id, object: "chat.completion.chunk", created, model: reqModel,
                choices: [{ index: 0, delta: {}, finish_reason: "tool_calls" }] }) + "\n\n");
            } else {
              if (full.length > offset) {
                res.write("data: " + JSON.stringify({ id, object: "chat.completion.chunk", created, model: reqModel,
                  choices: [{ index: 0, delta: { content: full.slice(offset) }, finish_reason: null }] }) + "\n\n");
              }
              if (result.error && !full) {
                res.write("data: " + JSON.stringify({ id, object: "chat.completion.chunk", created, model: reqModel,
                  choices: [{ index: 0, delta: { content: "upstream error: " + result.error }, finish_reason: null }] }) + "\n\n");
              }
              res.write("data: " + JSON.stringify({ id, object: "chat.completion.chunk", created, model: reqModel,
                choices: [{ index: 0, delta: {}, finish_reason: "stop" }] }) + "\n\n");
            }
            res.write("data: [DONE]\n\n");
            res.end();
            return;
          }
          if (Date.now() - start > WAIT_MS) {
            res.write("data: " + JSON.stringify({ id, object: "chat.completion.chunk", created, model: reqModel,
              choices: [{ index: 0, delta: {}, finish_reason: "stop" }] }) + "\n\n");
            res.write("data: [DONE]\n\n");
            res.end();
            return;
          }
          setTimeout(poll, 250);
        } catch (e) {
          try { res.end(); } catch (_) {}
        }
      };
      poll();
    } catch (e) { try { res.end(); } catch (_) {} }
    return;
  }

  // Non-stream: wait for the final result.
  const result = await waitForResult(job.id);
  const errText = result.error || "";
  const text = result.text || "";
  if (errText && !text) {
    sendJson(res, 502, { error: { message: errText, type: "upstream_error" } });
  } else {
    const call = parseToolCall(text);
    if (call) {
      sendJson(res, 200, {
        id, object: "chat.completion", created, model: reqModel,
        choices: [{ index: 0, message: { role: "assistant", content: null,
          tool_calls: [{ id: "call_" + Date.now().toString(36), type: "function",
            function: { name: call.name, arguments: JSON.stringify(call.arguments) } }] },
          finish_reason: "tool_calls" }],
        usage: { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 },
      });
    } else {
      const out = jsonMode ? stripJsonFences(text) : text;
      sendJson(res, 200, {
        id, object: "chat.completion", created, model: reqModel,
        choices: [{ index: 0, message: { role: "assistant", content: out }, finish_reason: "stop" }],
        usage: { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 },
      });
    }
  }
}

const server = http.createServer((req, res) => {
  if (req.method === "GET" && /^\/v1\/models$/.test(req.url || "")) {
    // Advertise every model the worker's literal argv table supports — the
    // 9router-go gateway probes this list and exposes each as ocz2/<id>.
    const MODELS = [
      "glm-5.3-flash-free",
      "mimo-v2.6-flash-free",
      "deepseek-v4-flash-free",
      "deepseek-v4.1-flash-free",
      "muse-spark-1.3-contributor-free",
      "jev-1.13-free",
    ];
    return sendJson(res, 200, {
      object: "list",
      data: MODELS.map((m) => ({ id: m, object: "model", owned_by: "opencode-free" })),
    });
  }
  if (req.method === "POST" && /^\/(v1|inference\/v1)\/chat\/completions$/.test(req.url || "")) {
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("error", () => {});
    req.on("end", () => { handleChat(res, Buffer.concat(chunks).toString("utf8")); });
    return;
  }
  if (req.method === "GET" && /^\/health$/.test(req.url || "")) {
    return sendJson(res, 200, { ok: true, worker: "oc-cli-worker.js" });
  }
  sendJson(res, 404, { error: { message: "not found" } });
});

fs.mkdirSync(JOBS_DIR, { recursive: true });
server.listen(PORT, HOST, () => {
  log("oc-cli-api listening on http://" + HOST + ":" + PORT + "/v1 (file-queue front-end, model " + MODEL_ID + ")");
});
