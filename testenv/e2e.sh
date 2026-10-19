#!/usr/bin/env bash
# Runs three `isutori bench` concurrently against a dockerized s1 and checks
# that their deploy..cleanup windows never overlap, then checks that Ctrl-C
# still runs cleanup, holding the queue until it ends, that a second Ctrl-C
# kills cleanup and frees the queue at once, and that a failed benchmark
# exits with 2.
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
git symbolic-ref HEAD refs/heads/main
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
if [ "\$1" = cleanup ] && [ -n "\${CLEANUP_SLEEP:-}" ]; then
  sleep "\$CLEANUP_SLEEP"
  ssh -F $work/ssh_config s1 "echo \$ISUTORI_TICKET_ID cleaned >> markers"
fi
EOF
chmod +x ops/hook.sh
git add . && git commit -qm init && git push -q origin HEAD:main 2>/dev/null
git fetch -q origin

# Waits until $2 shows $3, failing if process $1 exits first.
wait_log() {
  until grep -q "$3" "$2"; do
    kill -0 "$1" 2>/dev/null || { echo "$2 ended without '$3'"; cat "$2"; exit 1; }
    sleep 0.2
  done
}

post_results() {
  local passed=${1:-true}
  while :; do
    id=$(curl -sf http://127.0.0.1:17878/api/run | sed -n 's/.*"run_id":"\([^"]*\)".*/\1/p') || true
    [ -n "$id" ] && curl -sf -H 'Content-Type: application/json' \
      -d "{\"run_id\":\"$id\",\"passed\":$passed,\"score\":123,\"message\":\"ok\"}" \
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

# Ctrl-C during bench, with a cleanup longer than lease_ttl and eve waiting:
# eve must not deploy until dave's cleanup ends. The poster is stopped so that
# dave waits on the result.
kill $poster
trap 'docker compose -f "$here/compose.yml" down' EXIT
ssh -F "$work/ssh_config" s1 'rm -f markers'
CLEANUP_SLEEP=15 isutori bench --requester dave > "$work/dave.json" 2> "$work/dave.log" &
dave=$!
wait_log $dave "$work/dave.log" '== bench'
isutori bench --requester eve > "$work/eve.json" 2> "$work/eve.log" &
eve=$!
wait_log $eve "$work/eve.log" 'waiting: 1 ahead'
kill -INT $dave
post_results &
poster=$!
trap 'kill $poster; docker compose -f "$here/compose.yml" down' EXIT
status=0; wait $dave || status=$?
[ "$status" = 1 ] || { echo "interrupted bench exited with $status, want 1"; fail=1; }
wait $eve || { echo "eve's bench exited with $?"; fail=1; }
ssh -F "$work/ssh_config" s1 cat markers > "$work/markers"
awk '
  $2 == "deploy" && ++deploys == 2 { eve = NR }
  $2 == "cleaned" { cleaned = NR }
  END { exit !(cleaned && eve && cleaned < eve) }
' "$work/markers" || { echo "eve deployed before dave's cleanup ended"; cat "$work/markers"; fail=1; }
[ "$(isutori status --json | grep -c '"id"')" = 0 ] || { echo "ticket left in queue after SIGINT"; fail=1; }

kill $poster
post_results false &
poster=$!
status=0; isutori bench --requester frank > "$work/frank.json" 2> "$work/frank.log" || status=$?
[ "$status" = 2 ] || { echo "failed bench exited with $status, want 2"; fail=1; }

# A second Ctrl-C kills a long cleanup at once and still frees the queue.
kill $poster
trap 'docker compose -f "$here/compose.yml" down' EXIT
CLEANUP_SLEEP=60 isutori bench --requester gina > "$work/gina.json" 2> "$work/gina.log" &
gina=$!
wait_log $gina "$work/gina.log" '== bench'
kill -INT $gina
wait_log $gina "$work/gina.log" '== cleanup'
sleep 2
kill -INT $gina
start=$SECONDS
wait $gina || true
[ $((SECONDS - start)) -lt 15 ] || { echo "second Ctrl-C took $((SECONDS - start))s to exit"; fail=1; }
pgrep -fx 'sleep 60' > /dev/null && { echo "cleanup survived the second Ctrl-C"; pkill -fx 'sleep 60'; fail=1; }
[ "$(isutori status --json | grep -c '"id"')" = 0 ] || { echo "ticket left in queue after a second Ctrl-C"; fail=1; }

if [ "$fail" = 0 ]; then echo "e2e: OK"; else echo "e2e: FAILED (logs in $work)"; fi
exit $fail
