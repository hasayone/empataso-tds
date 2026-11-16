.PHONY: build run test bench lint vuln infra-up infra-down tidy mcp

build:
	go build -o bin/emtds-engine ./cmd/engine
	go build -o bin/emtds-admin  ./cmd/admin
	go build -o bin/emtds-cron   ./cmd/cron
	go build -o bin/emtds-apiclient ./cmd/apiclient

# MCP server binaries for linux, windows and darwin (amd64, arm64) into dist/mcp
mcp:
	docker build --target mcp-out --output type=local,dest=dist/mcp .

run:
	go run ./cmd/engine

test:
	go test ./...

bench:
	go test ./internal/ipindex/ ./internal/geo/ -run=^$$ -bench=. -benchmem

lint:
	golangci-lint run

vuln:
	govulncheck ./...

tidy:
	go mod tidy

# Local ClickHouse + Redis
infra-up:
	docker compose -f deploy/docker-compose.yml up -d

infra-down:
	docker compose -f deploy/docker-compose.yml down
