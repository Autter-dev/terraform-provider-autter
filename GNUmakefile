default: build

build:
	go build -v ./...

install: build
	go install -v ./...

lint:
	golangci-lint run

fmt:
	gofmt -s -w -e .

generate:
	go generate ./...

test:
	go test ./... -count=1 -timeout 5m

# Acceptance tests run against the in-memory mock API; they need Terraform on PATH, no secrets.
testacc:
	TF_ACC=1 go test ./... -count=1 -v -timeout 20m

# Copy the OpenAPI document and the export fixtures from a monorepo checkout: make sync MONOREPO=../autter-monorepo
sync:
	scripts/sync-from-monorepo.sh $(MONOREPO)

.PHONY: default build install lint fmt generate test testacc sync
