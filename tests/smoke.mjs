#!/usr/bin/env node
/**
 * End-to-end smoke battery for the oc-free-tier-shim chain.
 * Requires the shim (20142) and worker to be running.
 *
 *   node tests/smoke.mjs
 *
 * Exit code 0 = all assertions passed; 1 = at least one failed.
 */
const BASE = "http://127.0.0.1:20142";
const results = [];
let failed = 0;

function report(name, ok, detail) {
  results.push({ name, ok, detail });
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + name + (detail ? "  — " + detail : ""));
}

async function chat(body) {
  const r = await fetch(BASE + "/v1/chat/completions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  return r.json();
}

// 1x1-equivalent 32x32 solid-blue PNG generated with correct CRCs
import zlib from "node:zlib";
function makePng() {
  let T = [];
  for (let n = 0; n < 256; n++) { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xEDB88320 ^ (c >>> 1) : c >>> 1; T[n] = c >>> 0; }
  const crc32 = (buf) => { let c = 0xFFFFFFFF; for (const b of buf) c = T[(c ^ b) & 0xFF] ^ (c >>> 8); return (c ^ 0xFFFFFFFF) >>> 0; };
  const chunk = (type, data) => {
    const len = Buffer.alloc(4); len.writeUInt32BE(data.length);
    const td = Buffer.concat([Buffer.from(type), data]);
    const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(td));
    return Buffer.concat([len, td, crc]);
  };
  const w = 32, h = 32;
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0); ihdr.writeUInt32BE(h, 4); ihdr[8] = 8; ihdr[9] = 2;
  const raw = Buffer.alloc(h * (1 + w * 3));
  for (let y = 0; y < h; y++) { raw[y * (1 + w * 3)] = 0; for (let x = 0; x < w; x++) { const o = y * (1 + w * 3) + 1 + x * 3; raw[o] = 30; raw[o + 1] = 80; raw[o + 2] = 220; } }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]),
    chunk("IHDR", ihdr), chunk("IDAT", zlib.deflateSync(raw)), chunk("IEND", Buffer.alloc(0)),
  ]);
}

(async () => {
  // 0. health
  try {
    const h = await (await fetch(BASE + "/health")).json();
    report("health", h.ok === true);
  } catch (e) { report("health", false, String(e).slice(0, 60)); }

  // 1. plain text
  try {
    const j = await chat({ model: "glm-5.3-flash-free", messages: [{ role: "user", content: "Reply with exactly: TEXT-OK" }] });
    report("text round-trip", /TEXT-OK/.test(j.choices?.[0]?.message?.content || ""), (j.choices?.[0]?.message?.content || j.error?.message || "").slice(0, 40));
  } catch (e) { report("text round-trip", false, String(e).slice(0, 60)); }

  // 2. tool calling
  try {
    const j = await chat({
      model: "glm-5.3-flash-free",
      messages: [{ role: "user", content: "What is the weather in Paris right now?" }],
      tools: [{ type: "function", function: { name: "get_weather", description: "Get current weather for a city", parameters: { type: "object", properties: { city: { type: "string" } }, required: ["city"] } } }],
    });
    const tc = j.choices?.[0]?.message?.tool_calls?.[0];
    report("tool calling", tc?.function?.name === "get_weather", tc ? tc.function.arguments : (j.error?.message || "no tool call"));
  } catch (e) { report("tool calling", false, String(e).slice(0, 60)); }

  // 3. vision
  try {
    const b64 = makePng().toString("base64");
    const j = await chat({
      model: "glm-5.3-flash-free",
      messages: [{ role: "user", content: [
        { type: "text", text: "Is this image mostly blue or mostly red? Answer with one word." },
        { type: "image_url", image_url: { url: "data:image/png;base64," + b64 } },
      ] }],
    });
    const c = j.choices?.[0]?.message?.content || "";
    report("vision", /blue/i.test(c), c.slice(0, 60) || j.error?.message);
  } catch (e) { report("vision", false, String(e).slice(0, 60)); }

  // 4. streaming
  try {
    const r = await fetch(BASE + "/v1/chat/completions", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ model: "glm-5.3-flash-free", stream: true, messages: [{ role: "user", content: "Reply with exactly: STREAM-OK" }] }),
    });
    let text = "", gotDone = false;
    const dec = new TextDecoder();
    for await (const part of r.body) {
      for (const line of dec.decode(part).split("\n")) {
        if (line === "data: [DONE]") gotDone = true;
        if (!line.startsWith("data: ") || line.includes("[DONE]")) continue;
        try { const c = JSON.parse(line.slice(6)).choices?.[0]?.delta?.content; if (c) text += c; } catch (_) {}
      }
    }
    report("streaming SSE", gotDone && /STREAM-OK/.test(text), "done=" + gotDone + " text=" + text.slice(0, 30));
  } catch (e) { report("streaming SSE", false, String(e).slice(0, 60)); }

  // 5. limit-reset battery: 5 sequential, zero limit errors
  try {
    let ok = 0, limitErrs = 0;
    for (let i = 1; i <= 5; i++) {
      const j = await chat({ model: "glm-5.3-flash-free", messages: [{ role: "user", content: "Reply with exactly the number " + i }] });
      const err = j.error?.message || "";
      if (/429|limit|usage|quota/i.test(err)) limitErrs++;
      if (!err && (j.choices?.[0]?.message?.content || "").includes(String(i))) ok++;
    }
    report("limit reset (5x sequential)", ok === 5 && limitErrs === 0, ok + "/5 ok, limit errors: " + limitErrs);
  } catch (e) { report("limit reset (5x sequential)", false, String(e).slice(0, 60)); }

  // 6. context preservation with an IDE-sized tool table (regression: the old
  // shim prepended 339 tool defs and truncated from the end, amputating the
  // conversation — the model then answered generically).
  try {
    const tools = [];
    for (let i = 1; i <= 339; i++) {
      tools.push({ type: "function", function: { name: "ide_tool_" + i, description: "Tool number " + i + " that does IDE thing " + i + " with several words of description text to inflate the payload size significantly.", parameters: { type: "object", properties: { path: { type: "string", description: "file path parameter with a longish explanation for size" }, content: { type: "string" } } } } });
    }
    const history = [];
    for (let i = 1; i <= 20; i++) {
      history.push({ role: "user", content: "Message " + i + ": we are working on the NIMBUS-ROCKET project deployment." });
      history.push({ role: "assistant", content: "Understood, continuing with NIMBUS-ROCKET deployment step " + i + "." });
    }
    history.push({ role: "user", content: "What project are we working on? Answer with just the project name." });
    const j = await chat({ model: "glm-5.3-flash-free", messages: history, tools });
    const c = j.choices?.[0]?.message?.content || "";
    report("context w/ 339 tools", /NIMBUS-ROCKET/i.test(c), c.slice(0, 40) || j.error?.message);
  } catch (e) { report("context w/ 339 tools", false, String(e).slice(0, 60)); }

  console.log("\n" + results.filter(r => r.ok).length + "/" + results.length + " passed");
  process.exit(failed ? 1 : 0);
})();
