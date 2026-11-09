.PHONY: build run generate db-up db-down clean

build:
	go build -o bin/server ./cmd/server

run:
	go run ./cmd/server

# Regenerate Go types and the chi server from contract/openapi.yaml (the single source of truth)
generate:
	go tool oapi-codegen -config oapi-codegen.yaml ../contract/openapi.yaml
	# Enterprise contract (absent in the open-source edition; skipped)
	[ ! -f ../contract/enterprise.yaml ] || go tool oapi-codegen -config oapi-codegen-enterprise.yaml ../contract/enterprise.yaml

db-up:
	docker compose up -d postgres

db-down:
	docker compose down

clean:
	rm -rf bin
