#!/usr/bin/env node
/**
 * Router combo seeder — makes bare model names (no oc/ prefix) resolve to the
 * reset-shim lane, automatically, on any machine.
 *
 * Both 9router variants (Node and the Go gateway) default to the same
 * DATA_DIR, whose sqlite carries a `combos` table. One single-member combo
 * per free model — named exactly like the bare model name and pointing at
 * its oc/<model> form — is all the routing the prefix-free lane needs:
 *
 *   glm-5.3-flash-free  ->  oc/glm-5.3-flash-free
 *   glm-5.3-flash       ->  oc/glm-5.3-flash-free   (suffix-less variant)
 *   ...
 *
 * Idempotent: combos that already exist (any shape) are left untouched, so
 * re-running after an upgrade or by the start script is always safe. When no
 * router database is present the script exits 0 with a note — the standalone
 * shim (port 20142) needs no routing at all.
 */
const fs = require("fs");
const path = require("path");
const os = require("os");
const crypto = require("crypto");

const FREE_MODELS = [
  "glm-5.3-flash-free",
  "mimo-v2.6-flash-free",
  "deepseek-v4-flash-free",
  "deepseek-v4.1-flash-free",
  "muse-spark-1.3-contributor-free",
  "jev-1.13-free",
];

// Bare name + suffix-less variant both route to the same -free model.
function comboTargets() {
  const pairs = [];
  for (const m of FREE_MODELS) {
    pairs.push([m, "oc/" + m]);
    const bare = m.replace(/-free$/, "");
    if (bare !== m) pairs.push([bare, "oc/" + m]);
  }
  return pairs;
}

function candidateDbs() {
  const list = [];
  if (process.env.N9R_DB) list.push(process.env.N9R_DB);
  if (process.env.APPDATA) list.push(path.join(process.env.APPDATA, "9router", "db", "data.sqlite"));
  list.push(path.join(os.homedir(), ".9router", "db", "data.sqlite"));
  return [...new Set(list)].filter((p) => { try { return fs.existsSync(p); } catch (_) { return false; } });
}

function seed(dbPath) {
  let DatabaseSync;
  try { ({ DatabaseSync } = require("node:sqlite")); }
  catch (_) { console.log("[seed-combos] node:sqlite unavailable (node >= 22 required); skipped " + dbPath); return; }
  let db;
  try { db = new DatabaseSync(dbPath); }
  catch (e) { console.log("[seed-combos] cannot open " + dbPath + ": " + String(e).slice(0, 80)); return; }
  try {
    const tables = db.prepare("SELECT name FROM sqlite_master WHERE type='table' AND name='combos'").all();
    if (!tables.length) { console.log("[seed-combos] no combos table in " + dbPath + "; skipped"); return; }
    const existing = new Set(db.prepare("SELECT name FROM combos").all().map((r) => r.name));
    const now = new Date().toISOString();
    let inserted = 0;
    for (const [name, target] of comboTargets()) {
      if (existing.has(name)) continue;
      db.prepare("INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?,?,?,?,?,?)")
        .run(crypto.randomUUID(), name, null, JSON.stringify([target]), now, now);
      inserted++;
    }
    console.log("[seed-combos] " + dbPath + ": " + inserted + " combo(s) added, " + existing.size + " already present");
  } finally {
    try { db.close(); } catch (_) {}
  }
}

function main() {
  const dbs = candidateDbs();
  if (!dbs.length) {
    console.log("[seed-combos] no 9router database found — standalone shim mode, nothing to seed");
    return;
  }
  for (const dbPath of dbs) {
    try { seed(dbPath); } catch (e) { console.log("[seed-combos] " + dbPath + " failed: " + String(e).slice(0, 120)); }
  }
}

if (require.main === module) main();
module.exports = { comboTargets, candidateDbs };
