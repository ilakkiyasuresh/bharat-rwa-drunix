.DEFAULT_GOAL := help
CC := chaincode/dr
export GOFLAGS := -mod=mod

.PHONY: help demo test build vet fmt check clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

demo: ## Run the full narrative and print the transaction log
	@cd $(CC) && go run ./cmd/demo

test: ## Run the domain test suite
	@cd $(CC) && go test ./internal/...

vet: ## Vet the domain packages
	@cd $(CC) && go vet ./internal/... ./cmd/...

fmt: ## Format
	@cd $(CC) && gofmt -w .

build: ## Build the chaincode binary (needs the Fabric modules)
	@cd $(CC) && go mod download && go build -o dr .

check: vet test demo ## Vet, test, then run the demo

clean:
	@rm -f $(CC)/dr
