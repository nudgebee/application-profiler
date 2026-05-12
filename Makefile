VERSION ?= v0.1.0-dev
AGENT_NAME ?= agent
AGENT_DIR ?= ./cmd/agent/
BUILD_DIR ?= bin
REGISTRY ?= ghcr.io
DOCKER_BASE_IMAGE ?= nudgebee/application-profiler
DOCKER_JVM_IMAGE ?= $(DOCKER_BASE_IMAGE)-jvm:$(VERSION)
DOCKERFILE_JVM ?= ./docker/jvm/Dockerfile
DOCKER_JVM_ALPINE_IMAGE ?= $(DOCKER_BASE_IMAGE)-jvm-alpine:$(VERSION)
DOCKERFILE_JVM_ALPINE ?= ./docker/jvm/alpine/Dockerfile
DOCKER_BPF_IMAGE ?= $(DOCKER_BASE_IMAGE)-bpf:$(VERSION)
DOCKERFILE_BPF ?= ./docker/bpf/Dockerfile
DOCKER_PERF_IMAGE ?= $(DOCKER_BASE_IMAGE)-perf:$(VERSION)
DOCKERFILE_PERF ?= ./docker/perf/Dockerfile
DOCKER_PYTHON_IMAGE ?= $(DOCKER_BASE_IMAGE)-python:$(VERSION)
DOCKERFILE_PYTHON ?= ./docker/python/Dockerfile
DOCKER_RUBY_IMAGE ?= $(DOCKER_BASE_IMAGE)-ruby:$(VERSION)
DOCKERFILE_RUBY ?= ./docker/ruby/Dockerfile
DOCKER_DUMMY_IMAGE ?= $(DOCKER_BASE_IMAGE)-dummy:$(VERSION)
DOCKERFILE_DUMMY ?= ./docker/dummy/Dockerfile
DOCKER_TARGET_PLATFORM ?= linux/amd64,linux/arm64
DOCKER_BUILD_ADDITIONAL_ARGS ?=

M = $(shell printf "\033[34;1m▶\033[0m")

## all: Build the agent binary and push all docker images
.PHONY: all
all: build-agent push-docker-jvm push-docker-jvm-alpine push-docker-bpf push-docker-perf push-docker-python push-docker-ruby

## build: Build the agent binary
.PHONY: build
build: build-agent

## build-docker-agents: Build all the docker images
.PHONY: build-docker-agents
build-docker-agents: build-docker-bpf build-docker-jvm build-docker-jvm-alpine build-docker-perf build-docker-python build-docker-ruby

## install-deps: install dependencies if needed
.PHONY: install-deps
install-deps:
	$(info $(M) getting dependencies...)
	@go get -v ./...

## upgrade-deps: upgrade dependencies if needed
.PHONY: upgrade-deps
upgrade-deps:
	$(info $(M) upgrading dependencies...)
	@go get -t -u ./...

## build-agent: Build the agent binary
.PHONY: build-agent
build-agent: install-deps
	$(info $(M) building agent...)
	@go build -o $(BUILD_DIR)/$(AGENT_NAME) -v $(AGENT_DIR)

## build-docker-jvm: Build the JVM docker image
.PHONY: build-docker-jvm
build-docker-jvm:
	$(info $(M) building JVM docker image...)
	@docker buildx build ${DOCKER_BUILD_ADDITIONAL_ARGS} --progress plain --platform=${DOCKER_TARGET_PLATFORM} -t ${REGISTRY}/${DOCKER_JVM_IMAGE} --label git-commit=$(shell git rev-parse HEAD) -f $(DOCKERFILE_JVM) .

## push-docker-jvm: Build and push the JVM docker image
.PHONY: push-docker-jvm
push-docker-jvm: build-docker-jvm
	$(info $(M) pushing JVM docker image...)
	@docker push $(REGISTRY)/$(DOCKER_JVM_IMAGE)

## build-docker-jvm-alpine: Build the JVM Alpine docker image
.PHONY: build-docker-jvm-alpine
build-docker-jvm-alpine:
	$(info $(M) building JVM Alpine docker image...)
	@docker buildx build ${DOCKER_BUILD_ADDITIONAL_ARGS} --progress plain --platform=${DOCKER_TARGET_PLATFORM} -t ${REGISTRY}/${DOCKER_JVM_ALPINE_IMAGE} --label git-commit=$(shell git rev-parse HEAD) -f $(DOCKERFILE_JVM_ALPINE) .

## push-docker-jvm-alpine: Build and push the JVM Alpine docker image
.PHONY: push-docker-jvm-alpine
push-docker-jvm-alpine: build-docker-jvm-alpine
	$(info $(M) pushing JVM Alpine docker image...)
	@docker push $(REGISTRY)/$(DOCKER_JVM_ALPINE_IMAGE)

## build-docker-bpf: Build the BPF docker image
.PHONY: build-docker-bpf
build-docker-bpf:
	$(info $(M) building BPF docker image...)
	docker buildx build ${DOCKER_BUILD_ADDITIONAL_ARGS} --platform=${DOCKER_TARGET_PLATFORM} -t ${REGISTRY}/${DOCKER_BPF_IMAGE} --label git-commit=$(shell git rev-parse HEAD) -f $(DOCKERFILE_BPF) .

## push-docker-bpf: Build and push the BPF docker image
.PHONY: push-docker-bpf
push-docker-bpf: build-docker-bpf
	$(info $(M) pushing BPF docker image...)
	@docker push $(REGISTRY)/$(DOCKER_BPF_IMAGE)

## build-docker-perf: Build the PERF docker image
.PHONY: build-docker-perf
build-docker-perf:
	$(info $(M) building PERF docker image...)
	docker buildx build ${DOCKER_BUILD_ADDITIONAL_ARGS} --platform=${DOCKER_TARGET_PLATFORM} --no-cache -t ${REGISTRY}/${DOCKER_PERF_IMAGE} --label git-commit=$(shell git rev-parse HEAD) -f $(DOCKERFILE_PERF) .

## push-docker-perf: Build and push the PERF docker image
.PHONY: push-docker-perf
push-docker-perf: build-docker-perf
	$(info $(M) pushing PERF docker image...)
	@docker push $(REGISTRY)/$(DOCKER_PERF_IMAGE)

## build-docker-python: Build the PYTHON docker image
.PHONY: build-docker-python
build-docker-python:
	$(info $(M) building PYTHON docker image...)
	docker buildx build ${DOCKER_BUILD_ADDITIONAL_ARGS} --platform=${DOCKER_TARGET_PLATFORM} -t ${REGISTRY}/${DOCKER_PYTHON_IMAGE} --label git-commit=$(shell git rev-parse HEAD) -f $(DOCKERFILE_PYTHON) .

## push-docker-python: Build and push the PYTHON docker image
.PHONY: push-docker-python
push-docker-python: build-docker-python
	$(info $(M) pushing PYTHON docker image...)
	@docker push $(REGISTRY)/$(DOCKER_PYTHON_IMAGE)

## build-docker-ruby: Build the RUBY docker image
.PHONY: build-docker-ruby
build-docker-ruby:
	$(info $(M) building RUBY docker image...)
	docker buildx build ${DOCKER_BUILD_ADDITIONAL_ARGS} --platform=${DOCKER_TARGET_PLATFORM} -t ${REGISTRY}/${DOCKER_RUBY_IMAGE} --label git-commit=$(shell git rev-parse HEAD) -f $(DOCKERFILE_RUBY) .

## push-docker-ruby: Build and push the RUBY docker image
.PHONY: push-docker-ruby
push-docker-ruby: build-docker-ruby
	$(info $(M) pushing RUBY docker image...)
	@docker push $(REGISTRY)/$(DOCKER_RUBY_IMAGE)

## build-docker-dummy: Build the DUMMY docker image (test only)
.PHONY: build-docker-dummy
build-docker-dummy:
	$(info $(M) building DUMMY docker image...)
	docker buildx build ${DOCKER_BUILD_ADDITIONAL_ARGS} --platform=${DOCKER_TARGET_PLATFORM} -t ${REGISTRY}/${DOCKER_DUMMY_IMAGE} --label git-commit=$(shell git rev-parse HEAD) -f $(DOCKERFILE_DUMMY) .

## push-docker-all: Build and push all production docker images
.PHONY: push-docker-all
push-docker-all: push-docker-jvm push-docker-jvm-alpine push-docker-bpf push-docker-perf push-docker-python push-docker-ruby

## test: Run unit tests
.PHONY: test
test:
	$(info $(M) running tests...)
	GOARCH=amd64 GOOS=linux go test -p 1 ./... -coverprofile=coverage.out

## coverage: Run unit tests and show coverage
.PHONY: coverage
coverage: test
	$(info $(M) running tests and coverage...)
	GOARCH=amd64 GOOS=linux go tool cover -html=coverage.out && unlink coverage.out

## vet: Run go vet
.PHONY: vet
vet:
	$(info $(M) running go vet...)
	@go vet ./...

## check: Check the code
.PHONY: check
check: vet
	$(info $(M) checking code...)

## clean: Clean build artifacts
.PHONY: clean
clean:
	$(info $(M) cleaning all..)
	@rm -f coverage.out
	@rm -rf $(BUILD_DIR)
	@go clean

## version: Show the project version
.PHONY: version
version:
	@echo $(VERSION)

## help: This message
.PHONY: help
help: Makefile
	@echo
	@echo " Choose a command:"
	@echo
	@sed -n 's/^##//p' $< | column -t -s ':' |  sed -e 's/^/ /'
	@echo
