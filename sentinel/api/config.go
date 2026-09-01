package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	oktabifrost "github.com/oktaforai-okta/okta-bifrost-plugin/plugin"
)

// bindingName is the key the tasking lane is filed under in the plugin's Bindings map.
// The plugin keys bindings by Bifrost MCP client name; there is no Bifrost here, so any
// stable key does, and this one names what it is.
const bindingName = "sentinel-tasking"

// config is everything the chain needs, all of it from the environment.
//
// Nothing in this struct has a default that would change what Okta is asked for. Display
// names default because getting them wrong is cosmetic; ids, resource URLs and scopes do
// not, because a silent default there would produce a refusal whose cause is the default
// rather than the tenant, which is the opposite of what this app is for.
type config struct {
	// Shared tenant.
	oktaDomain string

	// Hop 1: the Sentinel Watch Service, a plain API Services client. Registered agents
	// cannot use client_credentials at all, which is why the chain has to be started by
	// something that is not an agent.
	serviceClientID     string
	serviceClientSecret string
	serviceClientScope  string

	// The authorization server hop 1 is performed AT: the one protecting the Intake Agent
	// as a resource. Not the Tasking Agent's.
	intakeOwnASID string

	// The Sentinel Intake Agent (agent A). It is the audience of hop 1 and the actor of
	// hop 2, so it appears on both.
	intakeAgentID     string
	intakeResourceURL string
	intakeKeyFile     string

	// The Sentinel Tasking Agent (agent B), the privileged target of hop 2.
	taskingASID        string
	taskingResourceURL string
	taskingScopes      []string

	// denyScopes is what the refusal run asks for instead of taskingScopes: a scope the
	// agent's managed connection does not grant, so Okta refuses it by name. Empty means
	// no refusal run is offered.
	denyScopes []string

	// The gateway path: a real tool call through Bifrost. Empty bifrostURL means the
	// gateway path is not offered at all, and only the direct-to-Okta path is available.
	//
	// The tool names are the MCP server's own, not tenant values, and both are reported to
	// the UI, so defaulting them is safe and saves configuring the common case.
	bifrostURL      string
	gatewayTool     string
	gatewayArgs     string
	gatewayDenyTool string
	gatewayDenyArgs string

	// Display only.
	watchName   string
	intakeName  string
	taskingName string

	// maskIDs blanks the tenant host and the wlp/aus/0oa ids out of the endpoint strings
	// this API reports. Claims are never masked: the point of the app is that you see
	// exactly what the token carries.
	maskIDs bool

	// allowedOrigin is echoed as Access-Control-Allow-Origin. The frontend is served from
	// somewhere else (Vercel) and the API from Render, so this is always cross-origin.
	allowedOrigin string

	// port to listen on. Render supplies PORT.
	port string

	// missing lists the required variables that were not set. Collected rather than
	// fatal, so /api/config still answers and the frontend can draw its idle state.
	missing []string
}

func loadConfig() *config {
	c := &config{}

	req := func(key string) string {
		v := strings.TrimSpace(os.Getenv(key))
		if v == "" {
			c.missing = append(c.missing, key)
		}
		return v
	}
	opt := func(key, def string) string {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
		return def
	}

	// Names carried over from the repo's .env.example, so a filled-in .env that already
	// drives the Fleet Ops driver needs only the three SENTINEL_TASKING_* values added.
	c.oktaDomain = req("OKTA_DOMAIN")
	c.serviceClientID = req("OKTA_SERVICE_CLIENT_ID")
	c.serviceClientSecret = req("OKTA_SERVICE_CLIENT_SECRET")
	c.serviceClientScope = opt("OKTA_SERVICE_CLIENT_SCOPE", "agent.invoke")
	c.intakeOwnASID = req("OKTA_AGENT_OWN_AS_ID")
	c.intakeAgentID = req("OKTA_AGENT_ID")
	c.intakeResourceURL = req("OKTA_AGENT_RESOURCE_URL")
	c.intakeKeyFile = req("OKTA_AGENT_PRIVATE_KEY_FILE")

	// New: the second agent. The Fleet Ops driver's target is a resource server behind a
	// lane, so there is no existing variable that means "the other agent".
	c.taskingASID = req("SENTINEL_TASKING_AS_ID")
	c.taskingResourceURL = req("SENTINEL_TASKING_RESOURCE_URL")
	c.taskingScopes = strings.Fields(req("SENTINEL_TASKING_SCOPES"))

	// The refusal run asks for a scope the agent's connection does not grant, so Okta
	// denies it by name. Optional: unset simply means the UI offers no refusal button.
	//
	// This is the one place a default IS appropriate despite the rule below, and the
	// reason is worth stating. Everywhere else a silent default could produce a refusal
	// whose cause is the default rather than the tenant, which would be misleading. Here
	// a refusal is the entire point, the scope requested is reported to the UI and shown
	// on the button, and Okta names the offending scope back verbatim. Nothing is hidden.
	c.denyScopes = strings.Fields(opt("SENTINEL_DENY_SCOPES", "task.dispatch"))

	// localhost is right for an API run natively next to the compose stack. Running this
	// API in a container needs the host's address instead, typically
	// http://host.docker.internal:8080/mcp, because the container's own localhost is not
	// the host's.
	c.bifrostURL = opt("SENTINEL_BIFROST_URL", "http://localhost:8080/mcp")
	c.gatewayTool = opt("SENTINEL_GATEWAY_TOOL", "fleetops_read-list_routes")
	c.gatewayArgs = opt("SENTINEL_GATEWAY_TOOL_ARGS", "{}")
	c.gatewayDenyTool = opt("SENTINEL_GATEWAY_DENY_TOOL", "fleetops_command-dispatch_vehicle")
	c.gatewayDenyArgs = opt("SENTINEL_GATEWAY_DENY_TOOL_ARGS",
		`{"vehicle_id":"FL-114","destination":"Depot 4"}`)

	c.watchName = opt("SENTINEL_WATCH_NAME", "Sentinel Watch Service")
	c.intakeName = opt("SENTINEL_INTAKE_NAME", "Sentinel Intake Agent")
	c.taskingName = opt("SENTINEL_TASKING_NAME", "Sentinel Tasking Agent")

	c.maskIDs = strings.EqualFold(opt("SENTINEL_MASK_IDS", "false"), "true")
	c.allowedOrigin = opt("SENTINEL_ALLOWED_ORIGIN", "*")

	// 8090 rather than 8080: Bifrost already owns 8080 in this repo's compose file, and
	// a port clash on a demo machine is a confusing way to lose ten minutes.
	c.port = opt("PORT", "8090")

	return c
}

func (c *config) ready() bool { return len(c.missing) == 0 }

// pluginConfig maps this app's environment onto the plugin's own Config, so the two
// cannot drift on what a binding means. The agent identity here is the INTAKE agent:
// it is the one that signs the client assertion for hop 2.
func (c *config) pluginConfig() oktabifrost.Config {
	return oktabifrost.Config{
		OktaDomain:        c.oktaDomain,
		AgentID:           c.intakeAgentID,
		AgentResourceURL:  c.intakeResourceURL,
		PrivateKeyJWKFile: c.intakeKeyFile,
		Bindings: map[string]oktabifrost.Binding{
			bindingName: {
				AuthorizationServerID: c.taskingASID,
				TargetResourceURL:     c.taskingResourceURL,
				Scopes:                c.taskingScopes,
			},
		},
	}
}

func (c *config) binding() oktabifrost.Binding {
	return c.pluginConfig().Bindings[bindingName]
}

// runMode selects which scopes hop 2 asks for.
type runMode string

const (
	modeGrant runMode = "grant" // the scopes the connection is expected to grant
	modeDeny  runMode = "deny"  // a scope it does not, so Okta refuses by name
)

// parseMode validates a mode from the query string. Unknown values are rejected rather
// than falling back to grant: a typo silently running the wrong demo is worse than an
// error, and on stage the difference matters.
func parseMode(s string) (runMode, error) {
	switch s {
	case "", string(modeGrant):
		return modeGrant, nil
	case string(modeDeny):
		return modeDeny, nil
	}
	return "", fmt.Errorf("unknown mode %q: use %q or %q", s, modeGrant, modeDeny)
}

// bindingFor returns hop 2's binding for a mode. Only the scope list differs: the target
// authorization server and resource URL are identical, so a refusal is attributable to
// the scopes asked for and to nothing else.
func (c *config) bindingFor(mode runMode) oktabifrost.Binding {
	b := c.binding()
	if mode == modeDeny {
		b.Scopes = c.denyScopes
	}
	return b
}

func (c *config) canDeny() bool { return len(c.denyScopes) > 0 }

// runPath selects which of the two demonstrations to run.
type runPath string

const (
	// pathGateway is a real tool call through Bifrost to the MCP server. This is the shape
	// the demo is about: Okta decides, the gateway enforces.
	pathGateway runPath = "gateway"

	// pathOkta is the token exchange with no gateway in the way. The right thing to look
	// at when the exchange itself is the question.
	pathOkta runPath = "okta"
)

func parsePath(s string) (runPath, error) {
	switch s {
	case "", string(pathGateway):
		return pathGateway, nil
	case string(pathOkta):
		return pathOkta, nil
	}
	return "", fmt.Errorf("unknown path %q: use %q or %q", s, pathGateway, pathOkta)
}

// gatewayCall returns the tool and arguments for a mode on the gateway path.
//
// The arguments are passed through as raw JSON rather than being parsed and re-encoded, so
// whatever was configured is what the gateway receives. Invalid JSON will be rejected by
// the gateway and reported as such, which is more useful than this app second-guessing it.
func (c *config) gatewayCall(mode runMode) (tool string, args json.RawMessage) {
	if mode == modeDeny {
		return c.gatewayDenyTool, json.RawMessage(c.gatewayDenyArgs)
	}
	return c.gatewayTool, json.RawMessage(c.gatewayArgs)
}

// namer resolves an Okta id seen in a token claim to the display name and diagram node
// this app was configured with.
//
// Configuration is the only source. An id this app was not told about returns empty
// strings, and the frontend then shows the bare id: a token can name a principal this app
// has never heard of, and inventing a label for it would be exactly the kind of
// embellishment the rest of this program refuses.
//
// This runs in the API rather than the browser on purpose. /api/config carries no tenant
// identifiers, so the frontend has no id-to-name map to work from; every id it sees
// arrives inside a token payload it is already being shown. Resolving here keeps that
// property intact.
func (c *config) namer() principalNamer {
	return func(id string) (string, string) {
		switch {
		case id == "":
			return "", ""
		case id == c.serviceClientID:
			return c.watchName, "watch"
		case id == c.intakeAgentID:
			return c.intakeName, "intake"
		}
		return "", ""
	}
}

// principal is one node of the diagram. Non-secret by construction: names, roles and
// resource URLs only, never an agent id, an authorization server id or the tenant host.
type principal struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	ResourceURL string `json:"resource_url,omitempty"`
	Note        string `json:"note"`

	// Aside marks a party that is not a step in the left-to-right flow but sits beside it.
	// Okta is the only one: it is asked on every hop rather than being passed through, and
	// drawing it in the row would say something false about the order of events.
	Aside bool `json:"aside,omitempty"`

	// Anchor is the principal an aside is drawn next to.
	Anchor string `json:"anchor,omitempty"`
}

// hop is one edge of the diagram, described before anything has run.
type hop struct {
	ID     string   `json:"id"`
	Number int      `json:"number"`
	From   string   `json:"from"`
	To     string   `json:"to"`
	Label  string   `json:"label"`
	Grants []string `json:"grants"`
	Scopes []string `json:"scopes,omitempty"`
	Note   string   `json:"note"`
}

// configView is what GET /api/config returns: enough shape to draw the whole diagram
// with nothing in it that would be unsafe on a shared screen.
type configView struct {
	Ready   bool     `json:"ready"`
	Missing []string `json:"missing"`

	// DefaultPath is the path the UI should select on load: the gateway when it is
	// actually live, and the direct path otherwise. Chosen here rather than in the browser
	// because deciding it requires probing the gateway.
	DefaultPath string `json:"default_path"`

	// Paths is every demonstration this API can run, keyed by id. An unavailable path is
	// still described, with the reason, so the UI can explain why a button is disabled
	// instead of silently omitting it.
	Paths map[string]pathView `json:"paths"`
}

// pathView is one demonstration: its shape, and whether it can run.
type pathView struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	Available bool `json:"available"`

	// Unavailable is why, in a sentence, when Available is false. Empty otherwise.
	Unavailable string `json:"unavailable,omitempty"`

	Summary    string      `json:"summary"`
	Principals []principal `json:"principals"`
	Hops       []hop       `json:"hops"`
	Steps      []stepShape `json:"steps"`
	Grant      grantView   `json:"grant"`
	Deny       denyView    `json:"deny"`
}

// grantView is the ALLOWED run's shape, the mirror of denyView. It exists so the frontend
// can name the tool it is about to call rather than hardcoding a guess, which matters
// because the tool name is configurable per deployment and a label naming the wrong tool
// would be worse than no label at all.
type grantView struct {
	Available bool `json:"available"`

	// Tool is set on the gateway path, where the allowed run calls a tool whose scope the
	// agent's connection does grant.
	Tool string `json:"tool,omitempty"`

	// Scopes is set on the direct path, where there is no tool and the run simply asks for
	// scopes the connection does grant.
	Scopes []string `json:"scopes,omitempty"`
}

// denyView is the refusal run's shape for a path, so the button that triggers it can name
// what it is about to be refused instead of the frontend hardcoding a guess.
type denyView struct {
	Available bool `json:"available"`

	// Scopes is set on the direct path, where the refusal is arranged by asking for a
	// scope the connection does not grant.
	Scopes []string `json:"scopes,omitempty"`

	// Tool is set on the gateway path, where the refusal is arranged by calling a tool
	// whose scope was never granted. Nothing in the tenant changes between the two runs.
	Tool string `json:"tool,omitempty"`
}

// stepShape declares the steps a run will emit, so the frontend can list them as idle
// before the first event arrives instead of growing the list as it goes.
type stepShape struct {
	Step  string `json:"step"`
	Hop   string `json:"hop"`
	Label string `json:"label"`
}

func (c *config) view() configView {
	unset := func(s string) string {
		if s == "" {
			return "(unset)"
		}
		return s
	}

	missing := c.missing
	if missing == nil {
		missing = []string{}
	}

	// One probe, on page load, deciding whether the gateway path can be offered. Doing it
	// here rather than in the browser keeps the gateway URL out of the frontend and means
	// the answer is the API's, which is the thing that would actually make the call.
	tools, why := probeGateway(c)

	gateway := c.gatewayView(unset, tools, why)
	direct := c.directView(unset)

	// Default to the gateway, because that is the demonstration. Fall back to the direct
	// path when the gateway cannot serve a call, so the page opens on something that works.
	defaultPath := string(pathGateway)
	if !gateway.Available {
		defaultPath = string(pathOkta)
	}

	return configView{
		Ready:       c.ready(),
		Missing:     missing,
		DefaultPath: defaultPath,
		Paths: map[string]pathView{
			string(pathGateway): gateway,
			string(pathOkta):    direct,
		},
	}
}

// gatewayView describes the gateway path: caller, Bifrost, MCP server, with Okta beside
// the gateway rather than in the row, because Okta is asked on every hop rather than being
// passed through.
func (c *config) gatewayView(unset func(string) string, tools []string, why string) pathView {
	available := why == "" && c.ready()
	if why == "" && !c.ready() {
		why = "this API is not fully configured, so it cannot mint the caller token"
	}

	return pathView{
		ID:          string(pathGateway),
		Name:        "Through the gateway",
		Available:   available,
		Unavailable: why,
		Summary: "A real MCP tool call through Bifrost. Okta decides, Bifrost enforces, " +
			"and the MCP server validates the token it receives and reports what it found.",
		Principals: []principal{
			{
				ID:   "watch",
				Name: c.watchName,
				Role: "caller, service client",
				Note: "Starts the work and mints the only token this app holds. A plain " +
					"API Services client, not an agent: registered agents cannot use " +
					"client_credentials at all.",
			},
			{
				ID:   "bifrost",
				Name: "Bifrost",
				Role: "gateway, enforcement point",
				Note: "Holds no policy. It asks Okta whether it would issue this agent a " +
					"token for this target, mints one at connect, and re-asks on every " +
					"tool call. The re-ask is the point: an issued bearer token cannot be " +
					"withdrawn, so asking again is the only thing standing between a " +
					"deactivated agent and a live connection.",
			},
			{
				ID:          "fleet",
				Name:        "Fleet Ops MCP server",
				Role:        "resource server",
				ResourceURL: unset(c.taskingResourceURL),
				Note: "Validates every token itself, independently of the gateway, so " +
					"bypassing the gateway does not bypass authorization. It appends the " +
					"delegation chain it read out of the token to every tool result.",
			},
			{
				ID:     "okta",
				Name:   "Okta",
				Role:   "decision point",
				Aside:  true,
				Anchor: "bifrost",
				Note: "Every decision on this page is Okta's. It is drawn beside the flow " +
					"rather than in it because it is asked on each hop rather than being " +
					"passed through.",
			},
		},
		Hops: []hop{
			{
				ID:     "hop1",
				Number: 1,
				From:   "watch", To: "bifrost",
				Label:  "client_credentials, resource = " + unset(c.intakeResourceURL),
				Grants: []string{"client_credentials"},
				Scopes: strings.Fields(c.serviceClientScope),
				Note: "The caller mints a token for itself and presents it to the gateway " +
					"as a bearer credential. The resource parameter is what makes the " +
					"grant specific to invoking this one agent rather than ambient.",
			},
			{
				ID:     "hop2",
				Number: 2,
				From:   "bifrost", To: "fleet",
				Label: "MCP tools/call, with a delegated token minted inside Bifrost",
				Grants: []string{
					"urn:ietf:params:oauth:grant-type:token-exchange",
					"urn:ietf:params:oauth:grant-type:jwt-bearer",
				},
				// Deliberately no Scopes. Which scopes are requested for this hop is
				// decided by Bifrost's own binding for the lane the tool belongs to, and
				// this app does not hold that configuration. Reporting the direct path's
				// scope list here would state a request that was never made: on a refused
				// run it read "agent.invoke task.read" while Okta was refusing
				// task.dispatch, which is precisely the kind of claim this app must not
				// make. The tool name is reported instead, and the scopes appear where
				// they are actually known: in Okta's refusal, and in the MCP server's
				// account of the token it received.
				// Kept to one sentence deliberately. This is presented on a shared screen,
				// and the "this app never holds the token" point used to be repeated in
				// four places, which reads as hedging rather than as rigour. It is now
				// stated once, on the step where the server's own account appears, which
				// is where it actually matters.
				Note: "Bifrost gets an ID-JAG from Okta, redeems it for a token, and " +
					"injects that token upstream.",
			},
		},
		Steps: []stepShape{
			{Step: stepCallerToken, Hop: "hop1", Label: labelCallerToken},
			{Step: stepGatewayCall, Hop: "hop2", Label: labelGatewayCall},
			{Step: stepResourceResult, Hop: "hop2", Label: labelResourceResult},
		},
		Grant: grantView{
			Available: available && c.gatewayTool != "",
			Tool:      c.gatewayTool,
		},
		Deny: denyView{
			Available: available && c.gatewayDenyTool != "",
			Tool:      c.gatewayDenyTool,
		},
	}
}

// directView describes the direct-to-Okta path: the token exchange with no gateway in the
// way, which is the right thing to look at when the exchange itself is the question.
func (c *config) directView(unset func(string) string) pathView {
	return pathView{
		ID:        string(pathOkta),
		Name:      "Direct to Okta, no gateway",
		Available: c.ready(),
		Unavailable: func() string {
			if c.ready() {
				return ""
			}
			return "this API is not fully configured"
		}(),
		Summary: "The same delegation, with nothing in the middle. No gateway is involved, " +
			"so every token is decoded here and every refusal is Okta's own error body " +
			"rather than something relayed.",
		Principals: []principal{
			{
				ID:   "watch",
				Name: c.watchName,
				Role: "service client",
				Note: "Starts the chain. A plain API Services client, not an agent: " +
					"registered agents cannot use client_credentials at all.",
			},
			{
				ID:          "intake",
				Name:        c.intakeName,
				Role:        "agent A",
				ResourceURL: unset(c.intakeResourceURL),
				Note: "Receives hop 1 as its audience, then acts as the caller for " +
					"hop 2. Authenticates to Okta with private_key_jwt.",
			},
			{
				ID:          "tasking",
				Name:        c.taskingName,
				Role:        "agent B, privileged",
				ResourceURL: unset(c.taskingResourceURL),
				Note: "The privileged target. Its authorization server decides whether " +
					"the chain gets a token, and with which scopes.",
			},
		},
		Hops: []hop{
			{
				ID:     "hop1",
				Number: 1,
				From:   "watch", To: "intake",
				Label:  "client_credentials, resource = " + unset(c.intakeResourceURL),
				Grants: []string{"client_credentials"},
				Scopes: strings.Fields(c.serviceClientScope),
				Note: "One call, at a custom authorization server. The resource " +
					"parameter is what makes the grant specific to the Intake Agent " +
					"rather than ambient.",
			},
			{
				ID:     "hop2",
				Number: 2,
				From:   "intake", To: "tasking",
				Label: "token exchange, then ID-JAG redemption",
				Grants: []string{
					"urn:ietf:params:oauth:grant-type:token-exchange",
					"urn:ietf:params:oauth:grant-type:jwt-bearer",
				},
				Scopes: c.taskingScopes,
				Note: "Two calls, run by the plugin's own exchange: an ID-JAG at the ORG " +
					"authorization server, redeemed at the Tasking Agent's. Both the " +
					"assertion and the final token are decoded below.",
			},
		},
		Steps: []stepShape{
			{Step: stepWatchToken, Hop: "hop1", Label: labelWatchToken},
			{Step: stepIDJAG, Hop: "hop2", Label: labelIDJAG},
			{Step: stepAccessToken, Hop: "hop2", Label: labelAccessToken},
		},
		Grant: grantView{
			Available: c.ready(),
			Scopes:    c.taskingScopes,
		},
		Deny: denyView{
			Available: c.ready() && c.canDeny(),
			Scopes:    c.denyScopes,
		},
	}
}
