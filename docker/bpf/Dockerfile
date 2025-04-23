FROM golang:1.23.4-alpine AS agentbuild
WORKDIR /go/src/github.com/josepdcs/kubectl-prof
COPY . .
RUN go get -d -v ./...
WORKDIR /go/src/github.com/josepdcs/kubectl-prof/cmd/agent
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /go/bin/agent
RUN go install github.com/google/pprof@latest

FROM alpine:3.21.0 AS builder
RUN apk add --no-cache git && git clone https://github.com/brendangregg/FlameGraph

FROM alpine:3.21.0
RUN apk add --no-cache bash bcc-tools perl procps strace util-linux iproute2 graphviz
RUN mkdir -p /app/FlameGraph
COPY --from=builder /FlameGraph /app/FlameGraph
COPY --from=agentbuild /go/bin/agent /app
COPY --from=agentbuild /go/bin/pprof /usr/local/bin/pprof
COPY --from=agentbuild /go/src/github.com/josepdcs/kubectl-prof/contrib/bpf/profile.py /app/bcc-profiler/profile
RUN chmod +x /app/bcc-profiler/profile
COPY --from=agentbuild /go/src/github.com/josepdcs/kubectl-prof/contrib/get-ps-command.sh /app/get-ps-command.sh
RUN chmod +x /app/get-ps-command.sh
CMD ["/app/agent"]
