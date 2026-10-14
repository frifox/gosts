#!/bin/sh
# install-log.sh: make install, its output shown as a page on port $1
# (default 8089): for Claude's server list (.claude/launch.json), whose
# pane shows a page, not a command's output. The page follows the log as
# it's written and says how it ended; then the server and the page's tab
# close (a few seconds on: time for the page to fetch the end).
# Refuses while GoSTS Rig is running (its app replaced under it).
set -u

port=${1:-8089}
root=$(cd "$(dirname "$0")/../.." && pwd)
dir=$(mktemp -d "${TMPDIR:-/tmp}/gosts-install-XXXXXX")
trap 'kill "$server" 2>/dev/null; rm -rf "$dir"' EXIT INT TERM

cat > "$dir/index.html" <<'HTML'
<!doctype html>
<html><head><meta charset="utf-8"><title>make install</title>
<style>
  :root { color-scheme: dark; }
  body { margin: 0; background: #12151b; color: #e6e9ef; font: 13px ui-monospace, SFMono-Regular, Menlo, monospace; }
  header { position: sticky; top: 0; padding: 10px 16px; background: #1b2029; border-bottom: 1px solid #2a303c; font: 600 14px system-ui; }
  header .ok { color: #3ccf7a; } header .bad { color: #ff6b6b; } header .run { color: #9aa3b2; }
  pre { margin: 0; padding: 12px 16px; white-space: pre-wrap; word-break: break-word; }
</style></head>
<body><header>make install · <span id="st" class="run">running…</span></header><pre id="log"></pre>
<script>
let done = false;
async function poll() {
  try {
    const log = await (await fetch("log.txt?" + Date.now())).text();
    const pre = document.getElementById("log"), atEnd = innerHeight + scrollY >= document.body.scrollHeight - 4;
    if (pre.textContent !== log) { pre.textContent = log; if (atEnd) scrollTo(0, document.body.scrollHeight); }
    const r = await fetch("status.txt?" + Date.now());
    if (r.ok) {
      const code = (await r.text()).trim(), st = document.getElementById("st");
      st.textContent = code === "0" ? "done ✓" : `failed (exit ${code})`;
      st.className = code === "0" ? "ok" : "bad";
      done = true;
      setTimeout(() => window.close(), 3000); // (the tab closes as the server does)
    }
  } catch {}
  if (!done) setTimeout(poll, 400);
}
poll();
</script></body></html>
HTML
: > "$dir/log.txt"

python3 -m http.server "$port" --bind 127.0.0.1 --directory "$dir" >/dev/null 2>&1 &
server=$!

{
  if pgrep -f '/Applications/GoSTS Rig.app/Contents/MacOS/' >/dev/null; then
    echo "GoSTS Rig is running: quit it first, then run make install again."
    code=1
  else
    cd "$root" && make install
    code=$?
  fi
  echo "$code" > "$dir/status.txt.tmp" && mv "$dir/status.txt.tmp" "$dir/status.txt"
} 2>&1 | tee -a "$dir/log.txt"

sleep 3 # (the page polls every 0.4 s: it has the end by then, and stops)
