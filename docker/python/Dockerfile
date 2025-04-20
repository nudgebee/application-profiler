<<<<<<< HEAD
FROM  golang:1.23-alpine AS agentbuild
=======
FROM golang:1.23.4-alpine AS agentbuild
>>>>>>> upstream/main
WORKDIR /go/src/github.com/josepdcs/kubectl-prof
COPY . /go/src/github.com/josepdcs/kubectl-prof
RUN go get -d -v ./...
WORKDIR /go/src/github.com/josepdcs/kubectl-prof/cmd/agent
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /go/bin/agent

<<<<<<< HEAD
FROM python:3.11.2-slim-bullseye AS pyspybuild
RUN pip3 install py-spy==0.4.0
RUN apt-get update && apt-get install -y git
RUN git clone https://github.com/brendangregg/FlameGraph

FROM python:3.11.2-alpine
RUN apk add --update --no-cache perl procps strace
RUN mkdir -p /app/FlameGraph
RUN pip3 install austin-dist==3.7.0
=======
FROM python:3.13.1-slim-bullseye AS pyspybuild
RUN pip3 install py-spy==0.4.0  \
    && apt-get update && apt-get install -y git \
    && git clone https://github.com/brendangregg/FlameGraph

FROM python:3.13.1-alpine
RUN apk add --update --no-cache perl procps strace  \
    && mkdir -p /app/FlameGraph
>>>>>>> upstream/main
COPY --from=pyspybuild /FlameGraph /app/FlameGraph
COPY --from=agentbuild /go/bin/agent /app/agent
COPY --from=pyspybuild /usr/local/bin/py-spy /app/py-spy
COPY --from=agentbuild /go/src/github.com/josepdcs/kubectl-prof/contrib/get-ps-command.sh /app/get-ps-command.sh
RUN chmod +x /app/get-ps-command.sh

CMD [ "/app/agent" ]