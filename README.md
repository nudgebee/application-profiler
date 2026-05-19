# application-profiler

Low-overhead, on-demand profiling agent for Kubernetes workloads. Generates
flamegraphs, JFR recordings, heap dumps, thread dumps, and other profiling
output for Java, Python, Go, Node.js, Ruby, Rust, C/C++ applications — without
requiring restarts or modifications to the target pods.

This repository ships the **agent** binary and the per-language container
images that run alongside a workload to collect profiling data. The agent is
invoked by an orchestrator (e.g. [Nudgebee](https://github.com/nudgebee/nudgebee))
that handles pod placement, log streaming, and result delivery.

This project is an open-source fork of
[josepdcs/kubectl-prof](https://github.com/josepdcs/kubectl-prof), which itself
is a fork of [yahoo/kubectl-flame](https://github.com/yahoo/kubectl-flame). The
kubectl CLI plugin has been removed from this fork — only the agent and images
are maintained here.

## Table of Contents

- [Architecture](#architecture)
- [Supported languages and tools](#supported-languages-and-tools)
- [Container images](#container-images)
- [Agent usage](#agent-usage)
- [Building from source](#building-from-source)
- [Contributing](#contributing)
- [License](#license)

## Architecture

The agent runs as a privileged pod scheduled on the same node as the target
workload. It enters the target container's namespaces, runs the configured
profiling tool (`async-profiler`, `jcmd`, `py-spy`, `bpf`, `perf`, `rbspy`,
`pprof`, or `austin`), and emits structured JSON log lines that the
orchestrator consumes:

```
{"type": "progress", "data": {"stage": "started"}}
{"type": "result",   "data": {"result-type": "flamegraph", "file": "...", ...}}
{"type": "progress", "data": {"stage": "ended"}}
```

The orchestrator (Nudgebee, or your own controller) is responsible for:

- Discovering the target pod's container runtime and ID
- Creating the agent pod with the right image, volumes, and `SYS_ADMIN`
  capability
- Streaming agent logs and parsing the result envelope
- Copying the result file out of the agent pod

## Supported languages and tools

| Language          | Default tool   | Outputs                                                |
| ----------------- | -------------- | ------------------------------------------------------ |
| Java / JVM        | async-profiler | flamegraph, jfr, threaddump, heapdump, heaphistogram   |
| Python            | py-spy         | flamegraph, raw, threaddump, speedscope                |
| Go                | pprof          | pprof, heapdump, raw                                   |
| Ruby              | rbspy          | flamegraph                                             |
| Node.js           | perf           | flamegraph, heapdump                                   |
| Rust / C / C++    | bpf, perf      | flamegraph                                             |

Supported container runtimes: containerd, CRI-O.

## Container images

Five production images are published to GitHub Container Registry on every
push to `main` and on every `v*` tag:

| Image                                                      | Used for                |
| ---------------------------------------------------------- | ----------------------- |
| `ghcr.io/nudgebee/application-profiler-jvm:<tag>`          | Java workloads          |
| `ghcr.io/nudgebee/application-profiler-bpf:<tag>`          | BPF-based profiling     |
| `ghcr.io/nudgebee/application-profiler-perf:<tag>`         | Node.js, Rust, C/C++    |
| `ghcr.io/nudgebee/application-profiler-python:<tag>`       | Python workloads        |
| `ghcr.io/nudgebee/application-profiler-ruby:<tag>`         | Ruby workloads          |

Available tags:

- `latest` — most recent `main` build
- `vX.Y.Z`, `vX.Y` — release tags
- `<short-sha>` — every commit on `main`

## Agent usage

The agent is intended to be invoked by an orchestrator, but its CLI flags are
stable and can be used directly for testing. The container's entrypoint is
`/app/agent`:

```
/app/agent \
  --target-container-runtime containerd \
  --target-container-runtime-path /run/containerd \
  --target-pod-uid <pod-uid> \
  --target-container-id <container-id> \
  --lang java \
  --profiling-tool async-profiler \
  --output-type flamegraph \
  --duration 30s \
  --grace-period-ending 600s \
  --compressor-type gzip
```

Run `/app/agent --help` for the full flag list.

## Building from source

Requires Go 1.26+, Docker with Buildx.

```bash
make build-agent           # builds bin/agent for the host platform
make test                  # runs unit tests (GOARCH=amd64 GOOS=linux)
make build-docker-agents   # builds all language images locally
```

Image registry and tag can be overridden:

```bash
REGISTRY=ghcr.io VERSION=v0.1.0 make push-docker-all
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Bug reports, feature requests, and PRs
are welcome. For security issues, please use
[GitHub Security Advisories](../../security/advisories/new).

## License

Apache License 2.0 — see [LICENSE](LICENSE).
