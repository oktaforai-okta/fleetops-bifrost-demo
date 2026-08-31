# Fleet Ops demo. Everything runs on one host; there is no cloud dependency.
#
# The plugin lives in a sibling repo because it is reusable and this demo is not. Clone
# both next to each other:
#
#   git clone .../okta-bifrost-plugin
#   git clone .../fleetops-bifrost-demo
#   cd fleetops-bifrost-demo && cp .env.example .env   # then fill it in
#   make up

PLUGIN_DIR ?= ../okta-bifrost-plugin
PLATFORM   ?= linux/amd64
COMPOSE    ?= docker compose

.DEFAULT_GOAL := help

.PHONY: up
up: plugin ## Build the plugin, render the config, start everything
	$(COMPOSE) up -d --build
	@echo
	@echo "bifrost is on http://localhost:8080"
	@echo "connect a client:  claude mcp add --transport http fleetops http://localhost:8080/mcp"
	@echo "watch the gateway: make logs"

.PHONY: plugin
plugin: ## Build the Okta plugin from the sibling repo
	@test -d $(PLUGIN_DIR) || { \
		echo "ERROR: $(PLUGIN_DIR) not found."; \
		echo "Clone okta-bifrost-plugin next to this repo, or set PLUGIN_DIR."; \
		exit 1; }
	$(MAKE) -C $(PLUGIN_DIR) plugin PLATFORM=$(PLATFORM)

.PHONY: down
down: ## Stop everything
	$(COMPOSE) down

.PHONY: clean
clean: ## Stop everything and drop the rendered config and Bifrost's data
	$(COMPOSE) down -v
	rm -f bifrost/config.json

.PHONY: logs
logs: ## Follow gateway and server logs
	$(COMPOSE) logs -f bifrost fleetops

.PHONY: config
config: ## Render bifrost/config.json from .env without starting anything
	$(COMPOSE) run --rm config

.PHONY: check
check: ## Build and vet the Fleet Ops server
	docker run --rm --platform $(PLATFORM) -v "$(CURDIR)/server":/src -w /src golang:1.27 \
		sh -c 'gofmt -l . && go vet ./... && go build ./...'
	@echo "server ok"

.PHONY: revoke
revoke: ## Print the console path for deactivating the agent, which is the demo's punchline
	@echo "Deactivate the agent in Okta:"
	@echo "  Directory > AI Agents > (your agent) > Deactivate"
	@echo
	@echo "Then call dispatch_vehicle again. Within agent_status_ttl (10s by default)"
	@echo "Bifrost refuses the call, even though the connection is open and the token"
	@echo "it holds has not expired. That refusal is the whole point."

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
