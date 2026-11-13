SHELL := /bin/bash

IMAGE        ?= profile

# Image tag: the short commit SHA; with uncommitted changes (tracked or untracked), plus a hash
# of those changes. A rebuild from a changed tree gets a new tag, so `helm upgrade` rolls it out
# (with the same tag it would be a no-op and the pod would keep the old image); the same tree
# always gets the same tag, so separate `make image` and `make deploy` runs agree.
GIT_SHA     := $(shell git rev-parse --short HEAD)
GIT_CHANGES := $(shell git status --porcelain)
GIT_DIFF_ID := $(shell { git diff HEAD; git ls-files --others --exclude-standard -z | xargs -0 git hash-object 2>/dev/null; } | git hash-object --stdin | cut -c1-7)
TAG          ?= $(GIT_SHA)$(if $(GIT_CHANGES),-dirty-$(GIT_DIFF_ID))
RELEASE      ?= profile
NAMESPACE    ?= apps
KIND_CLUSTER ?= distro
CHART        := deploy/chart

# Built with the local Go toolchain: a prebuilt golangci-lint must be built with
# Go >= the go.mod version, so `go run` a pinned release instead.
GOLANGCI_LINT_VERSION ?= v2.15.0

# `make run`: the compose PostgreSQL from distrocommerce-infra (local dev
# credentials, the same as its compose/.env.example). Override as needed.
LOCAL_DATABASE_URL ?= postgres://profile:profile_dev@localhost:5432/profile?sslmode=disable
# Not 8080: kind holds 127.0.0.1:8080 for Envoy, and a local :8080 listener
# would take over localhost (::1) requests meant for the cluster.
LOCAL_PORT ?= 8081

.PHONY: build test lint run image kind-load deploy undeploy

build: ## binary in bin/
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(TAG)" -o bin/profile-service ./cmd/profile-service

test: ## vet + unit tests (set PROFILE_TEST_DATABASE_URL to include the store test)
	go vet ./...
	go test -race ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

run: ## locally on :8081, against localhost:5432, telemetry off
	PORT=$(LOCAL_PORT) DATABASE_URL='$(LOCAL_DATABASE_URL)' LOG_LEVEL=debug OTEL_SDK_DISABLED=true go run ./cmd/profile-service

image: ## for the host platform (linux/arm64 on Apple silicon)
	docker build --build-arg VERSION=$(TAG) -t $(IMAGE):$(TAG) .

kind-load:
	kind load docker-image $(IMAGE):$(TAG) --name $(KIND_CLUSTER)

deploy: ## needs PROJECT_ID (the ZITADEL project ID): make deploy PROJECT_ID=...
	@test -n "$(PROJECT_ID)" || { echo "PROJECT_ID is required (the ZITADEL project ID)"; exit 1; }
	helm dependency update $(CHART)
	helm upgrade --install $(RELEASE) $(CHART) -n $(NAMESPACE) \
	  -f $(CHART)/values-local.yaml \
	  --set image.tag=$(TAG) \
	  --set-string 'auth.audiences={$(PROJECT_ID)}' \
	  --wait --timeout 3m

undeploy:
	helm uninstall $(RELEASE) -n $(NAMESPACE)
