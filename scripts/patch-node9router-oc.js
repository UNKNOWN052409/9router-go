// Patches the node 9router's compiled provider registry so the BUILT-IN
// no-auth "opencode" provider (dashboard: providers/opencode, alias oc/)
// points its free lane at the local limit-reset shim instead of
// opencode.ai (whose keyless gate 429s every synthesized request).
//
// The request URL is built as `${config.baseUrl}/inference/v1/chat/completions`
// (buildUrl in chunk 5330), so baseUrl = http://127.0.0.1:20142 lands exactly
// on the shim's /inference/v1/chat/completions route.
//
// Scope: ONLY the opencode provider's transport.baseUrl. The identical string
// inside other providers (zen/go URLs, systemoneConfig) is untouched.
const fs = require("fs");
const path = require("path");
const os = require("os");

// Target path is derived from the real user profile — works on any machine
// with an npm global install of 9router; override with N9R_HOME if needed.
const N9R_ROOT = process.env.N9R_HOME
  || (process.platform === "win32"
        ? path.join(process.env.APPDATA || path.join(os.homedir(), "AppData", "Roaming"), "npm", "node_modules", "9router")
        : path.join(os.homedir(), ".npm-global", "lib", "node_modules", "9router"));
const target = path.join(N9R_ROOT, "app", ".next-cli-build", "server", "chunks", "1892.js");
const backup = path.join(os.tmpdir(), "chunk-1892-backup.js");

if (!fs.existsSync(backup)) {
  fs.copyFileSync(target, backup);
  console.log("backup written:", backup);
} else {
  console.log("backup already exists, not overwriting");
}

const t = fs.readFileSync(target, "utf8");
const needle = 'id:"opencode",priority:40,hasFree:!0,alias:"oc",uiAlias:"oc",display:{name:"OpenCode Free",icon:"terminal",color:"#E87040",textIcon:"OC"},category:"free",noAuth:!0,transport:{baseUrl:"https://opencode.ai",';
const i = t.indexOf(needle);
if (i < 0) {
  console.error("needle not found — build layout changed? aborting without writes");
  process.exit(1);
}
if (t.includes('id:"opencode",priority:40,hasFree:!0,alias:"oc",uiAlias:"oc",display:{name:"OpenCode Free",icon:"terminal",color:"#E87040",textIcon:"OC"},category:"free",noAuth:!0,transport:{baseUrl:"http://127.0.0.1:20142",')) {
  console.log("already patched");
  process.exit(0);
}
const fixed = needle.replace('baseUrl:"https://opencode.ai"', 'baseUrl:"http://127.0.0.1:20142"');
fs.writeFileSync(target, t.slice(0, i) + fixed + t.slice(i + needle.length));
console.log("patched: opencode transport.baseUrl -> http://127.0.0.1:20142");
