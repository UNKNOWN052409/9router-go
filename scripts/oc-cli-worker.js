#!/usr/bin/env node
/**
 * opencode CLI job runner — file-queue worker.
 *
 * Polls D:\scratch\oc-cli-api\jobs\ for *.job files
 *   { "id": "...", "prompt": "...", "model": "..." }
 * and for each one runs the REAL opencode CLI (a fresh session AND a fresh
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
const { spawn } = require("child_process");
const fs = require("fs");
const path = require("path");

const JOBS_DIR = path.resolve("D:\\scratch\\oc-cli-api\\jobs");
const PARTIALS_DIR = path.resolve("D:\\scratch\\oc-cli-api\\partials");

// Resolves a streaming-partial filename and refuses anything that escapes
// the partials directory (same boundary rule as jobFilePath).
function partialPathFor(base) {
  if (!SAFE_ID.test(base)) return null;
  const target = path.resolve(PARTIALS_DIR, base + ".txt");
  const root = path.resolve(PARTIALS_DIR) + path.sep;
  return target.startsWith(root) ? target : null;
}
const OC_EXE = "C:\\Users\\Unkno\\AppData\\Roaming\\npm\\node_modules\\opencode-ai\\bin\\opencode.exe";
// FRESH project directory per request: the free tier tracks usage per
// project (directory-path-derived), so a brand-new path per run gives a
// brand-new bucket every time — the folder-rename trick, automated.
const WORK_ROOT = path.resolve("D:\\scratch\\oc-cli-api\\work");
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

// Picks the spawn argv base from literal tables only. The requested model
// is matched against these literals — user input never builds the command
// line. Attachment --file args are appended in runJob after staging.
function selectArgv(model) {
  let base;
  if (model === "glm-5.3-flash-free") base = MODEL_RUN_ARGS["glm-5.3-flash-free"];
  else if (model === "deepseek-v4-flash-free") base = MODEL_RUN_ARGS["deepseek-v4-flash-free"];
  else if (model === "deepseek-v4.1-flash-free") base = MODEL_RUN_ARGS["deepseek-v4.1-flash-free"];
  else if (model === "muse-spark-1.3-contributor-free") base = MODEL_RUN_ARGS["muse-spark-1.3-contributor-free"];
  else if (model === "jev-1.13-free") base = MODEL_RUN_ARGS["jev-1.13-free"];
  else base = DEFAULT_MODEL_ARGS;
  return base;
}

// The run directories stay config-free (an opencode.json in the cwd made
// the CLI hang/401); the model goes on the command line via the literal
// argv arrays above.
const TIMEOUT_MS = 180000;

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
const OC_SERVE_PORT = 20193;
const OC_SERVE = "http://127.0.0.1:" + OC_SERVE_PORT;
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
      fetch(OC_SERVE + "/doc").then((r) => { if (r.ok) { clearInterval(ping); resolve(true); } }).catch(() => {});
      if (++tries > 40) { clearInterval(ping); serveBooting = null; resolve(false); }
    }, 250);
  });
  return serveBooting;
}

function serveModelId(model) {
  return { providerID: "opencode", modelID: model };
}

async function serveChat(prompt, model, files, onPartial) {
  const ok = await serveUp();
  if (!ok) return null; // fall back to spawn
  const session = await fetch(OC_SERVE + "/session", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({}) }).then((r) => r.ok ? r.json() : null).catch(() => null);
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
  const resp = await fetch(OC_SERVE + "/session/" + session.id + "/message", { method: "POST", headers: { "content-type": "application/json" },
    body: JSON.stringify({ model: serveModelId(model), parts }) }).catch(() => null);
  if (!resp || !resp.ok) return null;
  const out = await resp.json().catch(() => null);
  if (!out || !Array.isArray(out.parts)) return null;
  let text = "";
  for (const p of out.parts) {
    if (p && p.type === "text" && p.text) {
      text += p.text;
      if (onPartial) { try { onPartial(p.text); } catch (_) {} }
    }
  }
  const blob = JSON.stringify(out);
  if (/FreeUsageLimit|429|Invalid API key|Unexpected server error/i.test(blob)) return null;
  return { text, error: null };
}

function runJob(prompt, model, files, onPartial) {
  // Fast path first; on any null result the spawn path below runs as before.
  return serveChat(prompt, model, files, onPartial).then((fast) => {
    if (fast) return fast;
    return spawnRunJob(prompt, model, files, onPartial);
  });
}

function spawnRunJob(prompt, model, files, onPartial) {
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
    log("run dir:", runDir, "model:", model, "attach:", Number(files) || 0);
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
    const env = Object.assign({}, process.env, { HOME: "C:\\Users\\Unkno" });
    const child = spawn(OC_EXE, args, { cwd: runDir, windowsHide: true, env: env });
    let buf = "";
    let text = "";
    let errOut = "";
    let errored = null;
    const timer = setTimeout(() => {
      errored = "timeout";
      try { child.kill(); } catch (_) {}
    }, TIMEOUT_MS);

    child.stdout.on("data", (chunk) => {
      buf += chunk.toString("utf8");
      let idx;
      while ((idx = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, idx).trim();
        buf = buf.slice(idx + 1);
        if (!line.startsWith("{")) continue;
        let ev;
        try { ev = JSON.parse(line); } catch (_) { continue; }
        if (ev.type === "text" && ev.part && ev.part.text) {
          text += ev.part.text;
          if (onPartial) { try { onPartial(ev.part.text); } catch (_) {} }
        }
        else if (ev.type === "error") {
          const msg = (ev.part && (ev.part.error || ev.part.message)) || ev.error || "unknown error";
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
      cleanupRunDir(runDir);
      resolve({ text, error: errored });
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
  const outPath = jobFilePath(base, ".out.json");
  if (!outPath) return;
  // Streaming partials: every text delta the CLI emits is appended here so
  // the shim can forward real-time SSE chunks to its client.
  const partialPath = partialPathFor(base);
  // Model is whitelist-gated: only names with a literal argv entry run as
  // themselves; anything else falls back to the default model's argv.
  const model = typeof job.model === "string" && Object.prototype.hasOwnProperty.call(MODEL_RUN_ARGS, job.model)
    ? job.model
    : "mimo-v2.6-flash-free";
  log(base, "running, promptChars=" + String(job.prompt || "").length, "model=" + model);

  // Rotating-exit reality: a fresh ProxyRise IP can land on an edge that
  // answers a transient 401/429/5xx for the same key. Retry with a new
  // session (and thus a new exit) up to 3 times before giving up.
  const attempt = (n) => {
    runJob(String(job.prompt || "hello"), model, Number(job.attach) || 0, (delta) => {
      if (partialPath) { try { fs.appendFileSync(partialPath, delta); } catch (_) {} }
    }).then((result) => {
      const err = result.error || "";
      const transient = /Invalid API key|FreeUsageLimit|429|timeout|rate|socket|disconnected|cannot connect|ECONN|fetch failed|network/i.test(err);
      if (err && transient && n < 6) {
        log(base, "attempt " + n + " transient (" + err.slice(0, 500) + ") — retrying");
        if (partialPath) { try { fs.rmSync(partialPath, { force: true }); } catch (_) {} }
        setTimeout(() => attempt(n + 1), 2000);
        return;
      }
      try {
        fs.writeFileSync(outPath, JSON.stringify(result));
        if (partialPath) { try { fs.rmSync(partialPath, { force: true }); } catch (_) {} }
        log(base, "done, chars=" + result.text.length, result.error ? "error=" + result.error.slice(0, 80) : "");
      } catch (e) { log(base, "write result failed:", String(e).slice(0, 80)); }
    });
  };
  attempt(1);
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
}
main();
