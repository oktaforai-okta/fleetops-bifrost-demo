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

# AGENT_KEY_FILE is the ONE canonical name for the agent's private key, and it matches
# what bifrost/config.template.json and scripts/render-config.sh both require. It used to
# differ here (agent-key.jwk), which worked only on a machine where both filenames
# happened to exist, and stopped anyone following the runbook at `make config`.
AGENT_KEY_FILE ?= secrets/sentinel-intake-key.jwk

# DENY_SCOPE is the scope the deny run asks for and expects to be refused. It must be a
# scope the tenant PUBLISHES but the agent's connection does NOT grant. A scope the tenant
# does not publish at all also fails, but for the wrong reason, which makes the demo prove
# nothing: that is what "fleet.dispatch.command" did here before.
DENY_SCOPE ?= $(if $(FLEETOPS_SCOPE_DISPATCH),$(FLEETOPS_SCOPE_DISPATCH),task.dispatch)

DRIVER = docker run --rm -v "$(HOME)/code":/w -w /w/fleetops-bifrost-demo/driver \
	--platform $(PLATFORM) --env-file .env \
	-e OKTA_AGENT_PRIVATE_KEY_FILE=/w/fleetops-bifrost-demo/$(AGENT_KEY_FILE) \
	golang:1.27 go run .

.PHONY: demo
demo: demo-read demo-command ## Both outcomes: read is issued, command is refused

.PHONY: demo-read
demo-read: ## Read lane: expect a token naming the caller and the agent
	@$(DRIVER) -lane read

# On the proven tenant this is REFUSED, and that is the correct outcome, not a broken
# target. The agent's managed connection grants task.read and not task.dispatch, so the
# command lane cannot be satisfied. It was previously described as expecting a token,
# which made `make demo` fail and read as a broken demo rather than a working refusal.
#
# The exit code is swallowed so `make demo` can show both outcomes in one run. Okta's
# refusal is printed in full by the driver either way, so nothing is hidden: if this
# starts SUCCEEDING, that is the signal worth noticing, and it means someone granted
# task.dispatch on the agent's connection.
.PHONY: demo-command
demo-command: ## Command lane: expect Okta to REFUSE, the connection does not grant dispatch
	@$(DRIVER) -lane command || true

.PHONY: demo-deny
demo-deny: ## Ask the read lane for a scope its connection does not grant. Expect Okta to refuse, by name
	@$(DRIVER) -lane read -scopes "$(DENY_SCOPE)" || true

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
