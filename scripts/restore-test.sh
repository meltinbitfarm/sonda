#!/usr/bin/env bash
# Local restore test (O5): the real docker-compose.yml with MinIO in place of R2.
#
#   1. build and start the stack (restore finds no replica, app creates an empty DB);
#      check every service runs read-only, without capabilities, with memory/pids limits
#   2. create an app from inside the container, ingest events with curl
#   3. wait until Litestream has replicated them to MinIO
#   4. docker compose down -v: Sonda's data volume is deleted (MinIO's is external, kept)
#   5. start again: restore must rebuild /data/sonda.db from MinIO
#   6. check through MCP that every event is back, that the old ingest key still
#      works and that the resent batch is recognised as duplicates
#   7. recreate the containers with the volume in place: restore must skip
#   8. ingest more, wait, down -v and restore again: replication goes on after a restore
#
# Usage:  scripts/restore-test.sh
# Env:    SONDA_TEST_PLATFORM=linux/amd64  build and run for x86 (emulated on arm Macs)
#         SONDA_TEST_PORT=18080            host port for the app (127.0.0.1 only)
#         KEEP=1                           leave the stack and volumes up at the end
# Needs:  docker with compose v2, curl, sed, od. Exit 0 = PASS.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
PROJECT=sonda-restore-test
PORT=${SONDA_TEST_PORT:-18080}
BASE=http://127.0.0.1:$PORT
MINIO_VOLUME=sonda-restore-test-minio
APP_NAME="Restore Test"
N_EVENTS=20
WORK=$(mktemp -d "${TMPDIR:-/tmp}/sonda-restore-test.XXXXXX")

if [[ -n "${SONDA_TEST_PLATFORM:-}" ]]; then
	export DOCKER_DEFAULT_PLATFORM=$SONDA_TEST_PLATFORM
fi

step() { printf '\n== %s\n' "$*"; }
fail() {
	printf '\nFAIL: %s\n' "$*" >&2
	$DC ps -a >&2 || true
	$DC logs --no-color --tail 40 >&2 || true
	exit 1
}

rand_hex() { od -An -N"$1" -tx1 /dev/urandom | tr -d ' \n'; }
uuid() {
	local h
	h=$(rand_hex 16)
	printf '%s-%s-4%s-8%s-%s' "${h:0:8}" "${h:8:4}" "${h:13:3}" "${h:17:3}" "${h:20:12}"
}

# docker-compose.yml without the Coolify-only key, which plain compose rejects.
sed '/exclude_from_hc/d' "$ROOT/docker-compose.yml" >"$WORK/docker-compose.yml"
TOKEN=$(rand_hex 32)
cat >"$WORK/env" <<EOF
SONDA_MCP_TOKEN=$TOKEN
SONDA_VERSION=restore-test
R2_ENDPOINT=http://minio:9000
R2_BUCKET=sonda-test
R2_PATH=sonda
R2_REGION=us-east-1
R2_ACCESS_KEY_ID=sondatest
R2_SECRET_ACCESS_KEY=$(rand_hex 16)
R2_FORCE_PATH_STYLE=true
SONDA_TEST_PORT=$PORT
SONDA_TEST_MINIO_VOLUME=$MINIO_VOLUME
EOF
DC="docker compose -p $PROJECT --project-directory $ROOT -f $WORK/docker-compose.yml -f $ROOT/docker-compose.test.yml --env-file $WORK/env"

cleanup() {
	local code=$?
	if [[ "${KEEP:-}" == 1 ]]; then
		echo "KEEP=1: stack left running. Compose command:"
		echo "  $DC"
		echo "Remove with: $DC down -v && docker volume rm $MINIO_VOLUME && rm -rf $WORK"
	else
		$DC down -v --remove-orphans >/dev/null 2>&1 || true
		docker volume rm "$MINIO_VOLUME" >/dev/null 2>&1 || true
		rm -rf "$WORK"
	fi
	exit $code
}
trap cleanup EXIT

wait_healthy() {
	for _ in $(seq 1 90); do
		if curl -fsS -o /dev/null "$BASE/healthz" 2>/dev/null; then
			return 0
		fi
		sleep 1
	done
	fail "app not healthy on $BASE/healthz after 90 s"
}

# batch FILE N: writes a batch of N events (fresh event_ids) for 3 users.
batch() {
	local file=$1 n=$2 now i sep="" users
	now=$(($(date +%s) * 1000))
	users=("$(uuid)" "$(uuid)" "$(uuid)")
	{
		printf '{"events":['
		for ((i = 0; i < n; i++)); do
			printf '%s{"event_id":"%s","name":"screen.viewed","ts":%d,"user_id":"%s","session_id":"%s","app_version":"1.0","device_type":"phone","props":{"screen":"s%d"}}' \
				"$sep" "$(uuid)" $((now - i * 1000)) "${users[$((i % 3))]}" "$(uuid)" "$i"
			sep=,
		done
		printf ']}'
	} >"$file"
}

ingest() { # ingest KEY FILE -> response body
	curl -sS -X POST "$BASE/v1/events" -H "Authorization: Bearer $1" \
		-H 'Content-Type: application/json' --data-binary "@$2"
}

mcp_tool() { # mcp_tool NAME ARGS_JSON -> JSON-RPC response
	curl -sS "$BASE/mcp/sonda" -H "Authorization: Bearer $TOKEN" \
		-H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
		-H 'MCP-Protocol-Version: 2025-06-18' \
		-d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}"
}

event_total() { # number of events of the test app according to MCP event_counts
	mcp_tool event_counts "{\"app\":\"$APP_NAME\"}" | grep -o '"total":{"events":[0-9]*' | grep -o '[0-9]*$' || true
}

# Objects in the MinIO bucket, listed from inside the compose network.
replica_objects() {
	docker run --rm --network "${PROJECT}_default" curlimages/curl:8.23.0 -s \
		--aws-sigv4 'aws:amz:us-east-1:s3' -u "sondatest:$(sed -n 's/^R2_SECRET_ACCESS_KEY=//p' "$WORK/env")" \
		"http://minio:9000/sonda-test?list-type=2&prefix=sonda/" | { grep -o '<Key>[^<]*</Key>' || true; } | wc -l | tr -d ' '
}

step "build images${SONDA_TEST_PLATFORM:+ for $SONDA_TEST_PLATFORM}"
$DC down -v --remove-orphans >/dev/null 2>&1 || true
docker volume rm "$MINIO_VOLUME" >/dev/null 2>&1 || true
docker volume create "$MINIO_VOLUME" >/dev/null
if [[ -n "${SONDA_TEST_PLATFORM:-}" ]]; then
	# Helper images may be cached for the host arch only.
	$DC pull --quiet minio minio-init
fi
$DC build --quiet

step "first start: empty bucket, restore must exit 0 and app create a new DB"
$DC up -d
wait_healthy
[[ "$($DC ps -a --format '{{.State}} {{.ExitCode}}' restore)" == "exited 0" ]] || fail "restore did not exit 0"
echo "version: $($DC exec -T app /sonda version)"

step "limits and hardening in effect (O6 M2, S3)"
for svc in restore app litestream; do
	id=$($DC ps -a -q "$svc")
	[[ -n "$id" ]] || fail "no container for $svc"
	read -r ro caps nnp mem swap pids < <(docker inspect -f \
		'{{.HostConfig.ReadonlyRootfs}} {{join .HostConfig.CapDrop ","}} {{join .HostConfig.SecurityOpt ","}} {{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} {{.HostConfig.PidsLimit}}' "$id")
	echo "  $svc: read_only=$ro cap_drop=$caps security_opt=$nnp memory=$mem memswap=$swap pids=$pids"
	[[ "$ro" == true && "$caps" == ALL && "$nnp" == no-new-privileges:true ]] || fail "$svc not hardened"
	[[ "$mem" -gt 0 && "$swap" == "$mem" && "$pids" -gt 0 ]] || fail "$svc has no memory/pids limit"
done

step "create app from inside the container (as nonroot, like a Coolify/docker exec)"
KEY=$($DC exec -T app /sonda app create "$APP_NAME")
[[ "$KEY" == sk_* ]] || fail "app create printed '$KEY'"
echo "ingest key: ${KEY:0:8}..."

step "ingest $N_EVENTS events in two batches"
batch "$WORK/b1.json" $((N_EVENTS / 2))
batch "$WORK/b2.json" $((N_EVENTS / 2))
for b in b1 b2; do
	r=$(ingest "$KEY" "$WORK/$b.json")
	echo "$b: $r"
	[[ "$r" == *"\"accepted\":$((N_EVENTS / 2))"* && "$r" == *'"rejected":0'* ]] || fail "ingest $b: $r"
done
[[ "$(event_total)" == "$N_EVENTS" ]] || fail "MCP sees $(event_total) events before the restore, want $N_EVENTS"

# wait_replicated MIN: until the bucket holds more than MIN objects, then a margin.
wait_replicated() {
	local n=0
	for _ in $(seq 1 60); do
		n=$(replica_objects)
		[[ "$n" -gt "$1" ]] && break
		sleep 1
	done
	[[ "$n" -gt "$1" ]] || fail "nothing new replicated to MinIO after 60 s ($n objects)"
	sleep 5 # sync-interval is 1 s: let the last transaction go out too
	echo "replica objects: $(replica_objects)"
}

# disaster_and_restore: delete the data volume, start again, restore must run.
disaster_and_restore() {
	step "down -v: delete the data volume"
	$DC down -v
	if docker volume inspect "${PROJECT}_sonda-data" >/dev/null 2>&1; then
		fail "data volume still exists"
	fi
	docker volume inspect "$MINIO_VOLUME" >/dev/null || fail "MinIO volume was removed"

	step "start again: restore from MinIO"
	$DC up -d
	wait_healthy
	[[ "$($DC ps -a --format '{{.State}} {{.ExitCode}}' restore)" == "exited 0" ]] || fail "restore did not exit 0"
	# litestream restore prints nothing on success: the proof is the data check.
	$DC logs --no-color restore | sed 's/^/  /'
}

step "wait for Litestream to replicate to MinIO"
wait_replicated 0
disaster_and_restore

step "verify restored data through MCP"
got=$(event_total)
echo "events via MCP event_counts: $got"
[[ "$got" == "$N_EVENTS" ]] || fail "restored $got events, want $N_EVENTS"
apps=$(mcp_tool list_apps '{}')
[[ "$apps" == *"\"name\":\"$APP_NAME\""* ]] || fail "list_apps: $apps"
recent=$(mcp_tool recent_events "{\"app\":\"$APP_NAME\",\"limit\":5}")
[[ "$recent" == *'"name":"screen.viewed"'* ]] || fail "recent_events: $recent"

step "old ingest key still valid; resent batch is all duplicates"
r=$(ingest "$KEY" "$WORK/b1.json")
echo "resend b1: $r"
[[ "$r" == *"\"duplicates\":$((N_EVENTS / 2))"* ]] || fail "resend after restore: $r"
batch "$WORK/b3.json" 1
r=$(ingest "$KEY" "$WORK/b3.json")
[[ "$r" == *'"accepted":1'* && "$r" == *'"duplicates":0'* ]] || fail "new event after restore: $r"

step "file ownership on the restored volume: everything uid 65532 (distroless nonroot)"
owners=$(docker run --rm -v "${PROJECT}_sonda-data:/data:ro" curlimages/curl:8.23.0 sh -c 'ls -lna /data | tail -n +2')
echo "$owners" | sed 's/^/  /'
if echo "$owners" | awk '$9 != ".." && ($3 != 65532 || $4 != 65532) {bad=1} END {exit !bad}'; then
	fail "files in /data not owned by 65532"
fi
$DC exec -T app /sonda app list

step "recreate containers with the volume in place: restore must skip"
$DC up -d --force-recreate
wait_healthy
$DC logs --no-color restore | sed 's/^/  /'
got=$(event_total)
[[ "$got" == "$((N_EVENTS + 1))" ]] || fail "after recreate: $got events, want $((N_EVENTS + 1))"

step "replication continues after a restore: second disaster"
before=$(replica_objects)
batch "$WORK/b4.json" 5
r=$(ingest "$KEY" "$WORK/b4.json")
[[ "$r" == *'"accepted":5'* ]] || fail "ingest b4: $r"
wait_replicated "$before"
disaster_and_restore
got=$(event_total)
echo "events via MCP event_counts: $got"
[[ "$got" == "$((N_EVENTS + 6))" ]] || fail "after the second restore: $got events, want $((N_EVENTS + 6))"

printf '\nPASS: events restored twice from the replica (%d, then %d) and served via MCP\n' "$N_EVENTS" "$((N_EVENTS + 6))"
