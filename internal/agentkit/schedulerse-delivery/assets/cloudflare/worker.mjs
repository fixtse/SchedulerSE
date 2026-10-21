const headers = {"Content-Type": "application/json", "Cache-Control": "no-store"};
const reply = (status, value) => new Response(JSON.stringify(value), {status, headers});

async function authorized(request, secret) {
  if (!secret) return false;
  const bytes = new TextEncoder();
  const digest = async value => new Uint8Array(await crypto.subtle.digest("SHA-256", bytes.encode(value)));
  const [actual, expected] = await Promise.all([
    digest(request.headers.get("Authorization") || ""), digest(`Bearer ${secret}`)
  ]);
  let mismatch = 0;
  for (let i = 0; i < actual.length; i++) mismatch |= actual[i] ^ expected[i];
  return mismatch === 0;
}

async function readEvent(request) {
  if (!(request.headers.get("Content-Type") || "").startsWith("application/json")) return null;
  const reader = request.body?.getReader();
  if (!reader) return null;
  const chunks = [];
  let length = 0;
  while (true) {
    const {done, value} = await reader.read();
    if (done) break;
    length += value.length;
    if (length > 65536) { await reader.cancel(); return null; }
    chunks.push(value);
  }
  const bytes = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
  let event;
  try { event = JSON.parse(new TextDecoder("utf-8", {fatal: true}).decode(bytes)); } catch { return null; }
  if (event?.event !== "schedule.due" || typeof event.event_id !== "string" ||
      !event.event_id.length || event.event_id.length > 256 ||
      typeof event.task?.id !== "string" || !["timer", "reminder", "task"].includes(event.task.kind) ||
      !event.task.context || typeof event.task.context !== "object" || Array.isArray(event.task.context)) return null;
  return event;
}

export default {
  async fetch(request, env) {
    try {
      const path = new URL(request.url).pathname;
      if (path === "/health" && request.method === "GET") return reply(200, {status: "ok"});
      if (path === "/hooks/schedulerse" && request.method === "POST") {
        if (!await authorized(request, env.WEBHOOK_TOKEN)) return reply(401, {error: "Unauthorized"});
        const event = await readEvent(request);
        if (!event) return reply(400, {error: "Invalid event or body exceeds 64 KiB"});
        const payload = JSON.stringify(event);
        // Retain receipts for 30 days. Unacknowledged reminders never expire here.
        await env.DB.prepare("DELETE FROM events WHERE acknowledged_at < ?").bind(Math.floor(Date.now()/1000)-2592000).run();
        const existing = await env.DB.prepare("SELECT payload,acknowledged_at FROM events WHERE id=?").bind(event.event_id).first();
        if (existing) return existing.acknowledged_at !== null || existing.payload === payload
          ? reply(202, {accepted: true}) : reply(409, {error: "Event ID conflicts"});
        await env.DB.prepare("INSERT OR IGNORE INTO events(id,payload,created_at) VALUES(?,?,?)")
          .bind(event.event_id, payload, Math.floor(Date.now()/1000)).run();
        // Check concurrent submissions with the same ID without exposing stored data.
        const saved = await env.DB.prepare("SELECT payload,acknowledged_at FROM events WHERE id=?").bind(event.event_id).first();
        return saved && (saved.acknowledged_at !== null || saved.payload === payload)
          ? reply(202, {accepted: true}) : reply(409, {error: "Event ID conflicts"});
      }
      if (path === "/inbox" && request.method === "GET") {
        if (!await authorized(request, env.POLL_TOKEN)) return reply(401, {error: "Unauthorized"});
        const {results} = await env.DB.prepare("SELECT payload FROM events WHERE acknowledged_at IS NULL ORDER BY created_at,id LIMIT 10").all();
        return reply(200, results.map(row => JSON.parse(row.payload)));
      }
      if (path.startsWith("/inbox/") && request.method === "DELETE") {
        if (!await authorized(request, env.POLL_TOKEN)) return reply(401, {error: "Unauthorized"});
        const id = decodeURIComponent(path.slice("/inbox/"));
        if (!id || id.length > 256) return reply(400, {error: "Invalid ID"});
        await env.DB.prepare("UPDATE events SET acknowledged_at=?,payload=NULL WHERE id=? AND acknowledged_at IS NULL")
          .bind(Math.floor(Date.now()/1000),id).run();
        return new Response(null, {status: 204, headers: {"Cache-Control": "no-store"}});
      }
      return reply(404, {error: "Not found"});
    } catch {
      // Provider errors may contain database details. Keep responses and logs generic.
      return reply(503, {error: "Inbox temporarily unavailable"});
    }
  }
};
