FROM golang:1.26.5-alpine AS agentbuild
ENV GOTOOLCHAIN=local
WORKDIR /go/src/github.com/nudgebee/application-profiler
COPY . /go/src/github.com/nudgebee/application-profiler
RUN go get -d -v ./...
WORKDIR /go/src/github.com/nudgebee/application-profiler/cmd/agent
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /go/bin/agent

FROM alpine:3.21.0
RUN apk add --no-cache bash coreutils procps strace &&  \
    mkdir -p /app
COPY --from=agentbuild /go/bin/agent /app

CMD [ "/app/agent" ]