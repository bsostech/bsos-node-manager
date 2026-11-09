.PHONY: build run generate clean

build:
	go build -o bin/agent ./cmd/agent

# For deployment: static Linux binaries.
# File names match the `besu-agent-linux-{arch}` that install.sh fetches; point the platform's AGENT_BINARY_DIR at bin/.
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/besu-agent-linux-amd64 ./cmd/agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/besu-agent-linux-arm64 ./cmd/agent

run:
	go run ./cmd/agent

# Regenerate models from contract/openapi.yaml (the single source of truth)
generate:
	go tool oapi-codegen -config oapi-codegen.yaml ../contract/openapi.yaml

clean:
	rm -rf bin
