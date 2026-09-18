FROM golang:1.26.5-alpine AS agentbuild
ENV GOTOOLCHAIN=local
WORKDIR /go/src/github.com/nudgebee/application-profiler
COPY . /go/src/github.com/nudgebee/application-profiler
RUN go get -d -v ./...
WORKDIR /go/src/github.com/nudgebee/application-profiler/cmd/agent
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /go/bin/agent

# bookworm, not bullseye: Debian 11 is past EOL and its security suite has
# moved to archive, so `apt-get install git` here 404s — this stage no longer
# builds at all on bullseye.
FROM python:3.13-slim-bookworm AS pyspybuild
# 0.4.2 is the first release that understands CPython 3.14. On 0.4.0 a 3.14
# target fails with the misleading "No python processes found in process <pid>".
RUN pip3 install py-spy==0.4.2  \
    && apt-get update && apt-get install -y git \
    && git clone https://github.com/brendangregg/FlameGraph

FROM python:3.14.0-alpine
RUN apk add --update --no-cache perl procps strace  \
    && mkdir -p /app/FlameGraph
COPY --from=pyspybuild /FlameGraph /app/FlameGraph
COPY --from=agentbuild /go/bin/agent /app/agent
COPY --from=pyspybuild /usr/local/bin/py-spy /app/py-spy
COPY --from=agentbuild /go/src/github.com/nudgebee/application-profiler/contrib/get-ps-command.sh /app/get-ps-command.sh
RUN pip3 install austin-dist==3.7.0
RUN chmod +x /app/get-ps-command.sh

CMD [ "/app/agent" ]