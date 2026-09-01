GOLANGCI_LINT := .bin/golangci-lint
GOLANGCI_LINT_VERSION := v2.13.2
FRONTEND_DIST := internal/web/assets/dist

.PHONY: all build test lint frontend tools clean

all: lint test build

## build: compile the frontend, then the single Go binary embedding it
build: frontend
	go build -o .bin/westbridge ./cmd/westbridge

## frontend: install deps if missing, then produce the Vite build
frontend:
	cd frontend && [ -d node_modules ] || npm ci
	cd frontend && npm run build
	# emptyOutDir wipes the placeholder that keeps go:embed happy
	touch $(FRONTEND_DIST)/.gitkeep

## test: Go unit tests
test:
	go test ./...

## lint: Go linters plus a TypeScript type check
lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...
	cd frontend && [ -d node_modules ] || npm ci
	cd frontend && npx tsc --noEmit -p tsconfig.app.json

## tools: install pinned developer tooling into .bin/
tools: $(GOLANGCI_LINT)

$(GOLANGCI_LINT):
	GOBIN=$(CURDIR)/.bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

clean:
	rm -rf .bin $(FRONTEND_DIST)/assets $(FRONTEND_DIST)/index.html
