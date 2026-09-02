#!/usr/bin/env bash
# Reproducible 20–40 second Conduit terminal demo. Run from any directory.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
audit_file="$repo_root/docs/demo/conduit-demo-audit.jsonl"
log_dir="$(mktemp -d)"
downstream_pid=""
conduit_pid=""

cleanup() {
  [[ -n "$conduit_pid" ]] && kill "$conduit_pid" 2>/dev/null || true
  [[ -n "$downstream_pid" ]] && kill "$downstream_pid" 2>/dev/null || true
  wait 2>/dev/null || true
  rm -f "$audit_file"
  rm -rf "$log_dir"
}
trap cleanup EXIT INT TERM

pause() {
  if [[ "${CONDUIT_DEMO_PAUSE:-}" == "1" ]]; then sleep 4; fi
}

cd "$repo_root"
rm -f "$audit_file"

printf '\n$ python docs/demo/downstreams.py\n'
python3 docs/demo/downstreams.py >"$log_dir/downstreams.log" 2>&1 &
downstream_pid=$!
until grep -q "ready" "$log_dir/downstreams.log"; do sleep 0.05; done

printf '$ conduit --config docs/demo/conduit-demo.yaml\n'
conduit --config docs/demo/conduit-demo.yaml >"$log_dir/conduit.log" 2>&1 &
conduit_pid=$!

printf 'starting Conduit...\n'
ready=""
for _ in $(seq 1 60); do
  ready="$(curl -fsS http://127.0.0.1:8080/healthz 2>/dev/null || true)"
  if grep -q '"ready":true' <<<"$ready"; then break; fi
  sleep 0.1
done
if ! grep -q '"ready":true' <<<"$ready"; then
  echo "Conduit did not become ready" >&2
  exit 1
fi
printf '%s' "$ready" | python3 -c 'import json,sys; value=json.load(sys.stdin); print("ready:", value["ready"], "| healthy downstreams:", ", ".join(item["id"] for item in value["downstreams"] if item["state"] == "healthy"))'

printf '\n$ python docs/demo/client.py list\n'
python3 docs/demo/client.py list
pause
printf '\n$ python docs/demo/client.py call calc.add '\''{"a": 20, "b": 22}'\''\n'
python3 docs/demo/client.py call calc.add '{"a": 20, "b": 22}'
pause
printf '\n$ python docs/demo/client.py call admin.reset '{}'\n'
python3 docs/demo/client.py call admin.reset '{}'
pause

printf '\n$ audit events\n'
python3 - "$audit_file" <<'PY'
import json
import sys

for line in open(sys.argv[1], encoding="utf-8"):
    event = json.loads(line)
    if event["event"] != "audit_ready":
        print(f'{event["event"]}: {event.get("public_tool", "-")}')
PY
pause
