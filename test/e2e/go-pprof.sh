#!/usr/bin/env bash
# End-to-end check of Go pprof profiling against real Go servers.
#
# Usage: test/e2e/go-pprof.sh <profiler-bpf-image>
#
# The agent scrapes /debug/pprof with wget from inside the target's network
# namespace, so what it can report depends on what the image's wget prints.
# Each target is a small Go server built here; the agent runs from the
# profiler image with --pid=container:<target>, as in python-austin.sh:
#   - pprof registered: a profile is published
#   - pprof behind basic auth: the error says authentication is required
#   - no pprof handler: the error says net/http/pprof is not registered
#   - nothing listening: the error says the port could not be detected and
#     the default was tried
set -euo pipefail

image=${1:?usage: $0 <profiler-bpf-image>}
server=application-profiler-e2e-go-server
work=$(mktemp -d)
containers=()
cleanup() {
	if [ ${#containers[@]} -gt 0 ]; then docker rm -f "${containers[@]}" >/dev/null 2>&1 || true; fi
	rm -rf "$work"
}
trap cleanup EXIT

# server <open|auth|none> <addr>
cat >"$work/main.go" <<'GO'
package main

import (
	"log"
	"net/http"
	"net/http/pprof"
	"os"
)

func main() {
	mode, addr := os.Args[1], os.Args[2]

	profiles := http.NewServeMux()
	profiles.HandleFunc("/debug/pprof/", pprof.Index)
	profiles.HandleFunc("/debug/pprof/profile", pprof.Profile)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	switch mode {
	case "open":
		mux.Handle("/debug/pprof/", profiles)
	case "auth":
		mux.Handle("/debug/pprof/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if user, pass, ok := r.BasicAuth(); !ok || user != "user" || pass != "secret" {
				w.Header().Set("WWW-Authenticate", `Basic realm="pprof"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			profiles.ServeHTTP(w, r)
		}))
	}
	log.Fatal(http.ListenAndServe(addr, mux))
}
GO
cat >"$work/Dockerfile" <<'DOCKERFILE'
FROM golang:1.26.5-alpine AS build
WORKDIR /src
COPY main.go .
RUN go mod init example.com/server && CGO_ENABLED=0 go build -o /server .

FROM alpine:3.23.4
COPY --from=build /server /server
ENTRYPOINT ["/server"]
DOCKERFILE
docker build -q -t "$server" "$work" >/dev/null

# event <type> <field>: prints data.<field> of every agent event of that type.
event() {
	jq -rR --arg type "$1" --arg field "$2" 'fromjson? | select(.type == $type) | .data[$field]'
}

start_target() { # name args...
	local name=$1
	shift
	docker run -d --name "$name" "$@" >/dev/null
	containers+=("$name")
}

# profile <target>: prints the published file, or the agent's error reason on
# stderr and returns 1. The file is copied to $work/<target>.gz.
profile() {
	local agent logs file
	agent=$(docker run -d --privileged --pid="container:$1" --entrypoint /app/agent "$image" \
		--target-container-id e2e --target-container-runtime containerd \
		--target-container-runtime-path /run/containerd --pid 1 --lang go \
		--profiling-tool pprof --output-type pprof --duration 5s \
		--grace-period-ending 30s --compressor-type gzip)
	containers+=("$agent")
	for _ in $(seq 1 60); do
		docker logs "$agent" 2>&1 | grep -q '"stage":"ended"\|"type":"error"' && break
		sleep 1
	done
	logs=$(docker logs "$agent" 2>&1)
	if grep -q '"type":"error"' <<<"$logs"; then
		event error reason <<<"$logs" >&2
		return 1
	fi
	file=$(event result file <<<"$logs")
	if [ -z "$file" ]; then
		echo "neither a result nor an error within 60s" >&2
		return 1
	fi
	docker cp -q "$agent:$file" "$work/$1.gz"
	echo "$file"
}

start_target t-pprof-open "$server" open :6060
start_target t-pprof-auth "$server" auth :6060
start_target t-pprof-none "$server" none :9090
start_target t-no-listener alpine:3.23.4 sleep 100000
sleep 2

failed=0

# A pprof profile is itself gzipped protobuf, which the agent gzips again.
if file=$(profile t-pprof-open 2>"$work/err") && gzip -dc "$work/t-pprof-open.gz" | gzip -t; then
	echo "ok   t-pprof-open: published $file"
else
	echo "FAIL t-pprof-open: no profile: $(cat "$work/err")"
	failed=1
fi

expect_error() { # target want
	local reason
	if profile "$1" >/dev/null 2>"$work/err"; then
		echo "FAIL $1: profiled, want an error containing: $2"
		failed=1
		return
	fi
	reason=$(cat "$work/err")
	if [[ $reason == *"$2"* ]]; then
		echo "ok   $1: $reason"
	else
		echo "FAIL $1: error '$reason', want it to contain: $2"
		failed=1
	fi
}

expect_error t-pprof-auth "PID 1: failed to fetch CPU profile: the pprof endpoint on :6060 requires authentication"
expect_error t-pprof-none "PID 1: failed to fetch CPU profile: no /debug/pprof handler on :9090 (is net/http/pprof registered?)"
expect_error t-no-listener "could not detect the listening port (no listening port found for PID), so tried the default :8080: failed to fetch CPU profile: nothing listening on :8080"

exit "$failed"
