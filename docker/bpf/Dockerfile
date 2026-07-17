FROM golang:1.26.5-alpine AS agentbuild
ENV GOTOOLCHAIN=local
WORKDIR /go/src/github.com/nudgebee/application-profiler
COPY . /go/src/github.com/nudgebee/application-profiler
RUN go get -d -v ./...
WORKDIR /go/src/github.com/nudgebee/application-profiler/cmd/agent
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /go/bin/agent

FROM alpine:3.24.1 AS builder
RUN apk add --no-cache git  \
    && git clone https://github.com/brendangregg/FlameGraph

FROM alpine:3.24.1
# apk upgrade pulls the latest 3.23 patch level for the base + dependency
# packages (libssl3/libcrypto3 3.5.6-r0 -> 3.5.7-r0, libexpat, libxml2), which
# the pinned alpine:3.23.4 snapshot otherwise ships stale.
RUN apk update \
    && apk upgrade \
    && apk add bash bcc-tools perl procps strace util-linux iproute2 \
    && rm -rf /var/cache/apk/* \
    && mkdir -p /app/FlameGraph
COPY --from=builder /FlameGraph /app/FlameGraph
COPY --from=agentbuild /go/bin/agent /app
COPY --from=agentbuild /go/src/github.com/nudgebee/application-profiler/contrib/bpf/profile.py /app/bcc-profiler/profile
RUN chmod +x /app/bcc-profiler/profile
COPY --from=agentbuild /go/src/github.com/nudgebee/application-profiler/contrib/get-ps-command.sh /app/get-ps-command.sh
RUN chmod +x /app/get-ps-command.sh

CMD [ "/app/agent" ]