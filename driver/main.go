// Command driver runs the machine-context token exchange against a real Okta tenant and
// prints what came back, with no gateway involved.
//
// It exists for two reasons.
//
// First, it is the demo. Three things are worth watching: a token that names both the
// calling service and the acting agent, a refusal when the wrong lane is asked for the
// wrong scope, and the same call failing once the agent is deactivated in Okta. None of
// that needs Bifrost, and none of it needs a frontend.
//
// Second, and less obviously, it calls the PLUGIN'S OWN exchange code for steps two and
// three rather than reimplementing them. So a successful run here is direct evidence
// that the plugin's exchange works, and a failure here is a failure we would otherwise
// only have discovered through a gateway, with far less visibility.
//
// The three steps, and who performs each:
//
//  1. The initiating SERVICE CLIENT mints its own token via client_credentials at the
//     agent's authorization server, with resource set to the agent's resource URL.
//     Registered agents cannot use client_credentials at all, which is exactly why a
//     separate service client has to start the chain. This step is done here.
//  2. The AGENT exchanges that token at the ORG authorization server for an ID-JAG.
//  3. The AGENT redeems the ID-JAG at the TARGET authorization server for the access
//     token that carries the delegation chain.
//
// Steps two and three are the plugin's MintResourceToken.
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	oktabifrost "github.com/oktaforai-okta/okta-bifrost-plugin/plugin"
)

func main() {
	lane := flag.String("lane", "read", `which lane to exercise: "read" or "command"`)
	scopes := flag.String("scopes", "", "override the scopes requested, space separated. Use this to ask a lane for a scope it must refuse")
	flag.Parse()

	cfg, err := loadConfig()
	if err != nil {
		fatal(err)
	}

	binding, ok := cfg.Bindings[*lane]
	if !ok {
		fatal(fmt.Errorf("unknown lane %q, expected read or command", *lane))
	}
	if *scopes != "" {
		binding.Scopes = strings.Fields(*scopes)
	}

	fmt.Printf("lane            : %s\n", *lane)
	fmt.Printf("authz server    : %s\n", binding.AuthorizationServerID)
	fmt.Printf("target resource : %s\n", binding.TargetResourceURL)
	fmt.Printf("scopes asked    : %s\n", strings.Join(binding.Scopes, " "))
	rule()

	// Step 1. The service client speaks for itself, so no agent key is involved and no
	// assertion is signed. It just needs a token whose audience is the agent it intends
	// to invoke.
	fmt.Println("STEP 1  service client mints its own token (client_credentials)")
	t1, err := mintServiceToken(cfg)
	if err != nil {
		fatal(fmt.Errorf("step 1 failed: %w", err))
	}
	fmt.Printf("        ok, token audience should be the agent's resource URL\n")
	summarise("        ", t1)
	rule()

	// Steps 2 and 3, run by the plugin's own code.
	fmt.Println("STEP 2  agent exchanges that token at the ORG authz server for an ID-JAG")
	fmt.Println("STEP 3  agent redeems the ID-JAG at the target authz server")

	client, err := oktabifrost.NewClient(cfg)
	if err != nil {
		fatal(err)
	}

	token, expiry, err := client.MintResourceToken(t1, binding)
	if err != nil {
		// A refusal is a legitimate outcome, and often the point of the run, so it is
		// reported as a result rather than a crash. Okta's own wording is preserved.
		rule()
		fmt.Println("REFUSED by Okta")
		fmt.Printf("  %s\n", err)
		rule()
		explainRefusal(err)
		os.Exit(2)
	}

	rule()
	fmt.Println("ISSUED")
	fmt.Printf("  expires in %s\n", time.Until(expiry).Round(time.Second))
	rule()
	describeToken(token)
}

// loadConfig builds the plugin's own Config from the environment, so the driver and the
// plugin cannot drift apart on how a lane is defined.
func loadConfig() (oktabifrost.Config, error) {
	var missing []string
	get := func(k string) string {
		v := strings.TrimSpace(os.Getenv(k))
		if v == "" {
			missing = append(missing, k)
		}
		return v
	}

	cfg := oktabifrost.Config{
		OktaDomain:        get("OKTA_DOMAIN"),
		AgentID:           get("OKTA_AGENT_ID"),
		AgentResourceURL:  get("OKTA_AGENT_RESOURCE_URL"),
		PrivateKeyJWKFile: get("OKTA_AGENT_PRIVATE_KEY_FILE"),
		Bindings: map[string]oktabifrost.Binding{
			"read": {
				AuthorizationServerID: get("OKTA_READ_LANE_AS_ID"),
				TargetResourceURL:     get("FLEETOPS_READ_RESOURCE_URL"),
				Scopes:                []string{"fleet.telemetry.read", "fleet.routes.read"},
			},
			"command": {
				AuthorizationServerID: get("OKTA_COMMAND_LANE_AS_ID"),
				TargetResourceURL:     get("FLEETOPS_COMMAND_RESOURCE_URL"),
				Scopes:                []string{"fleet.dispatch.command"},
			},
		},
	}

	// Needed only for step 1, which the plugin never performs.
	get("OKTA_SERVICE_CLIENT_ID")
	get("OKTA_SERVICE_CLIENT_SECRET")
	get("OKTA_AGENT_OWN_AS_ID")

	if len(missing) > 0 {
		sort.Strings(missing)
		return cfg, fmt.Errorf("missing environment variables:\n  %s\n\nsource them from .env, e.g.  set -a; . ./.env; set +a",
			strings.Join(missing, "\n  "))
	}
	return cfg, cfg.Validate()
}

// mintServiceToken performs step 1: client_credentials at the agent's own authorization
// server, with resource set to the agent's resource URL so the resulting token is scoped
// to invoking that specific agent rather than being ambient.
func mintServiceToken(cfg oktabifrost.Config) (string, error) {
	endpoint := fmt.Sprintf("https://%s/oauth2/%s/v1/token",
		cfg.OktaDomain, os.Getenv("OKTA_AGENT_OWN_AS_ID"))

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"scope":         {envOr("OKTA_SERVICE_CLIENT_SCOPE", "agent.invoke")},
		"resource":      {cfg.AgentResourceURL},
		"client_id":     {os.Getenv("OKTA_SERVICE_CLIENT_ID")},
		"client_secret": {os.Getenv("OKTA_SERVICE_CLIENT_SECRET")},
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("HTTP %d, and the response was not JSON: %w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 400 || body.AccessToken == "" {
		return "", fmt.Errorf("%s: %s (HTTP %d at %s)", body.Error, body.Description, resp.StatusCode, endpoint)
	}
	return body.AccessToken, nil
}

// claims is the subset worth showing. act nests, one entry per delegation hop.
type claims struct {
	Iss string   `json:"iss"`
	Sub string   `json:"sub"`
	Aud any      `json:"aud"`
	Exp int64    `json:"exp"`
	Scp []string `json:"scp"`
	Cid string   `json:"cid"`
	Jti string   `json:"jti"`
	Act *actor   `json:"act,omitempty"`
}

type actor struct {
	Sub string `json:"sub"`
	Act *actor `json:"act,omitempty"`
}

func describeToken(raw string) {
	c, err := decode(raw)
	if err != nil {
		fmt.Printf("could not decode the token: %v\n", err)
		return
	}

	fmt.Println("the token Okta issued")
	fmt.Printf("  subject          : %s\n", c.Sub)

	if c.Act == nil {
		// Worth calling out rather than glossing over: without act there is no
		// delegation chain, and the whole argument rests on it being present.
		fmt.Printf("  acting agent     : ABSENT. No act claim, so this token names no acting agent.\n")
	} else {
		// Okta terminates the nested act claim by restating the subject as the innermost
		// actor's own delegator, so the raw walk yields one more entry than there are
		// principals: "service <- agent <- service" for a two-party delegation. The
		// trailing entry is dropped, and only when it is identical to the subject, so a
		// chain that genuinely revisits a principal mid-way is still shown as-is.
		// Kept in step with Claims.Chain in server/auth.go.
		chain := []string{c.Sub}
		for a := c.Act; a != nil; a = a.Act {
			chain = append(chain, a.Sub)
		}
		if n := len(chain); n > 1 && chain[n-1] == c.Sub {
			chain = chain[:n-1]
		}
		fmt.Printf("  delegation chain : %s\n", strings.Join(chain, "  <-  "))
	}

	fmt.Printf("  scopes granted   : %s\n", strings.Join(c.Scp, " "))
	fmt.Printf("  audience         : %v\n", c.Aud)
	fmt.Printf("  issuer           : %s\n", c.Iss)
	fmt.Printf("  client id        : %s\n", c.Cid)
	fmt.Printf("  token id         : %s\n", c.Jti)
	if c.Exp != 0 {
		fmt.Printf("  expires          : %s\n", time.Unix(c.Exp, 0).UTC().Format(time.RFC3339))
	}
	fmt.Println()
	fmt.Println("This is the line that matters: on a shared service account the subject and")
	fmt.Println("the acting agent would be the same value for every agent in the fleet.")
}

func summarise(indent, raw string) {
	c, err := decode(raw)
	if err != nil {
		return
	}
	fmt.Printf("%saud %v, scp %s\n", indent, c.Aud, strings.Join(c.Scp, " "))
}

func decode(raw string) (*claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("not a JWT")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var c claims
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// explainRefusal maps Okta's refusal to the object that actually needs changing, because
// the error text often points somewhere other than the cause.
func explainRefusal(err error) {
	s := err.Error()
	switch {
	case strings.Contains(s, "invalid_scope"):
		fmt.Println("This is a permission refusal, and it is the good one to demonstrate.")
		fmt.Println("The scope is not on the agent's managed CONNECTION for this lane. Publishing it")
		fmt.Println("on the authorization server and its policy is not sufficient. Okta does not")
		fmt.Println("down-scope, so an ungrantable scope fails the whole request.")
	case strings.Contains(s, "invalid_target"):
		fmt.Println("No ACTIVE connection matches the resource sent. Byte-compare the resource URL")
		fmt.Println("against the connection's resource indicator, and confirm the connection is")
		fmt.Println("ACTIVE rather than staged. This reads like a permission problem but is usually")
		fmt.Println("a configuration one.")
	case strings.Contains(s, "access_denied"):
		fmt.Println("Policy evaluation failed. Either the agent is deactivated, which is the")
		fmt.Println("revocation case, or the caller is not a permitted client of this authorization")
		fmt.Println("server.")
	case strings.Contains(s, "subject_token"):
		fmt.Println("The subject token was rejected. It must be an access token from a CUSTOM")
		fmt.Println("authorization server carrying a resource-scoped aud. An org authorization")
		fmt.Println("server token is refused by design.")
	default:
		fmt.Println("See docs/RUNBOOK.md, the failure table at the end, for how to read this.")
	}
}

func rule() { fmt.Println(strings.Repeat("-", 74)) }

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "\n%v\n", err)
	os.Exit(1)
}
