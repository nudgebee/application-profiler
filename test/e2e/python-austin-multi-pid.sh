#!/usr/bin/env bash
# End-to-end check that a PID the profiler cannot attach to does not fail the
# run, using Python memory profiling (austin).
#
# Usage: test/e2e/python-austin-multi-pid.sh <profiler-python-image>
#
# Each target runs a Python program next to a non-Python process, so the
# container's leaf processes — what the agent profiles when no --pid is
# given — are one austin can read and one it cannot. The run must publish the
# Python PID's profile, name the other PID in a notice, and not fail. PIDs
# are started one after another, so both orders are checked.
#
# The agent finds the container's root PID through the containerd runtime
# directory. A minimal stand-in for it, pointing at PID 1 of the target, is
# mounted where the agent looks; the agent runs with --pid=container:<target>
# as in python-austin.sh, so that PID 1 is the target's.
set -euo pipefail

image=${1:?usage: $0 <profiler-python-image>}
work=$(mktemp -d)
containers=()
cleanup() {
	if [ ${#containers[@]} -gt 0 ]; then docker rm -f "${containers[@]}" >/dev/null 2>&1 || true; fi
	rm -rf "$work"
}
trap cleanup EXIT

# Grows by 64 KiB every 5 ms, resetting at ~128 MiB so the container stays small.
cat >"$work/grow.py" <<'PY'
import time
hold = []
while True:
    hold.append(bytearray(64 * 1024))
    if len(hold) >= 2000:
        hold = []
    time.sleep(0.005)
PY
chmod 0644 "$work/grow.py"

# The agent reads the root PID from <runtime path>/io.containerd.runtime.v2.task/k8s.io/<id>/init.pid.
mkdir -p "$work/runtime/io.containerd.runtime.v2.task/k8s.io/e2e"
printf 1 >"$work/runtime/io.containerd.runtime.v2.task/k8s.io/e2e/init.pid"

# event <type> <field>: prints data.<field> of every agent event of that type.
event() {
	jq -rR --arg type "$1" --arg field "$2" 'fromjson? | select(.type == $type) | .data[$field]'
}

failed=0
fail() {
	echo "FAIL $1: $2"
	failed=$((failed + 1))
}

# check <target> <command>: profiles the target, whose command starts a python
# and a sleep process, without --pid.
check() {
	local target=$1 agent logs detected p py="" other="" files samples notice err
	local -a pids
	docker run -d --name "$target" -v "$work/grow.py:/app.py:ro" python:3.12-slim sh -c "$2" >/dev/null
	containers+=("$target")
	sleep 3

	agent=$(docker run -d --privileged --pid="container:$target" \
		-v "$work/runtime:/run/containerd:ro" --entrypoint /app/agent "$image" \
		--target-container-id e2e --target-container-runtime containerd \
		--target-container-runtime-path /run/containerd --lang python \
		--profiling-tool austin --output-type raw --duration 5s \
		--grace-period-ending 30s --compressor-type gzip)
	containers+=("$agent")
	for _ in $(seq 1 60); do
		docker logs "$agent" 2>&1 | grep -q '"stage":"ended"\|"type":"error"' && break
		sleep 1
	done
	logs=$(docker logs "$agent" 2>&1)
	local before=$failed

	# Without two leaf PIDs there is nothing to tolerate, and the checks below
	# would pass for the wrong reason.
	detected=$(event notice msg <<<"$logs" | grep -o 'Detected more than one PID to profile: \[[0-9 ]*\]' | grep -o '[0-9 ]*\]' | tr -d ']' || true)
	read -r -a pids <<<"$detected"
	if [ ${#pids[@]} -eq 2 ]; then
		for p in "${pids[@]}"; do
			case $(docker exec "$target" cat "/proc/$p/comm") in
			python*) py=$p ;;
			sleep) other=$p ;;
			esac
		done
	fi
	if [ -z "$py" ] || [ -z "$other" ]; then
		fail "$target" "want a python and a sleep leaf PID, the agent detected [${detected}]"
	fi

	err=$(event error reason <<<"$logs")
	if [ -n "$err" ]; then
		fail "$target" "the run failed: $err"
	fi

	files=$(event result file <<<"$logs")
	if [ "$(grep -c . <<<"$files")" -ne 1 ]; then
		fail "$target" "want one result, got: ${files:-none}"
	elif [[ $files != *"-$py-"* ]]; then
		fail "$target" "the result $files is not the python PID $py"
	else
		docker cp -q "$agent:$files" "$work/profile.gz"
		samples=$(gzip -dc "$work/profile.gz" | grep -c '^P' || true)
		if [ "$samples" -eq 0 ]; then
			fail "$target" "the python PID's profile has no samples"
		else
			echo "ok   $target: PID $py (python) published $files, $samples samples"
		fi
	fi

	notice=$(event notice msg <<<"$logs" | grep "^Profiled 1 of 2 PIDs; skipped PID $other: " || true)
	if [ -z "$notice" ]; then
		fail "$target" "no notice naming the skipped PID $other"
	else
		echo "ok   $target: PID $other (sleep) skipped with a notice: $notice"
	fi

	if [ "$failed" -ne "$before" ]; then
		echo "--- $target: agent events" >&2
		grep -v '"type":"log"' <<<"$logs" >&2 || true
	fi
}

check t-python-first 'python /app.py & sleep 100000 & wait'
check t-sleep-first 'sleep 100000 & python /app.py & wait'

exit $((failed > 0))
