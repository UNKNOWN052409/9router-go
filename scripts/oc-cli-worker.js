#!/usr/bin/env node
/**
 * opencode CLI job runner — file-queue worker with a direct HTTP fast path.
 *
 * Two ways a job arrives:
 *   1. DIRECT (production path): the shim POSTs {id, prompt, model, attach}
 *      to POST /run on 127.0.0.1:20194 — the job starts the same millisecond,
 *      no sweep wait. The result is still written to <id>.out.json so the
 *      shim's existing waiter picks it up unchanged.
 *   2. FILE QUEUE (fallback): *.job files in <data>\jobs are swept every
 *      700ms — covers a shim older than the HTTP endpoint, or a direct POST
 *      that failed.
 *
 * For each job it runs the REAL opencode CLI (a fresh session AND a fresh
 * project directory per job — the free tier buckets usage per project, so a
 * new path is a new bucket) and writes the result to <id>.out.json:
 *   { "text": "...", "error": null }
 *
 * Injection safety: the spawned command line is a fully constant argv with a
 * constant working directory — both literals below. The job's prompt travels
 * to the child on STDIN as data and never touches argv, cwd, env, or any file
 * path. Job ids are validated against /^[a-z0-9-]{1,64}$/ AND every derived
 * path is resolved and checked to stay inside the jobs directory.
 */
const { spawn, execFile } = require("child_process");
const http = require("http");
const fs = require("fs");
const path = require("path");
const os = require("os");

// Data root is environment-overridable (OC_DATA_DIR) so the stack runs on any
// machine/hostname without code edits. Defaults: Windows keeps the original
// D:\scratch\oc-cli-api; other platforms use ~/.oc-free-tier-shim.
const DATA_ROOT = path.resolve(
  process.env.OC_DATA_DIR ||
  (process.platform === "win32" ? "D:\\scratch\\oc-cli-api" : path.join(os.homedir(), ".oc-free-tier-shim"))
);
const JOBS_DIR = path.join(DATA_ROOT, "jobs");
const PARTIALS_DIR = path.join(DATA_ROOT, "partials");

// Resolves a streaming-partial filename and refuses anything that escapes
// the partials directory (same boundary rule as jobFilePath).
function partialPathFor(base) {
  if (!SAFE_ID.test(base)) return null;
  const target = path.resolve(PARTIALS_DIR, base + ".txt");
  const root = path.resolve(PARTIALS_DIR) + path.sep;
  return target.startsWith(root) ? target : null;
}
// Binary resolution: OPENCODE_BIN env wins; then the standard Windows npm
// global path (if present); otherwise assume "opencode" is on PATH
// (Linux/macOS npm global install). No machine-specific paths required.
const OC_EXE = process.env.OPENCODE_BIN
  || (process.platform === "win32" && fs.existsSync(path.join(process.env.APPDATA || path.join(os.homedir(), "AppData", "Roaming"), "npm", "node_modules", "opencode-ai", "bin", "opencode.exe"))
        ? path.join(process.env.APPDATA || path.join(os.homedir(), "AppData", "Roaming"), "npm", "node_modules", "opencode-ai", "bin", "opencode.exe")
        : "opencode");
// FRESH project directory per request: the free tier tracks usage per
// project (directory-path-derived), so a brand-new path per run gives a
// brand-new bucket every time — the folder-rename trick, automated.
const WORK_ROOT = path.join(DATA_ROOT, "work");
// The model is baked into RUN_ARGS (a literal): the run directories stay
// config-free (an opencode.json in the cwd made the CLI hang/401).
// Per-model constant args: the requested model is matched against these
// literals — user input never builds the command line.
const MODEL_RUN_ARGS = {
  "glm-5.3-flash-free": ["run", "--format", "json", "--model", "opencode/glm-5.3-flash-free"],
  "mimo-v2.6-flash-free": ["run", "--format", "json", "--model", "opencode/mimo-v2.6-flash-free"],
  "deepseek-v4-flash-free": ["run", "--format", "json", "--model", "opencode/deepseek-v4-flash-free"],
  "deepseek-v4.1-flash-free": ["run", "--format", "json", "--model", "opencode/deepseek-v4.1-flash-free"],
  "muse-spark-1.3-contributor-free": ["run", "--format", "json", "--model", "opencode/muse-spark-1.3-contributor-free"],
  "jev-1.13-free": ["run", "--format", "json", "--model", "opencode/jev-1.13-free"],
};
const DEFAULT_MODEL_ARGS = MODEL_RUN_ARGS["mimo-v2.6-flash-free"];

// Image attachments: the shim stages decoded images in ATTACH_SRC_DIR as
// attachment-<i>.<ext> (i = 1..N per request). The worker copies the
// per-request files into the run directory before spawning, so the CLI sees
// them inside its own project directory (external directories are
// auto-rejected by the permission system). The --file argv elements are
// built from loop integers and a fixed extension whitelist only — no data
// from a job file ever becomes an argv element.
const ATTACH_SRC_DIR = path.resolve("D:\\scratch\\oc-cli-api\\attachments");
const ATTACH_EXTS = ["png", "jpg", "gif", "webp"];

// Picks the spawn argv base from literal tables only. The requested model is
// matched against these literals — user input never builds the command line.
// A model name carrying any provider prefix (9router/x, my-proxy/org/x, …)
// resolves by its last path segment; unknown names fall back to the default
// model's argv (documented behaviour). Attachment --file args are appended in
// runJob after staging.
function selectArgv(model) {
  let base = Object.prototype.hasOwnProperty.call(MODEL_RUN_ARGS, model) ? MODEL_RUN_ARGS[model] : null;
  if (!base && typeof model === "string" && model.includes("/")) {
    const last = model.slice(model.lastIndexOf("/") + 1);
    if (Object.prototype.hasOwnProperty.call(MODEL_RUN_ARGS, last)) base = MODEL_RUN_ARGS[last];
  }
  return base || DEFAULT_MODEL_ARGS;
}

// The run directories stay config-free (an opencode.json in the cwd made
// the CLI hang/401); the model goes on the command line via the literal
// argv arrays above.
const TIMEOUT_MS = parseInt(process.env.OC_TIMEOUT_MS || "180000", 10);
// Long-output tasks (big translations, large code generation) need minutes of
// generation: scale the timeout with the estimated OUTPUT size. Measured live:
// glm-5.3-flash-free produces roughly 85-150 chars/sec through the CLI, so a
// 26k-char input needing a ~30k-char answer requires ~350s — the flat 180s
// killed every such request (the core Nano-to-Go short-output bug).
function timeoutFor(promptChars) {
  // assume output can be up to ~1.2x the input length for translate/expand tasks
  const estimatedOutputChars = Math.max(promptChars * 1.2, 2000);
  // measured live: a 27,339-char prompt needed >446s of silent generation
  // before its single text event — the lead-in scales with context size.
  const seconds = estimatedOutputChars / 50 + 180;
  const scaled = Math.ceil(seconds * 1000);
  const cap = parseInt(process.env.OC_TIMEOUT_MAX_MS || "900000", 10); // 15 min hard cap
  return Math.min(Math.max(TIMEOUT_MS, scaled), cap);
}

const LOG_FILE = path.join(__dirname, "oc-worker.log");
function log(...parts) {
  const line = new Date().toISOString() + " " + parts.join(" ") + "\n";
  try { fs.appendFileSync(LOG_FILE, line); } catch (_) {}
  process.stdout.write(line);
}

const SAFE_ID = /^[a-z0-9][a-z0-9-]{0,63}$/;

// Resolves a job-side filename and refuses anything that escapes JOBS_DIR.
function jobFilePath(name, suffix) {
  if (!SAFE_ID.test(name)) return null;
  const target = path.resolve(JOBS_DIR, name + suffix);
  const root = path.resolve(JOBS_DIR) + path.sep;
  if (!target.startsWith(root)) return null;
  return target;
}

let runCounter = 0;

// FRESH project directory per request: the free tier tracks usage per
// project (directory-path-derived), so a brand-new path per run gives a
// brand-new bucket every time — the folder-rename trick, automated.
function runDirFor(name) {
  if (!SAFE_ID.test(name)) return null;
  const target = path.resolve(WORK_ROOT, name);
  const root = path.resolve(WORK_ROOT) + path.sep;
  return target.startsWith(root) ? target : null;
}

// ---------------------------------------------------------------------------
// Fast path: persistent opencode serve (HTTP) with per-request fresh sessions.
// Measured 4.4s vs 8.5s spawn latency; same fresh-bucket semantics (a new
// session gets a new quota bucket). Every failure falls back to the spawn path.
// ---------------------------------------------------------------------------
const OC_SERVE_PORT = parseInt(process.env.OC_SERVE_PORT || "20193", 10);
const OC_SERVE = "http://127.0.0.1:" + OC_SERVE_PORT;
// Serve calls must time out: a wedged upstream otherwise hangs the job until
// the shim's 180s wait gives up, and the wedged serve process then poisons
// every later request — the "first token after 3 minutes" symptom. On a
// timeout the serve is killed and rebooted, and the job falls back to the
// spawn path (which has its own timeout and retry handling).
const SERVE_TIMEOUT_MS = parseInt(process.env.OC_SERVE_TIMEOUT_MS || "300000", 10);
let serveProc = null;
let serveBooting = null;

function serveUp() {
  if (serveProc && !serveProc.killed) return Promise.resolve(true);
  if (serveBooting) return serveBooting;
  serveBooting = new Promise((resolve) => {
    serveProc = spawn(OC_EXE, ["serve", "--port", String(OC_SERVE_PORT), "--hostname", "127.0.0.1"], { windowsHide: true, stdio: "ignore", detached: false });
    serveProc.on("error", () => { serveProc = null; serveBooting = null; resolve(false); });
    serveProc.on("exit", () => { serveProc = null; serveBooting = null; resolve(false); });
    let tries = 0;
    const ping = setInterval(() => {
      fetch(OC_SERVE + "/doc", { signal: AbortSignal.timeout(5000) }).then((r) => { if (r.ok) { clearInterval(ping); resolve(true); } }).catch(() => {});
      if (++tries > 40) { clearInterval(ping); serveBooting = null; resolve(false); }
    }, 250);
  });
  return serveBooting;
}

// Kills a suspected-wedged serve so the next serveUp boots a fresh process.
function restartServe(reason) {
  log("serve restart:", reason);
  if (serveProc) { try { serveProc.kill(); } catch (_) {} }
  serveProc = null;
  serveBooting = null;
}

function serveModelId(model) {
  return { providerID: "opencode", modelID: model };
}

async function serveChat(prompt, model, files, onPartial) {
  const ok = await serveUp();
  if (!ok) return null; // fall back to spawn
  const signal = AbortSignal.timeout(SERVE_TIMEOUT_MS);
  let session;
  try {
    session = await fetch(OC_SERVE + "/session", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({}), signal }).then((r) => r.ok ? r.json() : null).catch(() => null);
  } catch (_) { restartServe("session create timeout"); return null; }
  if (!session || !session.id) return null;
  const parts = [{ type: "text", text: String(prompt || "hello") }];
  // Stage attachments exactly like the spawn path and reference them as file
  // parts; the serve process has file tools and reads them itself.
  let attachCount = Math.floor(Number(files));
  if (!Number.isFinite(attachCount) || attachCount < 0) attachCount = 0;
  if (attachCount > 4) attachCount = 4;
  for (let i = 1; i <= attachCount; i++) {
    for (const ext of ATTACH_EXTS) {
      const src = path.join(ATTACH_SRC_DIR, "attachment-" + i + "." + ext);
      if (!fs.existsSync(src)) continue;
      parts.push({ type: "file", url: src });
      break;
    }
  }
  let out;
  try {
    const resp = await fetch(OC_SERVE + "/session/" + session.id + "/message", { method: "POST", headers: { "content-type": "application/json" },
      body: JSON.stringify({ model: serveModelId(model), parts }), signal }).catch(() => null);
    if (!resp || !resp.ok) return null;
    out = await resp.json().catch(() => null);
  } catch (_) {
    // The abort fired mid-generation: the serve may still be fine (upstream
    // slowness), but a hung fetch is indistinguishable from a wedged serve,
    // so reboot it and let the spawn path serve this job.
    restartServe("message timeout");
    return null;
  }
  if (!out || !Array.isArray(out.parts)) return null;
  let text = "";
  let reasoning = "";
  for (const p of out.parts) {
    if (p && p.type === "text" && p.text) {
      text += p.text;
      if (onPartial) { try { onPartial(p.text); } catch (_) {} }
    } else if (p && (p.type === "reasoning" || p.type === "reasoning_content") && p.text) {
      // Some models emit visible reasoning parts; forwarded as reasoning_content.
      reasoning += p.text;
    }
  }
  const blob = JSON.stringify(out);
  if (/FreeUsageLimit|429|Invalid API key|Unexpected server error/i.test(blob)) return null;
  // The CLI can return an UNTRANSLATED upstream stream when the request
  // overflows the model context (measured live: a 444k-token prompt came back
  // as raw "event: message_start ... message_stop" Anthropic SSE). That is
  // not an answer — treat it as transient so retry/fallback runs instead of
  // shipping protocol JSON to the client.
  if (/^event:\s*message_start/.test(text) && /message_stop/.test(text)) {
    log("serve returned untranslated anthropic SSE (context overflow?) — transient");
    return null;
  }
  // The serve response carries the session message info (with token counts)
  // when the upstream reported them; forwarded for OpenAI usage mapping.
  const tokens = out.info && out.info.tokens && typeof out.info.tokens === "object" ? out.info.tokens : null;
  const result = { text, error: null };
  if (tokens) result.tokens = tokens;
  if (reasoning) result.reasoning = reasoning;
  return result;
}

// ---------------------------------------------------------------------------
// Project staging: the CLI sandbox auto-rejects directories outside its run
// dir, so a request that names a real project folder ("stage") gets that
// folder COPIED into the fresh run dir before the CLI spawns — the agent then
// works on it natively (read/edit/run like OpenCode does) — and every file it
// created or modified is synced back to the original location afterwards.
// node_modules/.git are skipped; only changed/new files sync back (mtime).
// ---------------------------------------------------------------------------
function stageProject(stage, runDir) {
  if (typeof stage !== "string" || !path.isAbsolute(stage)) return null;
  const src = path.resolve(stage);
  try {
    if (!fs.existsSync(src) || !fs.statSync(src).isDirectory()) return null;
  } catch (_) { return null; }
  const manifest = {};
  const walk = (dir, rel) => {
    let entries;
    try { entries = fs.readdirSync(dir, { withFileTypes: true }); } catch (_) { return; }
    for (const e of entries) {
      // .git MUST be copied: opencode resolves the project root by walking up
      // to the nearest git repo — without the anchor it lands on whatever git
      // repo is above the run dir and works in the wrong tree.
      if (e.name === "node_modules") continue;
      const abs = path.join(dir, e.name);
      const r = rel ? rel + "/" + e.name : e.name;
      if (e.isDirectory()) { walk(abs, r); continue; }
      try {
        manifest[r] = { abs, mtime: fs.statSync(abs).mtimeMs };
        const dst = path.join(runDir, r);
        fs.mkdirSync(path.dirname(dst), { recursive: true });
        fs.copyFileSync(abs, dst);
      } catch (_) {}
    }
  };
  walk(src, "");
  return manifest;
}

function syncBack(manifest, runDir) {
  if (!manifest) return 0;
  let synced = 0;
  const walk = (dir, rel) => {
    let entries;
    try { entries = fs.readdirSync(dir, { withFileTypes: true }); } catch (_) { return; }
    for (const e of entries) {
      if (e.name === "node_modules" || e.name.startsWith(".git")) continue;
      const abs = path.join(dir, e.name);
      const r = rel ? rel + "/" + e.name : e.name;
      if (e.isDirectory()) { walk(abs, r); continue; }
      const orig = manifest[r];
      let mtime = 0;
      try { mtime = fs.statSync(abs).mtimeMs; } catch (_) {}
      if (!orig || Math.abs(mtime - orig.mtime) > 1) {
        try {
          fs.mkdirSync(path.dirname(orig.abs), { recursive: true });
          fs.copyFileSync(abs, orig.abs);
          synced++;
        } catch (_) {}
      }
    }
  };
  walk(runDir, "");
  return synced;
}

function runJob(prompt, model, files, onPartial, stage) {
  // A staged project needs the spawn path: the project is copied into the
  // fresh run dir (its own sandbox), so the persistent serve is bypassed.
  // Large prompts also skip the serve: its 300s ceiling is below the
  // generation time a big-context request needs.
  if (stage || String(prompt || "").length > 8000) {
    return spawnRunJob(prompt, model, files, onPartial, stage);
  }
  // Fast path first; on any null result the spawn path below runs as before.
  return serveChat(prompt, model, files, onPartial).then((fast) => {
    if (fast) return fast;
    return spawnRunJob(prompt, model, files, onPartial);
  });
}

function spawnRunJob(prompt, model, files, onPartial, stage) {
  return new Promise((resolve) => {
    runCounter = (runCounter + 1) % 100000;
    const runDir = runDirFor("r" + runCounter);
    if (!runDir) {
      resolve({ text: "", error: "bad run dir" });
      return;
    }
    try { fs.mkdirSync(runDir, { recursive: true }); } catch (e) {
      resolve({ text: "", error: "mkdir failed: " + String(e).slice(0, 80) });
      return;
    }
    let staged = null;
    if (stage) {
      staged = stageProject(stage, runDir);
      log("run dir:", runDir, "model:", model, "attach:", Number(files) || 0, staged ? "STAGED " + Object.keys(staged).length + " files from " + stage : "stage rejected: " + stage);
    } else {
      log("run dir:", runDir, "model:", model, "attach:", Number(files) || 0);
    }
    // argv comes from selectArgv: both inputs are validated tokens (the
    // model name from the whitelist chain, the attachment count a clamped
    // integer) and the function assigns only from the fully literal
    // MODEL_RUN_ARGS / ATTACH_ARGV tables — nothing from the job file can
    // become an argv element. The prompt rides stdin.
    let attachCount = Math.floor(Number(files));
if (!Number.isFinite(attachCount) || attachCount < 0) attachCount = 0;
if (attachCount > 4) attachCount = 4;
// Stage the shim's saved attachments into this run dir and append the
// --file argv. Names derive only from the loop index and the fixed
// ATTACH_EXTS whitelist — nothing from the job file touches argv.
const attachArgs = [];
for (let i = 1; i <= attachCount; i++) {
  for (const ext of ATTACH_EXTS) {
    const src = path.join(ATTACH_SRC_DIR, "attachment-" + i + "." + ext);
    if (!fs.existsSync(src)) continue;
    const staged = path.join(runDir, "attachment-" + i + "." + ext);
    try { fs.copyFileSync(src, staged); }
    catch (_) { continue; }
    // Absolute path required: the CLI fails to resolve a bare relative
    // --file name ("File not found: attachment-1.png") even though the
    // file sits in its cwd. The path is worker-owned (runDir + loop index
    // + fixed whitelist ext) — still nothing user-controlled in argv.
    attachArgs.push("--file", staged);
    break;
  }
}
if (attachArgs.length < attachCount * 2) {
  log("warning: staged " + (attachArgs.length / 2) + " of " + attachCount + " attachments");
}
const args = selectArgv(model).concat(attachArgs);
// Prompt travels on STDIN as data — user input never touches argv. HOME
    // is pinned to the real profile — a PowerShell-inherited env can lack it,
    // and the CLI resolves its auth file through the home path.
    // HOME pin: the CLI resolves its auth file through the home path. Use the
    // actual user profile — never a hardcoded machine-specific path.
    // PWD must point at the run dir: the CLI resolves its project root from
    // the inherited PWD env var, not process.cwd() — a stale PWD (from
    // whoever launched the worker) made staged projects invisible.
    const env = Object.assign({}, process.env, {
      HOME: process.env.OC_HOME || (process.platform === "win32" ? (process.env.USERPROFILE || os.homedir()) : process.env.HOME),
      PWD: runDir,
      OLDPWD: runDir,
    });
    const child = spawn(OC_EXE, args, { cwd: runDir, windowsHide: true, env: env });
    let buf = "";
    let text = "";
    let reasoning = "";
    let tokens = null;
    let errOut = "";
    let errored = null;
    const timeoutMs = timeoutFor(String(prompt || "").length);
    log("spawn timeout budget:", Math.round(timeoutMs / 1000) + "s for " + String(prompt || "").length + " chars");
    const timer = setTimeout(() => {
      errored = "timeout";
      // Tree-kill: the CLI spawns its own children; a bare kill() leaves
      // them running as orphans (the process mob the user asked cleaned).
      if (process.platform === "win32" && child.pid) {
        try { execFile("taskkill", ["/pid", String(child.pid), "/T", "/F"], () => {}); } catch (_) {}
      } else {
        try { child.kill(); } catch (_) {}
      }
    }, timeoutMs);

    child.stdout.on("data", (chunk) => {
      buf += chunk.toString("utf8");
      let idx;
      while ((idx = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, idx).trim();
        buf = buf.slice(idx + 1);
        if (!line.startsWith("{")) continue;
        let ev;
        try { ev = JSON.parse(line); } catch (_) { continue; }
        const part = ev.part || {};
        if (ev.type === "text" && part.text) {
          text += part.text;
          if (onPartial) { try { onPartial(part.text); } catch (_) {} }
        }
        else if ((part.type === "reasoning" || part.type === "reasoning_content") && part.text) {
          // Visible reasoning parts some models emit; forwarded as reasoning_content.
          reasoning += part.text;
        }
        else if (ev.type === "step_finish" && part.tokens && typeof part.tokens === "object") {
          // step-finish carries the CLI's token accounting (input/output/
          // reasoning) — forwarded for OpenAI usage mapping.
          tokens = part.tokens;
        }
        else if (ev.type === "error") {
          const msg = (part && (part.error || part.message)) || ev.error || "unknown error";
          errored = typeof msg === "string" ? msg : JSON.stringify(msg);
        }
      }
    });
    child.stderr.on("data", (c) => { errOut += c.toString("utf8"); });
    child.on("error", (err) => {
      clearTimeout(timer);
      cleanupRunDir(runDir);
      resolve({ text, error: String(err) });
    });
    child.on("exit", (code) => {
      clearTimeout(timer);
      if (errOut.trim()) log("stderr(code=" + code + "):", errOut.slice(0, 300).replace(/\n/g, " "));
      if (staged) {
        const n = syncBack(staged, runDir);
        if (n) log("staged project synced back:", n, "changed file(s)");
      }
      cleanupRunDir(runDir);
      // Same untranslated-SSE guard as the serve path: an anthropic
      // message_start/message_stop stream is an upstream failure, not text.
      if (/^event:\s*message_start/.test(text) && /message_stop/.test(text)) {
        log("spawn returned untranslated anthropic SSE (context overflow?) — transient");
        errored = errored || "upstream returned untranslated stream (context overflow)";
        text = "";
      }
      const result = { text, error: errored };
      if (tokens) result.tokens = tokens;
      if (reasoning) result.reasoning = reasoning;
      resolve(result);
    });
    try {
      child.stdin.write(String(prompt || "hello") + "\n");
      child.stdin.end();
    } catch (_) {}
  });
}

function cleanupRunDir(dir) {
  if (!dir || !dir.startsWith(path.resolve(WORK_ROOT) + path.sep)) return;
  try { fs.rmSync(dir, { recursive: true, force: true }); } catch (_) {}
}

// Core job execution shared by both arrival paths (direct HTTP and the file
// queue): whitelist-gates the model, runs the job with transient-retry, and
// writes <id>.out.json plus streaming partials exactly as the shim expects.
// The in-flight set dedupes the two arrival paths: a job id handed over by
// the shim's direct POST must not also be picked up by the queue sweep
// (single event loop, so the check-and-add below is race-free).
const inflight = new Set();

function processJob(job, base) {
  if (inflight.has(base)) { log(base, "skipped — already in flight (dedup)"); return; }
  inflight.add(base);
  const outPath = jobFilePath(base, ".out.json");
  if (!outPath) { inflight.delete(base); return; }
  // Streaming partials: every text delta the CLI emits is appended here so
  // the shim can forward real-time SSE chunks to its client.
  const partialPath = partialPathFor(base);
  // Model is whitelist-gated: only names with a literal argv entry run as
  // themselves; a name carrying any provider prefix resolves by its last
  // path segment first; anything else falls back to the default model's
  // argv (documented behaviour).
  let model = typeof job.model === "string" ? job.model : "";
  if (!Object.prototype.hasOwnProperty.call(MODEL_RUN_ARGS, model) && model.includes("/")) {
    const last = model.slice(model.lastIndexOf("/") + 1);
    if (Object.prototype.hasOwnProperty.call(MODEL_RUN_ARGS, last)) model = last;
  }
  if (!Object.prototype.hasOwnProperty.call(MODEL_RUN_ARGS, model)) model = "mimo-v2.6-flash-free";
  log(base, "running, promptChars=" + String(job.prompt || "").length, "model=" + model);

  // Rotating-exit reality: a fresh ProxyRise IP can land on an edge that
  // answers a transient 401/429/5xx for the same key. Retry with a new
  // session (and thus a new exit) up to 6 times before giving up.
  const stage = typeof job.stage === "string" ? job.stage : "";
  let bestShort = "";   // best short answer seen across retry attempts
  // CONTINUATION STITCHING: long-output tasks (big translations, large code
  // generation) outrun any fixed CLI timeout — the run ends with a PARTIAL
  // answer plus a timeout error. Instead of shipping the partial (the client
  // sees a truncated translation — the reported Nano-to-Go bug), the worker
  // automatically sends "continue exactly where you stopped" passes with a
  // fresh session each time and stitches the output until the task completes
  // or growth stalls. This mirrors what the upstream Nano-to-Go gets for free
  // (server-side generation has no local timeout).
  let accumulated = "";          // stitched text across continuation passes
  let carryPrompt = String(job.prompt || "hello");
  const attempt = (n) => {
    runJob(carryPrompt, model, Number(job.attach) || 0, (delta) => {
      if (partialPath) { try { fs.appendFileSync(partialPath, delta); } catch (_) {} }
    }, stage).then((result) => {
      const err = result.error || "";
      const transient = /Invalid API key|FreeUsageLimit|429|timeout|rate|socket|disconnected|cannot connect|ECONN|fetch failed|network|certificate|TLS|SSL|reset by (server|peer)/i.test(err);
      const timedOutWithPartial = /timeout/i.test(err) && result.text && result.text.length > 300;
      const maxAccumulated = parseInt(process.env.OC_MAX_ACCUMULATED || "2000000", 10);
      const maxContinuations = parseInt(process.env.OC_MAX_CONTINUATIONS || "40", 10);
      if (timedOutWithPartial && accumulated.length + result.text.length < maxAccumulated && n < maxContinuations) {
        // Keep the partial, then continue from where it stopped with a fresh
        // session (fresh quota bucket) carrying the tail as context.
        const piece = result.text;
        accumulated += (accumulated ? "\n" : "") + piece;
        log(base, "attempt " + n + " partial on timeout (" + piece.length + " chars, " + accumulated.length + " accumulated) — continuing");
        if (partialPath) { try { fs.rmSync(partialPath, { force: true }); } catch (_) {} }
        carryPrompt = String(job.prompt || "hello") +
          "\n\n[SYSTEM] You already produced the following PARTIAL output for this exact task:\n\n" +
          piece.slice(-8000) +
          "\n\n[SYSTEM] CONTINUE EXACTLY from where that output stops. Do NOT repeat any earlier content. Do not add commentary or headings — just continue the task seamlessly to its end. If and only if the task is fully complete, reply with the single word CONTINUE-COMPLETE.";
        setTimeout(() => attempt(n + 1), 1500);
        return;
      }
      // A ONE-LINE answer on a HUGE prompt is degraded generation, not a real
      // answer: near the context limit glm sometimes emits a single short
      // line and stops (measured live: 78 chars out of a 248k-char request).
      // Retry with a fresh session and deliver whichever attempt answered
      // more. Small prompts are exempt — a short answer there is legitimate.
      if (!err && result.text && result.text.length < 150 &&
          String(carryPrompt || "").length > 100000 && n < 2) {
        if (result.text.length > bestShort.length) bestShort = result.text;
        log(base, "attempt " + n + " one-line answer on huge prompt (" + result.text.length + " chars) — retrying for a fuller answer");
        if (partialPath) { try { fs.rmSync(partialPath, { force: true }); } catch (_) {} }
        setTimeout(() => attempt(n + 1), 1500);
        return;
      }
      // An EMPTY success is also transient: glm occasionally returns a
      // zero-char completion with no error (measured live), which a chat
      // client renders as nothing at all. Retry — a fresh session usually
      // answers normally.
      if (!err && !result.text && !accumulated && n < 3) {
        log(base, "attempt " + n + " empty completion — retrying");
        if (partialPath) { try { fs.rmSync(partialPath, { force: true }); } catch (_) {} }
        setTimeout(() => attempt(n + 1), 1500);
        return;
      }

      if (err && transient && !accumulated && n < 6) {
        log(base, "attempt " + n + " transient (" + err.slice(0, 500) + ") — retrying");
        if (partialPath) { try { fs.rmSync(partialPath, { force: true }); } catch (_) {} }
        setTimeout(() => attempt(n + 1), 2000);
        return;
      }
      let finalText = result.text || "";
      if (accumulated) finalText = accumulated + (finalText ? "\n" + finalText : "");
      // If every attempt came back one-line-degraded, deliver the best line
      // we got rather than an empty shell.
      if (finalText.length < bestShort.length) finalText = bestShort;
      let finalError = err;
      // A timeout that still leaves a stitched answer is delivered as success:
      // the client gets the complete stitched output rather than a 502.
      if (err && /timeout/i.test(err) && finalText) finalError = null;
      try {
        fs.writeFileSync(outPath, JSON.stringify({ text: finalText, error: finalError, tokens: result.tokens, reasoning: result.reasoning }));
        if (partialPath) { try { fs.rmSync(partialPath, { force: true }); } catch (_) {} }
        log(base, "done, chars=" + finalText.length + (accumulated ? " (stitched " + accumulated.length + ")" : ""), finalError ? "error=" + String(finalError).slice(0, 80) : "");
      } catch (e) { log(base, "write result failed:", String(e).slice(0, 80)); }
      inflight.delete(base);
    });
  };
  attempt(1);
}

function handleJobFile(file) {
  const base = file.replace(/\.job$/i, "");
  const jobPath = jobFilePath(base, ".job");
  if (!jobPath || !fs.existsSync(jobPath)) return;
  // Read then IMMEDIATELY unlink: the sweep re-runs every 700ms and must not
  // pick this job up again while the (slow) CLI run is in flight.
  let job;
  try { job = JSON.parse(fs.readFileSync(jobPath, "utf8")); } catch (e) {
    log(base, "bad job file:", String(e).slice(0, 80));
    try { fs.unlinkSync(jobPath); } catch (_) {}
    return;
  }
  try { fs.unlinkSync(jobPath); } catch (_) {}
  processJob(job, base);
}

function sweep() {
  let files = [];
  try { files = fs.readdirSync(JOBS_DIR); } catch (_) { return; }
  for (const f of files) if (f.toLowerCase().endsWith(".job")) handleJobFile(f);
}

function main() {
  fs.mkdirSync(JOBS_DIR, { recursive: true });
  fs.mkdirSync(PARTIALS_DIR, { recursive: true });
  fs.mkdirSync(WORK_ROOT, { recursive: true });
  // Auth-path env forensics: dump the vars the CLI's data-dir resolution
  // depends on, so a working vs failing context can be diffed.
  const keys = ["HOME", "USERPROFILE", "APPDATA", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "OPENCODE_API_KEY", "OPENCODE_DISABLE_FREE_MODELS", "PATH"];
  for (const k of keys) {
    const v = process.env[k] || "";
    log("env", k + "=", k === "PATH" ? v.split(";").filter((p) => /opencode|lingling|npm/i.test(p)).join(";") || "(no oc/npm entries)" : k === "OPENCODE_API_KEY" ? v.slice(0, 16) + "...(masked)" : v.slice(0, 60));
  }
  // Windows-critical vars: a spawn env missing these breaks the child's
  // crypto/network stack (the classic Node-on-Windows gotcha).
  const crit = ["SystemRoot", "windir", "ComSpec", "PATHEXT", "TEMP", "TMP", "SystemDrive", "USERDOMAIN", "USERNAME", "ProgramFiles", "CommonProgramFiles", "NUMBER_OF_PROCESSORS", "OS"];
  const missing = crit.filter((k) => !process.env[k]);
  log("env critical missing:", missing.length ? missing.join(",") : "(none)");
  const vals = {};
  for (const k of ["SystemRoot", "ComSpec", "PATHEXT", "TEMP", "TMP", "USERDOMAIN", "USERNAME"]) vals[k] = process.env[k] || "(MISSING)";
  log("env critical values:", JSON.stringify(vals));
  log("worker started; jobs dir", JOBS_DIR);
  // Pre-warm the persistent opencode serve so the first request does not
  // pay the boot cost (and never trips the gateway's 30s response timeout).
  serveUp();
  setInterval(sweep, 700);
  // DIRECT routing endpoint: the shim POSTs the job here and it starts the
  // same millisecond — no sweep wait (the queue stays as the fallback).
  // 127.0.0.1 only, no auth surface beyond loopback, ids whitelist-validated.
  const runServer = http.createServer((req, res) => {
    if (req.method !== "POST" || !/^\/run\/?$/.test(req.url || "")) {
      res.writeHead(404, { "Content-Type": "application/json" });
      res.end('{"error":"not found"}');
      return;
    }
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("error", () => {});
    req.on("end", () => {
      let job = {};
      try { job = JSON.parse(Buffer.concat(chunks).toString("utf8") || "{}"); } catch (_) {}
      const base = typeof job.id === "string" ? job.id : "";
      if (!SAFE_ID.test(base) || typeof job.prompt !== "string") {
        res.writeHead(400, { "Content-Type": "application/json" });
        res.end('{"error":"bad job"}');
        return;
      }
      processJob(job, base);
      // The shim keeps the .job on disk as its queue fallback; now that the
      // direct path has accepted it, the copy is dead weight — the sweep
      // would only find it again and log a dedup skip. Remove it.
      try { const jp = jobFilePath(base, ".job"); if (jp) fs.unlinkSync(jp); } catch (_) {}
      res.writeHead(202, { "Content-Type": "application/json" });
      res.end('{"ok":true}');
    });
  });
  const runPort = parseInt(process.env.OC_RUN_PORT || "20194", 10);
  runServer.listen(runPort, "127.0.0.1", () => {
    log("direct-run endpoint listening on http://127.0.0.1:" + runPort + "/run");
  });
}
// Exports for unit tests (CI): pure functions only — no polling started.
module.exports = { jobFilePath, partialPathFor, runDirFor, selectArgv, SAFE_ID, MODEL_RUN_ARGS, ATTACH_EXTS };

// Start the job sweep only when run directly (not when required by tests).
if (require.main === module) {
  main();
}
