package main

import (
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

// principal is one node of the diagram. Non-secret by construction: names, roles and
// resource URLs only, never an agent id, an authorization server id or the tenant host.
type principal struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	ResourceURL string `json:"resource_url,omitempty"`
	Note        string `json:"note"`
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
	Ready      bool        `json:"ready"`
	Missing    []string    `json:"missing"`
	Principals []principal `json:"principals"`
	Hops       []hop       `json:"hops"`
	Steps      []stepShape `json:"steps"`
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

	return configView{
		Ready:   c.ready(),
		Missing: missing,
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
	}
}
