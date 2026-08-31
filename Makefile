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
up: plugin config ## Build the plugin, render the config, start everything
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
	./scripts/render-config.sh

.PHONY: check
check: ## Build and vet the Fleet Ops server
	docker run --rm --platform $(PLATFORM) -v "$(CURDIR)/server":/src -w /src golang:1.27 \
		sh -c 'gofmt -l . && go vet ./... && go build ./...'
	@echo "server ok"

# --- the demo, no gateway involved -----------------------------------------------------
# These run the real exchange against your tenant and print what came back. They call the
# plugin's own exchange code, so a passing run here is evidence the plugin's exchange
# works, not a parallel implementation that might have drifted.

DRIVER = docker run --rm -v "$(HOME)/code":/w -w /w/fleetops-bifrost-demo/driver \
	--platform $(PLATFORM) --env-file .env \
	-e OKTA_AGENT_PRIVATE_KEY_FILE=/w/fleetops-bifrost-demo/secrets/agent-key.jwk \
	golang:1.27 go run .

.PHONY: demo
demo: demo-read demo-command ## Run the happy path on both lanes

.PHONY: demo-read
demo-read: ## Read lane: expect a token naming the caller and the agent
	@$(DRIVER) -lane read

.PHONY: demo-command
demo-command: ## Command lane: expect a token with the dispatch scope
	@$(DRIVER) -lane command

.PHONY: demo-deny
demo-deny: ## Ask the READ lane for a COMMAND scope. Expect Okta to refuse, by name
	@$(DRIVER) -lane read -scopes "fleet.dispatch.command" || true

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
