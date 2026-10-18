#!/usr/bin/env bash
# Runs three `isutori bench` concurrently against a dockerized s1 and checks
# that their deploy..cleanup windows never overlap, then checks that Ctrl-C
# still runs cleanup and frees the queue.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work="$here/.work"
rm -rf "$work"
mkdir -p "$work"

ssh-keygen -q -t ed25519 -N '' -f "$work/id"
cp "$work/id.pub" "$here/authorized_keys"
cat > "$work/ssh_config" <<EOF
Host s1
  HostName 127.0.0.1
  Port 2222
  User isucon
  IdentityFile $work/id
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
EOF

docker compose -f "$here/compose.yml" up -d --build --wait
trap 'docker compose -f "$here/compose.yml" down' EXIT
for _ in $(seq 30); do ssh -F "$work/ssh_config" s1 true 2>/dev/null && break; sleep 0.5; done
ssh -F "$work/ssh_config" s1 'rm -rf .isutori markers'

(cd "$here/.." && go build -o "$work/bin/isutori" .)
export PATH="$work/bin:$PATH"

git init -q --bare "$work/origin.git"
git clone -q "$work/origin.git" "$work/repo" 2>/dev/null
cd "$work/repo"
git config user.name tester
git config user.email tester@example.com
mkdir ops
cat > isutori.toml <<EOF
[queue]
host = "s1"
lease_ttl = "10s"
ssh_command = "ssh -F $work/ssh_config"

[hooks]
deploy  = "isutori guard && ops/hook.sh deploy"
health  = "ops/hook.sh health"
prepare = "ops/hook.sh prepare"
collect = "ops/hook.sh collect"
cleanup = "ops/hook.sh cleanup"

[bench]
listen = "127.0.0.1:17878"
timeout = "60s"
EOF
cat > ops/hook.sh <<EOF
#!/bin/sh
set -e
ssh -F $work/ssh_config s1 "echo \$ISUTORI_TICKET_ID \$1 >> markers"
sleep 1
EOF
chmod +x ops/hook.sh
git add . && git commit -qm init && git push -q origin HEAD:main 2>/dev/null
git fetch -q origin

post_results() {
  while :; do
    id=$(curl -sf http://127.0.0.1:17878/api/run | sed -n 's/.*"run_id":"\([^"]*\)".*/\1/p') || true
    [ -n "$id" ] && curl -sf -H 'Content-Type: application/json' \
      -d "{\"run_id\":\"$id\",\"passed\":true,\"score\":123,\"message\":\"ok\"}" \
      http://127.0.0.1:17878/api/result || true
    sleep 0.3
  done
}
post_results &
poster=$!
trap 'kill $poster; docker compose -f "$here/compose.yml" down' EXIT

for who in alice bob carol; do
  isutori bench --requester "$who" > "$work/$who.json" 2> "$work/$who.log" &
done
fail=0
for job in $(jobs -p); do
  [ "$job" = "$poster" ] && continue
  wait "$job" || { echo "a bench exited with $?"; fail=1; }
done

ssh -F "$work/ssh_config" s1 cat markers > "$work/markers"
awk '
  $2 == "deploy"  { if (owner != "") { print "overlap at line " NR; bad = 1 } owner = $1; next }
  $1 != owner     { print "foreign stage at line " NR; bad = 1 }
  $2 == "cleanup" { owner = ""; n++ }
  END { if (n != 3) { print "expected 3 runs, got " n; bad = 1 } exit bad }
' "$work/markers" || { cat "$work/markers"; fail=1; }

ISUTORI_TICKET_ID=bogus ISUTORI_CONFIG="$work/repo/isutori.toml" isutori guard 2>/dev/null \
  && { echo "guard passed for a ticket not in the queue"; fail=1; }
grep -q 'now running main by' "$work/bob.log" "$work/carol.log" "$work/alice.log" || { echo "queue does not show branch names"; fail=1; }

# Ctrl-C during bench: the poster is stopped so the run waits on the result.
kill $poster
trap 'docker compose -f "$here/compose.yml" down' EXIT
ssh -F "$work/ssh_config" s1 'rm -f markers'
isutori bench --requester dave > "$work/dave.json" 2> "$work/dave.log" &
dave=$!
until grep -q '== bench' "$work/dave.log"; do sleep 0.2; done
kill -INT $dave
status=0; wait $dave || status=$?
[ "$status" = 1 ] || { echo "interrupted bench exited with $status, want 1"; fail=1; }
ssh -F "$work/ssh_config" s1 cat markers | grep -q cleanup || { echo "cleanup did not run after SIGINT"; fail=1; }
[ "$(isutori status --json | grep -c '"id"')" = 0 ] || { echo "ticket left in queue after SIGINT"; fail=1; }

if [ "$fail" = 0 ]; then echo "e2e: OK"; else echo "e2e: FAILED (logs in $work)"; fi
exit $fail
