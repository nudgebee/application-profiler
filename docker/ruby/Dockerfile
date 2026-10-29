FROM golang:1.26.10-bookworm AS agentbuild
ENV GOTOOLCHAIN=local
WORKDIR /go/src/github.com/nudgebee/application-profiler
COPY . /go/src/github.com/nudgebee/application-profiler
RUN go get -d -v ./...
WORKDIR /go/src/github.com/nudgebee/application-profiler/cmd/agent
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /go/bin/agent

FROM rust:1.98-bookworm AS rbspybuild
WORKDIR /
RUN git clone https://github.com/brendangregg/FlameGraph  \
    && git clone --depth 1 --branch v0.27.0 https://github.com/rbspy/rbspy
WORKDIR /rbspy
RUN cargo build --release

# trixie, not bookworm: bookworm leaves zlib CVE-2023-45853 unfixed. rbspy and
# the agent are built against bookworm's older glibc, which runs on trixie's.
FROM bitnami/minideb:trixie
RUN apt-get update  \
    && apt-get upgrade -y  \
    && apt-get install -y procps strace  \
    && apt-get clean \
    && mkdir -p /app/FlameGraph
COPY --from=rbspybuild /FlameGraph /app/FlameGraph
COPY --from=agentbuild /go/bin/agent /app/agent
COPY --from=rbspybuild /rbspy/target/release/rbspy /app/rbspy
COPY --from=agentbuild /go/src/github.com/nudgebee/application-profiler/contrib/get-ps-command.sh /app/get-ps-command.sh
RUN chmod +x /app/get-ps-command.sh

CMD [ "/app/agent" ]
