#!/usr/bin/env bash
# End-to-end check of Python memory profiling (austin) against real targets.
#
# Usage: test/e2e/python-austin.sh <profiler-python-image>
#
# Each target runs a small Python program in its own container. The agent
# runs from the profiler image with --pid=container:<target>, which gives it
# the same view a debugger pod has: the target's PIDs, but its own
# filesystem. That split is what broke austin, so the targets deliberately
# use interpreters at paths the profiler image does not have (3.12), a shared
# libpython (Debian), and the same path as the profiler's own interpreter
# (3.14) — which must be read from the target, not from the profiler.
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
# Allocates once, then only reads.
cat >"$work/steady.py" <<'PY'
import time
hold = [bytearray(64 * 1024) for _ in range(500)]
while True:
    sum(len(b) for b in hold)
    time.sleep(0.01)
PY
chmod 0644 "$work"/*.py

start_target() { # name image script
	docker run -d --name "$1" -v "$work/$3:/app.py:ro" "$2" python /app.py >/dev/null
	containers+=("$1")
}

# profile <target>: prints the decompressed raw profile, or the agent's error
# on stderr and returns 1.
profile() {
	local agent file
	agent=$(docker run -d --privileged --pid="container:$1" --entrypoint /app/agent "$image" \
		--target-container-id e2e --target-container-runtime containerd \
		--target-container-runtime-path /run/containerd --pid 1 --lang python \
		--profiling-tool austin --output-type raw --duration 5s \
		--grace-period-ending 30s --compressor-type gzip)
	containers+=("$agent")
	for _ in $(seq 1 60); do
		docker logs "$agent" 2>&1 | grep -q '"type":"\(result\|error\)"' && break
		sleep 1
	done
	file=$(docker logs "$agent" 2>&1 | grep -o '"file":"[^"]*"' | cut -d'"' -f4 || true)
	if [ -z "$file" ]; then
		docker logs "$agent" >&2
		return 1
	fi
	# Consumers pick a renderer by suffix; a raw profile must not look like an SVG.
	case "$file" in
	*.txt.gz) ;;
	*) echo "raw profile published as $file, want *.txt.gz" >&2; return 1 ;;
	esac
	docker cp -q "$agent:$file" "$work/profile.gz"
	gzip -dc "$work/profile.gz"
}

start_target t-alpine-312 python:3.12-alpine grow.py
start_target t-debian-312 python:3.12-slim grow.py
start_target t-alpine-314 python:3.14-alpine grow.py
start_target t-steady python:3.12-slim steady.py
sleep 3

failed=0
for t in t-alpine-312 t-debian-312 t-alpine-314; do
	if ! out=$(profile "$t"); then
		echo "FAIL $t: no profile"; failed=1; continue
	fi
	samples=$(grep -c '^P' <<<"$out" || true)
	version=$(grep '^# python:' <<<"$out" || true)
	want=$(docker exec "$t" python -c 'import platform; print(platform.python_version())')
	if [ "$samples" -eq 0 ] || [ "$version" != "# python: $want" ]; then
		echo "FAIL $t: samples=$samples, header '$version', want python $want"; failed=1
	else
		echo "ok   $t: $samples samples, python $want"
	fi
done

if ! out=$(profile t-steady); then
	echo "FAIL t-steady: no profile"; failed=1
elif grep -q '^# no memory growth observed' <<<"$out"; then
	echo "ok   t-steady: reported no memory growth"
else
	echo "FAIL t-steady: missing the no-growth note"; failed=1
fi

exit "$failed"
