FROM golang:1.26.3-alpine AS agentbuild
WORKDIR /go/src/github.com/nudgebee/application-profiler
COPY . /go/src/github.com/nudgebee/application-profiler
RUN go get -d -v ./...
WORKDIR /go/src/github.com/nudgebee/application-profiler/cmd/agent
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /go/bin/agent

FROM python:3.13.6-slim-bullseye AS pyspybuild
RUN pip3 install py-spy==0.4.0  \
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