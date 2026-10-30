// The gosts-rig front end, for working on it: the page and its files from
// web/ on disk (no build, no cache; the page reloads itself when one's
// saved), everything else (the API, /ws, photos, /ctl/) passed on to a
// running gosts-rig. No dependencies.
//
//   npm run dev                       (on :5173, to localhost:8081)
//   PORT=5174 BACKEND=localhost:8082 npm run dev
import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const PORT = Number(process.env.PORT || 5173);
const [BHOST, BPORT] = (process.env.BACKEND || "localhost:8081").split(":");
const WEB = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../web");
const TYPES = { ".html": "text/html; charset=utf-8", ".js": "text/javascript", ".mjs": "text/javascript", ".css": "text/css", ".json": "application/json",
  ".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".ico": "image/x-icon", ".md": "text/plain; charset=utf-8", ".wasm": "application/wasm" };

// Live reload: the page listens on /__reload (server-sent events); a save in
// web/ tells it to reload (a burst of saves: once).
const listeners = new Set();
let pending = null;
fs.watch(WEB, { recursive: true }, (_, file) => {
  clearTimeout(pending);
  pending = setTimeout(() => {
    console.log(`changed: ${file}: reloading ${listeners.size} page(s)`);
    for (const res of listeners) res.write("data: reload\n\n");
  }, 100);
});
const RELOAD = `<script>new EventSource("/__reload").onmessage = () => location.reload();</script>`;

// fromDisk: the file in web/ for this path ("/": index.html), or null.
function fromDisk(url) {
  let p = decodeURIComponent(new URL(url, "http://x").pathname);
  if (p.endsWith("/")) p += "index.html";
  const f = path.join(WEB, p);
  if (!f.startsWith(WEB + path.sep)) return null;
  try { return fs.statSync(f).isFile() ? f : null; } catch { return null; }
}

// toBackend: the request's headers as the backend expects them (its own
// host and origin: it checks the WebSocket's origin against its host).
function toBackend(h) {
  const out = { ...h, host: `${BHOST}:${BPORT}` };
  if (out.origin) out.origin = `http://${BHOST}:${BPORT}`;
  if (out.referer) out.referer = out.referer.replace(/^https?:\/\/[^/]+/, `http://${BHOST}:${BPORT}`);
  return out;
}

const server = http.createServer((req, res) => {
  if (req.url === "/__reload") {
    res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache", connection: "keep-alive" });
    res.write(": hi\n\n");
    listeners.add(res);
    req.on("close", () => listeners.delete(res));
    return;
  }
  const f = req.method === "GET" || req.method === "HEAD" ? fromDisk(req.url) : null;
  if (f) {
    const ext = path.extname(f);
    let body = fs.readFileSync(f);
    if (ext === ".html") body = Buffer.from(body.toString().replace(/<\/body>|$/i, (m) => RELOAD + m));
    res.writeHead(200, { "content-type": TYPES[ext] || "application/octet-stream", "cache-control": "no-store" });
    return res.end(req.method === "HEAD" ? undefined : body);
  }
  const up = http.request({ host: BHOST, port: BPORT, method: req.method, path: req.url, headers: toBackend(req.headers) }, (r) => {
    res.writeHead(r.statusCode, r.headers);
    r.pipe(res);
  });
  up.on("error", (e) => { res.writeHead(502, { "content-type": "text/plain" }); res.end(`gosts-rig at ${BHOST}:${BPORT}: ${e.message}`); });
  req.pipe(up);
});

// WebSockets (/ws): passed on as they are, both ways.
server.on("upgrade", (req, sock, head) => {
  const up = http.request({ host: BHOST, port: BPORT, method: req.method, path: req.url, headers: toBackend(req.headers) });
  up.on("upgrade", (r, upSock, upHead) => {
    sock.write(`HTTP/1.1 ${r.statusCode} ${r.statusMessage}\r\n` + Object.entries(r.headers).map(([k, v]) => `${k}: ${v}\r\n`).join("") + "\r\n");
    if (upHead.length) sock.write(upHead);
    if (head.length) upSock.write(head);
    upSock.pipe(sock).pipe(upSock);
    const end = () => { sock.destroy(); upSock.destroy(); };
    upSock.on("error", end); sock.on("error", end); upSock.on("close", end); sock.on("close", end);
  });
  up.on("response", (r) => { sock.end(`HTTP/1.1 ${r.statusCode} ${r.statusMessage}\r\n\r\n`); });
  up.on("error", () => sock.destroy());
  up.end();
});

server.listen(PORT, "localhost", () => console.log(`gosts-rig front end: http://localhost:${PORT} (web/ from disk, the rest from ${BHOST}:${BPORT})`));
