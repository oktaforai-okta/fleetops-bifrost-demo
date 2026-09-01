package main

// The gateway path: a real tool call through Bifrost to the MCP server.
//
// This is the shape the demo is actually about. The direct-to-Okta path in chain.go shows
// the token exchange with nothing in the way, which is the right thing to look at when the
// exchange itself is in question. This path shows the exchange doing its job inside a
// gateway, which is where it runs in production:
//
//	Watch Service  ->  Bifrost  ->  MCP server
//	  (caller)          (PEP)        (validates the token it received)
//	                      |
//	                    Okta (PDP), asked at connect and re-asked every call
//
// One thing to be precise about, because it shapes everything below. This app mints the
// CALLER token and nothing else. The delegated token that Bifrost mints and injects
// upstream never passes through here, so its claims cannot be decoded here and are not
// guessed at. What can be shown is the MCP server's own account of the token it received,
// which it appends to every tool result. That is a better witness than a claim this app
// made up: the resource server is independent of both the gateway and this page.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The steps the gateway path emits.
const (
	stepCallerToken    = "caller_token"
	stepGatewayCall    = "gateway_call"
	stepResourceResult = "resource_result"
)

const (
	labelCallerToken    = "Watch Service mints its caller token"
	labelGatewayCall    = "Bifrost asks Okta, mints the delegated token, and calls the tool"
	labelResourceResult = "The MCP server validates that token and answers"
)

// attributionMarker is the line the MCP server writes before its account of the token it
// received. Used only to SPLIT the text for display, never to derive a claim from it: if
// it is absent the whole body is shown as one block and nothing is lost.
const attributionMarker = "--- authorized by Okta ---"

// gatewayDenialMarker is how the plugin prefixes a refusal it relayed from Okta.
//
// Matching on prose is not this codebase's habit and it is not done lightly here. MCP
// gives a tool result exactly two things, an isError boolean and free text, so there is no
// structure to prefer. The match is therefore deliberately conservative and fails in the
// safe direction: a call whose text does not carry this marker is reported as reaching NO
// DECISION rather than as an Okta refusal. If the plugin rewords this string the demo
// under-claims, which is recoverable. The alternative failure, attributing an arbitrary
// tool error to an Okta policy decision, is exactly the dishonesty the rest of this app
// exists to avoid.
const gatewayDenialMarker = "okta denied"

// runGateway performs the gateway path, emitting as it goes.
func runGateway(cfg *config, mode runMode, emit emitFunc) {
	tool, args := cfg.gatewayCall(mode)

	callerEvent := func(state string) stepEvent {
		return stepEvent{
			Step:     stepCallerToken,
			Hop:      "hop1",
			Label:    labelCallerToken,
			Actor:    "watch",
			Target:   "bifrost",
			Grant:    "client_credentials",
			Scopes:   strings.Fields(cfg.serviceClientScope),
			Endpoint: maskEndpoint(serviceTokenEndpoint(cfg), cfg.oktaDomain, cfg.maskIDs),
			State:    state,
		}
	}

	// --- Step 1: the caller token ------------------------------------------------------
	// The same client_credentials call the direct path makes, and the same one
	// scripts/get-caller-token.sh makes, so all three agree by construction. Registered
	// agents cannot use client_credentials, which is why a service client starts the chain.
	ev := callerEvent(stateInProgress)
	ev.Detail = "This app mints ONE token: the caller's. Its audience is the Intake Agent's " +
		"resource URL, so the grant names the agent being invoked rather than being ambient."
	emit(ev)

	callerToken, err := mintServiceToken(cfg)
	if err != nil {
		ev = callerEvent(outcomeState(err))
		ev.Error = failure(err, cfg)
		ev.Detail = "The chain stops here. With no caller token there is nothing for " +
			"Bifrost to delegate from."
		emit(ev)

		skip(emit, stepGatewayCall, "hop2", labelGatewayCall, "bifrost", "fleet",
			"Not attempted: there is no caller token to send.")
		skip(emit, stepResourceResult, "hop2", labelResourceResult, "fleet", "fleet",
			"Not attempted: no call was made.")
		return
	}

	ev = callerEvent(stateIssued)
	ev.Token = describe(kindAccessToken, callerToken, cfg.namer())
	ev.Detail = "This is the only token this app holds. Note it carries no act claim: it " +
		"is the start of the chain, not a delegation. The delegated token that Bifrost " +
		"mints next is never seen by this app, so nothing about its claims is asserted here."
	emit(ev)

	// --- Step 2: the call through the gateway ------------------------------------------
	callEvent := func(state string) stepEvent {
		return stepEvent{
			Step:     stepGatewayCall,
			Hop:      "hop2",
			Label:    labelGatewayCall,
			Actor:    "bifrost",
			Target:   "fleet",
			Grant:    "MCP tools/call, delegated token minted inside Bifrost",
			Tool:     tool,
			Endpoint: cfg.bifrostURL,
			State:    state,
		}
	}

	ev = callEvent(stateInProgress)
	ev.Detail = "The caller token goes to Bifrost as a bearer credential. Bifrost then " +
		"asks Okta whether it would issue this agent a token for this target, mints it, " +
		"and injects it upstream. Bifrost holds no policy of its own: the decision is " +
		"Okta's, and Bifrost only enforces it."
	if mode == modeDeny {
		ev.Detail = "Calling " + tool + ", whose scope the agent's managed connection does " +
			"not grant. Nothing in the tenant has been changed to arrange this: the same " +
			"agent, the same connection, a different tool."
	}
	emit(ev)

	result, err := callTool(cfg, callerToken, tool, args)
	if err != nil {
		// Transport, or a response that could not be read. Okta was never asked, so this
		// is never a refusal.
		ev = callEvent(stateFailed)
		ev.Error = failure(err, cfg)
		ev.Detail = "The gateway could not be reached, or its answer could not be read. " +
			"Nothing here may be attributed to a policy decision."
		emit(ev)
		skip(emit, stepResourceResult, "hop2", labelResourceResult, "fleet", "fleet",
			"Not attempted: the call did not complete.")
		return
	}

	if result.denied() {
		ev = callEvent(stateRefused)
		ev.Error = &oktaFailure{
			Description: result.Text,
			Source:      sourceGateway,
		}
		ev.Detail = "Bifrost refused the call before it reached the MCP server, and the " +
			"reason it gives is Okta's. This is the enforcement point obeying the decision " +
			"point. Okta's wording is reproduced below exactly as the gateway relayed it; " +
			"this app did not reword it and did not reach Okta itself on this call."
		emit(ev)

		skip(emit, stepResourceResult, "hop2", labelResourceResult, "fleet", "fleet",
			"Not attempted: the gateway refused the call, so the MCP server never saw it.")
		return
	}

	if result.IsError {
		// An error that is not a relayed Okta denial. Reported as reaching no decision,
		// because attributing it to Okta would be a guess.
		ev = callEvent(stateFailed)
		ev.Error = &oktaFailure{Description: result.Text, Source: sourceGateway}
		ev.Detail = "The tool call failed, but the reason does not identify itself as an " +
			"Okta decision, so it is not presented as one."
		emit(ev)
		skip(emit, stepResourceResult, "hop2", labelResourceResult, "fleet", "fleet",
			"Not attempted: the tool call did not succeed.")
		return
	}

	ev = callEvent(stateIssued)
	ev.Detail = "Bifrost obtained a delegated token from Okta and the call went through. " +
		"That token is not shown here because this app never holds it; what the MCP server " +
		"made of it is the next step."
	emit(ev)

	// --- Step 3: what the resource server said -----------------------------------------
	body, attribution := splitAttribution(result.Text)

	ev = stepEvent{
		Step:   stepResourceResult,
		Hop:    "hop2",
		Label:  labelResourceResult,
		Actor:  "fleet",
		Target: "fleet",
		Tool:   tool,
		State:  stateIssued,
		Detail: "The MCP server validated the token itself, independently of the gateway, " +
			"and appended its own account of what it found. Both blocks below are its " +
			"words verbatim. This is the strongest evidence in the demo precisely because " +
			"it does not come from this page: the resource server read the delegation " +
			"chain off the credential it was handed.",
		Result: &resourceResult{
			Body:        body,
			Attribution: attribution,
		},
	}
	if attribution == "" {
		ev.Detail = "The MCP server answered, but its reply carries no authorization " +
			"block, so there is nothing here about the token it received. The reply is " +
			"shown verbatim and nothing is added to it."
	}
	emit(ev)
}

// resourceResult is what the MCP server said, verbatim, split only for presentation.
type resourceResult struct {
	// Body is the tool's own output.
	Body string `json:"body"`

	// Attribution is the server's account of the token it received: the delegation chain
	// it read, the scopes granted, the audience, the token id and the expiry. Empty when
	// the reply carried no such block, which is reported rather than filled in.
	Attribution string `json:"attribution,omitempty"`
}

// splitAttribution divides the tool text at the server's marker. Presentation only: both
// halves are returned unmodified, and an absent marker yields the whole body with no
// attribution rather than a guess at where one would have been.
func splitAttribution(text string) (body, attribution string) {
	i := strings.Index(text, attributionMarker)
	if i < 0 {
		return strings.TrimRight(text, "\n"), ""
	}
	return strings.TrimRight(text[:i], "\n"),
		strings.TrimRight(text[i+len(attributionMarker):], "\n")
}

// toolResult is the part of an MCP tools/call response this app reads.
type toolResult struct {
	Text    string
	IsError bool
}

// denied reports whether this result is a refusal the gateway relayed from Okta, as
// opposed to any other kind of failure. See gatewayDenialMarker for why this is a text
// match and why it is deliberately conservative.
func (r *toolResult) denied() bool {
	return r.IsError && strings.Contains(strings.ToLower(r.Text), gatewayDenialMarker)
}

// callTool performs one MCP tools/call against Bifrost, presenting the caller token as a
// bearer credential.
//
// No initialize handshake and no session header: Bifrost answers a bare tools/call over
// plain JSON-RPC and returns no Mcp-Session-Id, so there is no session to carry. Verified
// against the running gateway rather than assumed.
func callTool(cfg *config, callerToken, tool string, args json.RawMessage) (*toolResult, error) {
	if strings.TrimSpace(cfg.bifrostURL) == "" {
		return nil, fmt.Errorf("no gateway URL is configured: set SENTINEL_BIFROST_URL")
	}

	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      tool,
			"arguments": args,
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, cfg.bifrostURL, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+callerToken)
	req.Header.Set("User-Agent", "sentinel-chain-of-custody/0.1")

	resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var envelope struct {
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("HTTP %d from the gateway, and the response was not JSON-RPC: %w",
			resp.StatusCode, err)
	}

	// A JSON-RPC level error. Surfaced as an error rather than as a tool result, because
	// it means the call was not dispatched at all.
	if envelope.Error != nil {
		return nil, fmt.Errorf("the gateway returned JSON-RPC error %d: %s",
			envelope.Error.Code, envelope.Error.Message)
	}
	if envelope.Result == nil {
		return nil, fmt.Errorf("HTTP %d from the gateway with neither a result nor an error",
			resp.StatusCode)
	}

	var text strings.Builder
	for _, c := range envelope.Result.Content {
		text.WriteString(c.Text)
	}
	return &toolResult{Text: text.String(), IsError: envelope.Result.IsError}, nil
}

// probeGateway asks the gateway for its tool list, to decide whether the gateway path can
// be offered at all. Read-only, and short-timeout because it runs on page load.
//
// An empty tool list is reported as unavailable WITH that reason, because it is the
// failure mode this setup actually has: Bifrost registers MCP clients at startup, where
// there is no caller token, and if that registration is refused no tools are discovered
// and every later call fails as "tool not found" rather than as anything resembling an
// authorization problem. Saying so beats letting someone press a button that cannot work.
func probeGateway(cfg *config) (tools []string, reason string) {
	if strings.TrimSpace(cfg.bifrostURL) == "" {
		return nil, "no gateway URL is configured (SENTINEL_BIFROST_URL is unset)"
	}

	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req, err := http.NewRequest(http.MethodPost, cfg.bifrostURL, strings.NewReader(payload))
	if err != nil {
		return nil, "the gateway URL could not be used: " + err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		return nil, "the gateway could not be reached: " +
			maskEndpoint(err.Error(), cfg.oktaDomain, cfg.maskIDs)
	}
	defer resp.Body.Close()

	var envelope struct {
		Result *struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Sprintf("the gateway answered HTTP %d but not with JSON-RPC", resp.StatusCode)
	}
	if envelope.Result == nil {
		return nil, fmt.Sprintf("the gateway answered HTTP %d with no tool list", resp.StatusCode)
	}

	for _, t := range envelope.Result.Tools {
		tools = append(tools, t.Name)
	}
	if len(tools) == 0 {
		return nil, "the gateway is up but has registered no tools, so no tool call can " +
			"succeed. This usually means Bifrost's startup connection to the MCP server " +
			"was refused, which happens when it has no caller token at registration time."
	}
	return tools, ""
}
