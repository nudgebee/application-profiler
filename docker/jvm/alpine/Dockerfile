ARG JAVA_VERSION_TAG=21
ARG ASYNCP_VERSION=2.9
FROM golang:1.26.5-alpine AS agentbuild
ENV GOTOOLCHAIN=local
WORKDIR /go/src/github.com/nudgebee/application-profiler
COPY . /go/src/github.com/nudgebee/application-profiler
RUN go get -d -v ./...
WORKDIR /go/src/github.com/nudgebee/application-profiler/cmd/agent
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /go/bin/agent

# JDK 17, not a current one: async-profiler 2.9's Makefile compiles its Java
# helper with a hardcoded `-source 7 -target 7`, which javac rejects from JDK 20
# onwards ("Source option 7 is no longer supported"). On JDK 26 that killed the
# very first make target, leaving build/ empty.
FROM amazoncorretto:17-alpine-jdk AS async-profiler-builder
ARG TARGETPLATFORM
ARG ASYNCP_VERSION=2.9
RUN apk add --no-cache curl g++ git linux-headers make musl-dev tar wget
WORKDIR /src
RUN git clone --branch v${ASYNCP_VERSION} --depth 1 https://github.com/async-profiler/async-profiler
WORKDIR /src/async-profiler
# `all` only — `native` is upstream's release-packaging target and expects the
# published per-arch tarballs to be sitting in the source tree, so it always
# failed here. The trailing `;` that used to swallow that failure also swallowed
# a broken `all`, which is how an empty build/ got published. Assert instead:
# every artefact profiler.sh needs at runtime must exist before we ship a layer.
RUN make CXXFLAGS='-O3 -fno-omit-frame-pointer -fvisibility=hidden -D_LARGEFILE64_SOURCE' all \
    && test -f build/libasyncProfiler.so \
    && test -x build/jattach \
    && test -x build/fdtransfer

# Second build of the same source against glibc. libasyncProfiler.so is
# dlopen'd by the *target* JVM, not by anything in this image, so it has to
# match the target's libc: a musl .so in a temurin/corretto/distroless JVM
# fails the attach with "libc.musl-x86_64.so.1: cannot open shared object
# file". jattach and fdtransfer stay musl — those run here, in this alpine
# image. Same JDK 17 pin, same reason as above.
FROM amazoncorretto:17 AS async-profiler-builder-glibc
ARG TARGETPLATFORM
ARG ASYNCP_VERSION=2.9
RUN yum install -y gcc-c++ git make tar
WORKDIR /src
RUN git clone --branch v${ASYNCP_VERSION} --depth 1 https://github.com/async-profiler/async-profiler
WORKDIR /src/async-profiler
RUN make CXXFLAGS='-O3 -fno-omit-frame-pointer -fvisibility=hidden -D_LARGEFILE64_SOURCE' all \
    && test -f build/libasyncProfiler.so

FROM eclipse-temurin:$JAVA_VERSION_TAG-alpine AS tools
ARG TARGETPLATFORM
ARG ASYNCP_VERSION=2.9
RUN apk add --no-cache curl g++ git linux-headers make tar wget  \
    && echo "ASYNCP_VERSION=${ASYNCP_VERSION}, TARGETPLATFORM=${TARGETPLATFORM}" \
    && "$JAVA_HOME"/bin/jlink \
         --module-path jmods \
         --add-modules jdk.jcmd \
         --strip-debug \
         --no-man-pages \
         --no-header-files \
         --compress=2 \
         --output /jcmd

FROM alpine:3.23.4
RUN mkdir -p /app/async-profiler/build  \
    && apk add --no-cache procps coreutils
COPY --from=agentbuild /go/bin/agent /app
COPY --from=agentbuild /go/src/github.com/nudgebee/application-profiler/contrib/jvm/profiler.sh /app/async-profiler
COPY --from=async-profiler-builder /src/async-profiler/build /app/async-profiler/build
# Ship both libc builds of the injected library. The default name — the one
# profiler.sh hands to the JVM — is the glibc build, because that is what most
# JVM images are; the agent swaps the musl one in when the target's rootfs is
# musl (internal/agent/profiler/jvm/async_profiler.go).
RUN mv /app/async-profiler/build/libasyncProfiler.so /app/async-profiler/build/libasyncProfiler-musl.so
COPY --from=async-profiler-builder-glibc /src/async-profiler/build/libasyncProfiler.so /app/async-profiler/build/libasyncProfiler.so
COPY --from=tools /jcmd /opt/jdk
# COPY of a directory is happy to copy nothing, so re-assert in the final stage:
# a shipped image without these fails at profile time, not at build time.
RUN test -f /app/async-profiler/build/libasyncProfiler.so \
    && test -f /app/async-profiler/build/libasyncProfiler-musl.so \
    && test -x /app/async-profiler/build/jattach \
    && test -x /app/async-profiler/build/fdtransfer
RUN chmod +x /app/async-profiler/profiler.sh  \
    && mkdir -p /app/jfr/settings
COPY --from=agentbuild /go/src/github.com/nudgebee/application-profiler/contrib/jvm/jfr-profile.jfc /app/jfr/settings
COPY --from=agentbuild /go/src/github.com/nudgebee/application-profiler/contrib/get-ps-command.sh /app/get-ps-command.sh
RUN chmod +x /app/get-ps-command.sh

CMD [ "/app/agent" ]