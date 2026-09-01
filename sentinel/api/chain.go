package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	oktabifrost "github.com/oktaforai-okta/okta-bifrost-plugin/plugin"
)

// The three steps a run emits. Two hops, but hop 2 is two token calls, and collapsing
// them would hide the ID-JAG, which is the part of the chain worth seeing.
const (
	stepWatchToken  = "watch_token"
	stepIDJAG       = "id_jag"
	stepAccessToken = "access_token"
)

// The two artefact kinds. An ID-JAG is an assertion, not an access token, and is labelled
// as such so an absent access-token claim on it is read as a difference in kind rather
// than as a missing value.
const (
	kindAssertion   = "assertion (ID-JAG)"
	kindAccessToken = "access token"
)

const (
	labelWatchToken  = "Watch Service mints its own token"
	labelIDJAG       = "Intake Agent exchanges that token for an ID-JAG"
	labelAccessToken = "Intake Agent redeems the ID-JAG for the Tasking Agent's token"
)

// Step states. A refusal is an outcome, not an error: an Okta denial is often the most
// interesting thing a run can produce, so it is reported the same way a grant is.
//
// refused and failed are kept apart on purpose. refused means Okta made a decision and
// the decision was no. failed means no decision was reached at all: a DNS failure, a
// timeout, a key that would not parse. Collapsing the two would let the page claim Okta
// denied something it was never asked, which is the same category of dishonesty as
// drawing an act chain that is not on the token.
const (
	stateInProgress = "in_progress"
	stateIssued     = "issued"
	stateRefused    = "refused"
	stateFailed     = "failed"
	stateSkipped    = "skipped"
)

// stepEvent is one Server-Sent Event. One per state change, so the UI lights up as the
// chain runs rather than all at once at the end.
type stepEvent struct {
	Step  string `json:"step"`
	Hop   string `json:"hop"`
	Label string `json:"label"`

	// Actor and Target are principal ids, so the frontend can colour the right node and
	// edge without knowing the scenario.
	Actor  string `json:"actor"`
	Target string `json:"target"`

	// Endpoint is the URL actually called, subject to SENTINEL_MASK_IDS.
	Endpoint string `json:"endpoint,omitempty"`
	Grant    string `json:"grant,omitempty"`

	// Scopes is what this step asked for. Reported so a refusal is shown next to the
	// request that earned it, rather than leaving the reader to infer what was asked.
	Scopes []string `json:"scopes,omitempty"`

	// Tool is the MCP tool called, on the gateway path only.
	Tool string `json:"tool,omitempty"`

	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`

	Error *oktaFailure `json:"error,omitempty"`
	Token *tokenView   `json:"token,omitempty"`

	// Result is what an upstream resource server said, verbatim. Gateway path only.
	Result *resourceResult `json:"result,omitempty"`
}

// oktaFailure carries a refusal. Okta's own wording is preserved without rewording:
// the difference between invalid_scope and invalid_target is the difference between a
// policy decision and a misconfiguration, and only Okta knows which one happened.
type oktaFailure struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
	HTTPStatus  int    `json:"http_status,omitempty"`

	// FromOkta distinguishes Okta's words from ours. A DNS failure is not a refusal, and
	// the UI must not present one as though it were.
	//
	// True only for source okta: an error body this app received from Okta directly. A
	// refusal relayed through the gateway quotes Okta but was not read from Okta by this
	// app, so it is source gateway and this stays false. The distinction is small and it
	// is the honest one.
	FromOkta bool `json:"from_okta"`

	// Source is where the wording came from: okta, gateway or sentinel.
	Source string `json:"source"`
}

// Where a piece of error wording originated.
const (
	sourceOkta     = "okta"     // an error body this app received from Okta
	sourceGateway  = "gateway"  // Bifrost's wording, which may quote Okta
	sourceSentinel = "sentinel" // this application's own wording
)

// emitFunc publishes one event. Implemented by the SSE writer.
type emitFunc func(stepEvent)

// runChain executes the whole chain, emitting as it goes. It returns nothing: every
// outcome, including a refusal, has already been reported through emit.
func runChain(cfg *config, mode runMode, emit emitFunc) {
	hop1 := func(state string) stepEvent {
		return stepEvent{
			Step:     stepWatchToken,
			Hop:      "hop1",
			Label:    labelWatchToken,
			Actor:    "watch",
			Target:   "intake",
			Grant:    "client_credentials",
			Scopes:   strings.Fields(cfg.serviceClientScope),
			Endpoint: maskEndpoint(serviceTokenEndpoint(cfg), cfg.oktaDomain, cfg.maskIDs),
			State:    state,
		}
	}

	// --- Hop 1 ------------------------------------------------------------------------
	// The Watch Service speaks for itself, so no agent key and no signed assertion. It
	// needs one thing: a token whose audience is the agent it intends to invoke.
	ev := hop1(stateInProgress)
	ev.Detail = "Requesting a token whose audience is the Intake Agent's resource URL, so " +
		"the grant names the agent being invoked rather than being ambient."
	emit(ev)

	subjectToken, err := mintServiceToken(cfg)
	if err != nil {
		ev = hop1(outcomeState(err))
		ev.Error = failure(err, cfg)
		ev.Detail = "The chain stops here. Without a caller token there is nothing for the " +
			"Intake Agent to delegate from."
		emit(ev)

		skip(emit, stepIDJAG, "hop2", labelIDJAG, "intake", "tasking",
			"Not attempted: hop 1 produced no subject token.")
		skip(emit, stepAccessToken, "hop2", labelAccessToken, "intake", "tasking",
			"Not attempted: hop 1 produced no subject token.")
		return
	}

	ev = hop1(stateIssued)
	ev.Token = describe(kindAccessToken, subjectToken, cfg.namer())
	ev.Detail = "Check aud below: it should be the Intake Agent's resource URL."
	emit(ev)

	// --- Hop 2 ------------------------------------------------------------------------
	// Both remaining steps are the plugin's own exchange, imported rather than
	// reimplemented. That is the point of this app: a run that reaches ISSUED is direct
	// evidence the plugin's exchange works, and a refusal is one we would otherwise only
	// have found through a gateway with far less visibility.
	binding := cfg.bindingFor(mode)

	idJAGEvent := func(state string) stepEvent {
		return stepEvent{
			Step:     stepIDJAG,
			Hop:      "hop2",
			Label:    labelIDJAG,
			Actor:    "intake",
			Target:   "tasking",
			Grant:    "urn:ietf:params:oauth:grant-type:token-exchange",
			Scopes:   binding.Scopes,
			Endpoint: maskEndpoint(orgTokenEndpoint(cfg), cfg.oktaDomain, cfg.maskIDs),
			State:    state,
		}
	}
	accessEvent := func(state string) stepEvent {
		return stepEvent{
			Step:     stepAccessToken,
			Hop:      "hop2",
			Label:    labelAccessToken,
			Actor:    "intake",
			Target:   "tasking",
			Grant:    "urn:ietf:params:oauth:grant-type:jwt-bearer",
			Scopes:   binding.Scopes,
			Endpoint: maskEndpoint(casTokenEndpoint(cfg), cfg.oktaDomain, cfg.maskIDs),
			State:    state,
		}
	}

	ev = idJAGEvent(stateInProgress)
	ev.Detail = "At the ORG authorization server, which has no id in its path. It is the " +
		"only place an ID-JAG can be obtained; exchanging at a custom authorization " +
		"server instead is what makes a gateway look like it supports delegation when it " +
		"only supports one hop."
	if mode == modeDeny {
		ev.Detail = "This run asks for " + strings.Join(binding.Scopes, " ") + ", which the " +
			"agent's managed connection is not expected to grant. Everything else about " +
			"the request is identical to the granted run, so whatever comes back is " +
			"attributable to the scopes asked for and to nothing else."
	}
	emit(ev)
	emit(accessEvent(stateInProgress))

	client, err := oktabifrost.NewClient(cfg.pluginConfig())
	if err != nil {
		// A bad key or an incoherent config fails here, before any network call. Nothing
		// was asked of Okta, so this is never a refusal.
		ev = idJAGEvent(stateFailed)
		ev.Error = failure(err, cfg)
		ev.Detail = "The plugin refused to start, so neither call was made."
		emit(ev)
		skip(emit, stepAccessToken, "hop2", labelAccessToken, "intake", "tasking",
			"Not attempted: the plugin client could not be constructed.")
		return
	}

	// Exchange rather than MintResourceToken. Both run the same two calls, and
	// MintResourceToken is now a thin wrapper over Exchange, but Exchange also hands back
	// the intermediate ID-JAG. That assertion is the thing that actually asserts the
	// delegation; the access token is only what it was redeemed for. Showing it is most of
	// the reason this page exists, so the narrower signature would throw away the evidence.
	result, err := client.Exchange(subjectToken, binding)
	if err != nil {
		// Never treat a non-nil result as success: the plugin documents that on error the
		// result may be nil or partial. err is checked first, always.
		if redemptionFailed(result, err) {
			// The assertion succeeded and came back on the partial result, so this step is
			// observed rather than inferred.
			ev = idJAGEvent(stateIssued)
			if result != nil && result.IDJAG != "" {
				ev.Token = describe(kindAssertion, result.IDJAG, cfg.namer())
				ev.Detail = "The exchange succeeded and its assertion is decoded below. " +
					"Redemption of that assertion is what then failed, which is the next " +
					"step. This is the distinction worth having: Okta DID assert this " +
					"delegation, and the target authorization server would not honour it."
			} else {
				// Only reachable if a future Exchange reports a redemption failure without
				// returning the assertion. Say what is known and no more.
				ev.Detail = "The exchange succeeded, but this run did not receive the " +
					"assertion, so there are no claims to show for it."
			}
			emit(ev)

			ev = accessEvent(outcomeState(err))
			ev.Error = failure(err, cfg)
			ev.Detail = explainRefusal(err)
			emit(ev)
			return
		}

		// Nothing was asserted, so there is no assertion to show and nothing to redeem.
		ev = idJAGEvent(outcomeState(err))
		ev.Error = failure(err, cfg)
		ev.Detail = explainRefusal(err)
		emit(ev)
		skip(emit, stepAccessToken, "hop2", labelAccessToken, "intake", "tasking",
			"Not attempted: no ID-JAG to redeem.")
		return
	}

	ev = idJAGEvent(stateIssued)
	ev.Token = describe(kindAssertion, result.IDJAG, cfg.namer())
	ev.Detail = "The assertion itself, decoded. This is where the delegation is asserted, " +
		"so if an act claim appears anywhere in this chain it is most likely to be here. " +
		"Being an assertion rather than an access token, its claim set need not match the " +
		"next step's. Whatever is below is what it carries; nothing is added."
	emit(ev)

	ev = accessEvent(stateIssued)
	ev.Token = describe(kindAccessToken, result.AccessToken, cfg.namer())
	ev.Token.ExpiresIn = time.Until(result.ExpiresAt).Round(time.Second).String()
	ev.Detail = "This is the token that would go upstream, and the only one of the two " +
		"that leaves the gateway. Everything below is decoded from it; nothing is added."
	emit(ev)
}

func skip(emit emitFunc, step, hop, label, actor, target, detail string) {
	emit(stepEvent{
		Step: step, Hop: hop, Label: label,
		Actor: actor, Target: target,
		State: stateSkipped, Detail: detail,
	})
}

// --- Endpoints ------------------------------------------------------------------------
//
// Recomputed here rather than read off the plugin, whose endpoint helpers are unexported.
// They are one-line format strings and they are reported to the UI, not used to call
// anything, so a copy is safer than widening the plugin's API surface for a demo.

func serviceTokenEndpoint(cfg *config) string {
	return fmt.Sprintf("https://%s/oauth2/%s/v1/token", cfg.oktaDomain, cfg.intakeOwnASID)
}

func orgTokenEndpoint(cfg *config) string {
	return fmt.Sprintf("https://%s/oauth2/v1/token", cfg.oktaDomain)
}

func casTokenEndpoint(cfg *config) string {
	return fmt.Sprintf("https://%s/oauth2/%s/v1/token", cfg.oktaDomain, cfg.taskingASID)
}

// mintServiceToken is hop 1, and the only exchange this app performs itself. The plugin
// does not do it, deliberately: registered agents cannot use client_credentials, so the
// first token has to come from something that is not an agent.
func mintServiceToken(cfg *config) (string, error) {
	endpoint := serviceTokenEndpoint(cfg)

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"scope":         {cfg.serviceClientScope},
		"resource":      {cfg.intakeResourceURL},
		"client_id":     {cfg.serviceClientID},
		"client_secret": {cfg.serviceClientSecret},
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sentinel-chain-of-custody/0.1")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("HTTP %d from %s, and the response was not JSON: %w",
			resp.StatusCode, endpoint, err)
	}
	if resp.StatusCode >= 400 || body.AccessToken == "" {
		// Reuse the plugin's error type so both hops surface refusals identically.
		return "", &oktabifrost.OktaError{
			StatusCode:  resp.StatusCode,
			Code:        body.Error,
			Description: body.Description,
		}
	}
	return body.AccessToken, nil
}

// redemptionFailed reports which of hop 2's two calls failed.
//
// It leads with the RETURN VALUE, which is now load-bearing rather than advisory: Exchange
// returns a partial result carrying the assertion when redemption fails, and a nil result
// when the exchange itself failed, because in that case nothing was asserted. Those two
// shapes answer the question outright.
//
// The error wording is kept only as a tiebreaker for the one shape the contract does not
// describe, a non-nil result with no assertion on it. Reading the structure first means a
// reworded error message can no longer silently misattribute a failure; at worst it costs
// the tiebreaker, on a case that should not arise.
func redemptionFailed(result *oktabifrost.ExchangeResult, err error) bool {
	switch {
	case result != nil && result.IDJAG != "":
		return true
	case result == nil:
		return false
	default:
		return strings.Contains(err.Error(), "id-jag redemption")
	}
}

// outcomeState decides whether an error represents a decision Okta made or a call that
// never reached one. Only Okta's own error body earns "refused".
func outcomeState(err error) string {
	var oe *oktabifrost.OktaError
	if errors.As(err, &oe) {
		return stateRefused
	}
	return stateFailed
}

// failure converts an error into something the UI can render, keeping Okta's wording
// intact when the error came from Okta and labelling it as ours when it did not.
//
// SENTINEL_MASK_IDS is applied only to our own wording. A Go transport error embeds the
// URL it tried, so masking it closes the hole that masking the endpoint field opened.
// Okta's own error_description is never touched: verbatim has to mean verbatim, and it
// would not be worth much if it were conditionally rewritten.
func failure(err error, cfg *config) *oktaFailure {
	var oe *oktabifrost.OktaError
	if errors.As(err, &oe) {
		return &oktaFailure{
			Error:       oe.Code,
			Description: oe.Description,
			HTTPStatus:  oe.StatusCode,
			FromOkta:    true,
			Source:      sourceOkta,
		}
	}
	return &oktaFailure{
		Description: maskEndpoint(err.Error(), cfg.oktaDomain, cfg.maskIDs),
		FromOkta:    false,
		Source:      sourceSentinel,
	}
}

// explainRefusal maps a refusal to the object that actually needs changing. Okta's error
// text is precise about what it rejected but not about where the fix lives, and the two
// are often different places.
func explainRefusal(err error) string {
	var oe *oktabifrost.OktaError
	if !errors.As(err, &oe) {
		return "This is not a refusal. The call did not reach Okta, or Okta's answer could " +
			"not be read, so nothing here may be attributed to a policy decision."
	}

	s := err.Error()
	switch {
	case strings.Contains(s, "invalid_scope"):
		return "A permission decision, and the one worth demonstrating. The scope is not " +
			"on the agent's managed CONNECTION for this target. Publishing it on the " +
			"authorization server and its policy is not sufficient. Okta does not " +
			"down-scope, so one ungrantable scope fails the whole request rather than " +
			"yielding the grantable subset."
	case strings.Contains(s, "invalid_target"):
		return "No ACTIVE connection matches the resource sent. Byte-compare the resource " +
			"URL against the connection's resource indicator and confirm the connection " +
			"is ACTIVE rather than staged. This reads like a permission problem and is " +
			"almost always a configuration one."
	case strings.Contains(s, "access_denied"):
		return "Policy evaluation failed. Either the acting agent is deactivated, which " +
			"is the revocation case, or the caller is not a permitted client of this " +
			"authorization server."
	case strings.Contains(s, "subject_token"):
		return "The subject token was rejected. It must be an access token from a CUSTOM " +
			"authorization server carrying a resource-scoped aud; an org authorization " +
			"server token is refused by design."
	case strings.Contains(s, "invalid_client"):
		return "Okta did not accept the client authentication. For hop 1 that is the " +
			"service client's id or secret; for hop 2 it is the agent's private_key_jwt, " +
			"meaning the key does not match the one registered on the agent."
	default:
		return "See docs/RUNBOOK.md, the failure table at the end, for how to read this."
	}
}
