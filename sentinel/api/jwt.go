package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// tokenView is everything this API will say about a token. There is deliberately no
// field that could hold the token itself.
type tokenView struct {
	// Kind names the artefact, because the two in this chain are not the same shape. An
	// ID-JAG is an assertion (token_type N_A, expires_in 300) and need not carry an access
	// token's claim set. Without this, five "not on this token" rows on the assertion read
	// as a fault rather than as a different kind of thing.
	Kind string `json:"kind,omitempty"`

	// Preview is the first 12 and last 8 characters. Enough to tell two tokens apart in
	// a screenshot, not enough to use.
	Preview string `json:"preview"`

	IsJWT bool `json:"is_jwt"`

	// Claims is the decoded payload exactly as it arrived, every claim, nothing added and
	// nothing dropped. The frontend renders whatever is in here, including the raw nested
	// act object, so any rendering below can be checked against the source.
	Claims map[string]any `json:"claims,omitempty"`

	// Act is the RFC 8693 act claim, read out. Nil when the token carried no act claim
	// at all, which is the case the UI has to state plainly rather than paper over.
	//
	// This is the one part of the app that must not be guessed. act is what carries the
	// delegation chain and it is not documented in Okta's published pages, so it is
	// reported as found and never assumed.
	Act *actView `json:"act,omitempty"`

	// Principals names the Okta ids that appear in this token's claims, for ids this app
	// was configured with. It is an aid to reading, never a substitute for the id.
	//
	// Every key here is an id already present in Claims, so this field tells the frontend
	// nothing about the tenant that the token did not already carry.
	Principals map[string]principalRef `json:"principals,omitempty"`

	// DecodeError explains why Claims is empty, for the opaque-token case.
	DecodeError string `json:"decode_error,omitempty"`

	ExpiresIn string `json:"expires_in,omitempty"`
}

// principalRef is this app's annotation of one Okta id.
type principalRef struct {
	// Name is the display name from configuration, matched by exact id.
	Name string `json:"name"`

	// Principal is the diagram node the id belongs to: watch, intake or tasking.
	Principal string `json:"principal"`

	// Profile is the sub_profile the TOKEN stated for this id, at whichever level named
	// it. Empty when the token stated none; never filled in from configuration, because
	// the whole value of sub_profile is that the token says it.
	Profile string `json:"sub_profile,omitempty"`
}

// principalNamer resolves an Okta id to the friendly name and diagram node this app was
// configured with. It returns empty strings for any id it was not told about: an
// unrecognised id is shown as the id and nothing is invented for it.
type principalNamer func(id string) (name, principal string)

// The two positions a party can hold in a delegation chain.
const (
	roleSubject = "subject" // the party the work is for
	roleActor   = "actor"   // a party that acted on the subject's behalf
)

// chainEntry is one party in the delegation chain, as the token names it.
type chainEntry struct {
	// Sub is the id verbatim: claims.sub for the subject, then each nested act.sub.
	Sub string `json:"sub"`

	// Profile is the sub_profile sibling of that sub. Okta carries one at every level of
	// the chain, and it is the token's own statement of WHAT KIND of principal this party
	// is: "service", "ai_agent". Read, never inferred. Empty means the level carried none.
	Profile string `json:"sub_profile,omitempty"`

	// Name and Principal are this app's annotations, resolved by exact id match against
	// configuration. Empty for an id this app was not told about.
	Name      string `json:"name,omitempty"`
	Principal string `json:"principal,omitempty"`

	Role string `json:"role"`
}

// actView is the act claim, read out into one entry per delegation hop.
type actView struct {
	Present bool `json:"present"`

	// Chain is the subject first, then each act.sub outward. One entry per DISTINCT hop:
	// see Collapsed.
	Chain []chainEntry `json:"chain,omitempty"`

	// Levels counts the nested act objects on the token, before any collapsing. Reported
	// so the number of entries on screen can always be reconciled against the raw claim,
	// which the frontend also shows in full.
	Levels int `json:"levels"`

	// Collapsed records that the innermost act level restated the subject and was folded
	// away.
	//
	// Okta terminates the chain by naming the subject again as its own actor's delegator:
	// sub=S, act.sub=A, act.act.sub=S. Walking that naively yields S <- A <- S, which
	// reads as three parties and asserts a delegation hop that did not happen. Only a
	// TERMINAL entry identical to the subject is folded, so genuine repetition further up
	// a longer chain is still shown in full.
	Collapsed bool `json:"collapsed"`
}

// describe builds the view for a token. It never returns the token.
//
// kind is a label only. Nothing about the decoding branches on it: both artefacts are
// decoded by exactly the same path, and whatever claims are present are what gets
// rendered. Assuming an assertion's shape would be the same mistake as assuming act.
func describe(kind, raw string, name principalNamer) *tokenView {
	v := &tokenView{Kind: kind, Preview: preview(raw)}

	claims, err := decodeClaims(raw)
	if err != nil {
		// Not a failure of the chain: an authorization server configured to issue opaque
		// tokens is a legitimate setup. It just means there is nothing to show.
		v.DecodeError = err.Error()
		return v
	}

	v.IsJWT = true
	v.Claims = claims
	v.Act = actChain(claims, name)
	v.Principals = namePrincipals(claims, v.Act, name)
	return v
}

// idClaims are the claims whose value is an Okta principal id. Listed explicitly rather
// than scanning every claim for something that looks like an id: naming a value that
// merely collided would be a guess, and guessing is the one thing this app does not do.
var idClaims = []string{"sub", "cid", "client_id"}

// namePrincipals collects the ids in this token that configuration can put a name to.
//
// The chain is read first because it is the only place the token states a sub_profile
// alongside an id. Ids from the flat claims are then added without a profile rather than
// borrowing one, unless the chain already stated it for that same id.
func namePrincipals(claims map[string]any, act *actView, name principalNamer) map[string]principalRef {
	out := map[string]principalRef{}

	add := func(id, profile string) {
		if id == "" {
			return
		}
		friendly, principal := name(id)
		if friendly == "" && principal == "" {
			return // not an id this app was told about; it stays a bare id on screen
		}
		if existing, seen := out[id]; seen && existing.Profile != "" {
			return // the chain already stated a profile for this id; keep it
		}
		out[id] = principalRef{Name: friendly, Principal: principal, Profile: profile}
	}

	if act != nil {
		for _, e := range act.Chain {
			add(e.Sub, e.Profile)
		}
	}
	for _, key := range idClaims {
		id, _ := claims[key].(string)
		add(id, "")
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// maxActDepth bounds the walk. act is attacker-adjacent input, and a self-nesting object
// should not become an infinite loop in a demo that is being screen-shared.
const maxActDepth = 16

// actChain walks the nested act claim outward, collecting one entry per delegation hop.
// It returns nil when there is no act claim at all, which is the case the UI has to state
// plainly rather than paper over.
func actChain(claims map[string]any, name principalNamer) *actView {
	node, ok := claims["act"].(map[string]any)
	if !ok {
		return nil
	}

	view := &actView{Present: true}

	// One reader for every level, so the subject and each actor are read the same way and
	// the top-level sub_profile cannot drift from the nested ones.
	add := func(m map[string]any, role string) {
		sub, _ := m["sub"].(string)
		if sub == "" {
			return
		}
		profile, _ := m["sub_profile"].(string)
		friendly, principal := name(sub)
		view.Chain = append(view.Chain, chainEntry{
			Sub:       sub,
			Profile:   profile,
			Name:      friendly,
			Principal: principal,
			Role:      role,
		})
	}

	add(claims, roleSubject)

	for depth := 0; node != nil && depth < maxActDepth; depth++ {
		view.Levels++
		add(node, roleActor)
		node, _ = node["act"].(map[string]any)
	}

	view.collapseTerminalSubject()
	return view
}

// collapseTerminalSubject folds a trailing entry that only restates the subject.
//
// Deliberately narrow, and narrow is the point. Only the LAST entry is considered, only
// when the FIRST entry is the subject, and only when the two are identical in both id and
// sub_profile. Everything else is left alone:
//
//   - S <- A <- S      collapses to S <- A. Two parties, and the terminator said so.
//   - S <- A <- S <- A stays as it is. The last entry is not the subject, so all four
//     hops are real and all four are shown.
//   - same id, different sub_profile, does NOT collapse. That would be the token
//     contradicting itself, which is worth seeing rather than tidying away.
func (a *actView) collapseTerminalSubject() {
	n := len(a.Chain)
	if n < 2 {
		return
	}
	first, last := a.Chain[0], a.Chain[n-1]
	if first.Role != roleSubject || last.Role != roleActor {
		return
	}
	if last.Sub != first.Sub || last.Profile != first.Profile {
		return
	}
	a.Chain = a.Chain[:n-1]
	a.Collapsed = true
}

// preview returns first12…last8. Short strings are reported as too short rather than
// sliced into something misleading.
func preview(raw string) string {
	const head, tail = 12, 8
	if len(raw) <= head+tail {
		return fmt.Sprintf("(token shorter than %d characters, not previewed)", head+tail+1)
	}
	return raw[:head] + "…" + raw[len(raw)-tail:]
}

// decodeClaims base64url-decodes a JWT payload into a map, so every claim present is
// carried through. A struct here would silently drop anything unexpected, and anything
// unexpected is precisely what is worth seeing.
func decodeClaims(raw string) (map[string]any, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("not a JWT: expected 3 dot-separated segments, got %d", len(parts))
	}

	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, fmt.Errorf("payload is not base64url: %w", err)
	}

	// UseNumber keeps exp and iat as their literal digits. Without it they round-trip
	// through float64 and reach the browser as 1.7568e+09, which looks like a bug in the
	// token rather than in the decoder.
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()

	var claims map[string]any
	if err := dec.Decode(&claims); err != nil {
		return nil, fmt.Errorf("payload is not a JSON object: %w", err)
	}
	return claims, nil
}

// idPattern matches the Okta object ids that identify a specific tenant's objects:
// wlp (workload principal), aus (authorization server), 0oa (application).
var idPattern = regexp.MustCompile(`\b(wlp|aus|0oa)[A-Za-z0-9]{6,}\b`)

// maskEndpoint redacts the tenant host and Okta object ids from a URL, for demos on a
// shared screen. Applied only to the endpoint strings this API reports; claims are never
// masked, because a masked claim would defeat the purpose of showing it.
func maskEndpoint(url, oktaDomain string, on bool) string {
	if !on {
		return url
	}
	if oktaDomain != "" {
		url = strings.ReplaceAll(url, oktaDomain, "<tenant>")
	}
	return idPattern.ReplaceAllString(url, "$1…")
}
