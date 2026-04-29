.PHONY: help dev db-up db-down db-reset run build vet test lint compose-up compose-down compose-build push image clean

# ----------------------------------------------------------------------
# A Makefile for the impatient. Run `make help` to see what's here.
# Targets are split into "stack" (compose-driven, what users want) and
# "dev" (single-component, what contributors want).
# ----------------------------------------------------------------------

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# ----- Stack (docker compose) -----

compose-up: ## Bring the full stack up (postgres + synapse + dashboard)
	docker compose up -d

compose-down: ## Stop the stack (keep volumes)
	docker compose down

compose-build: ## Rebuild stack images
	docker compose build

compose-logs: ## Tail logs for all services
	docker compose logs -f

# ----- Dev (synapse on host, postgres in docker) -----

dev: db-up ## Run synapse on the host with postgres in docker
	cd synapse && go run ./cmd/server

db-up: ## Start postgres only
	docker compose up -d postgres

db-down: ## Stop postgres
	docker compose stop postgres

db-reset: ## TRUNCATE all tables (keeps schema). Synapse must be stopped or restarted.
	PGPASSWORD=$${POSTGRES_PASSWORD:-synapse} psql -h localhost -U $${POSTGRES_USER:-synapse} -d $${POSTGRES_DB:-synapse} -c \
	  "TRUNCATE users, teams, projects, team_members, deployments, project_env_vars, team_invites, deploy_keys, access_tokens, audit_events RESTART IDENTITY;"

run: ## Run synapse (assumes postgres is up)
	cd synapse && go run ./cmd/server

build: ## Build the synapse binary into synapse/bin/synapse
	cd synapse && go build -o bin/synapse ./cmd/server

vet: ## go vet ./...
	cd synapse && go vet ./...

test: ## go test ./...
	cd synapse && go test ./...

lint: vet ## Alias for vet (no separate linter configured yet)

# ----- Provisioned-deployment cleanup -----

prune-deployments: ## Force-remove every Synapse-managed Convex container + volume
	-docker rm -f $$(docker ps -aq --filter label=synapse.managed=true) 2>/dev/null
	-docker volume ls -q --filter name=synapse-data- | xargs -r docker volume rm

# ----- Image / release -----

image: ## Build the synapse Docker image (tag synapse:dev)
	docker build -t synapse:dev ./synapse

clean: ## Remove build artifacts (binary)
	rm -rf synapse/bin/
