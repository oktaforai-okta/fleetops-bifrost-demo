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
	// nothing dropped. The frontend renders whatever is in here.
	Claims map[string]any `json:"claims,omitempty"`

	// ActPresent records whether an RFC 8693 act claim was actually on the token.
	//
	// This is the one field in the app that must not be guessed. act is what would carry
	// the delegation chain, it is not documented in Okta's published pages, and it has
	// never been observed from this tenant. So it is reported as found, and when it is
	// absent the frontend says so rather than drawing a chain that the token does not
	// support.
	ActPresent bool `json:"act_present"`

	// ActChain is sub, then each nested act.sub outward, and is populated only when
	// ActPresent is true.
	ActChain []string `json:"act_chain,omitempty"`

	// DecodeError explains why Claims is empty, for the opaque-token case.
	DecodeError string `json:"decode_error,omitempty"`

	ExpiresIn string `json:"expires_in,omitempty"`
}

// describe builds the view for a token. It never returns the token.
func describe(raw string) *tokenView {
	v := &tokenView{Preview: preview(raw)}

	claims, err := decodeClaims(raw)
	if err != nil {
		// Not a failure of the chain: an authorization server configured to issue opaque
		// tokens is a legitimate setup. It just means there is nothing to show.
		v.DecodeError = err.Error()
		return v
	}

	v.IsJWT = true
	v.Claims = claims

	if chain, ok := actChain(claims); ok {
		v.ActPresent = true
		v.ActChain = chain
	}
	return v
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

// actChain walks the nested act claim outward, collecting one sub per delegation hop.
// It reports false when there is no act claim at all, which is the case the UI has to
// state plainly rather than paper over.
func actChain(claims map[string]any) ([]string, bool) {
	node, ok := claims["act"].(map[string]any)
	if !ok {
		return nil, false
	}

	chain := []string{}
	if sub, ok := claims["sub"].(string); ok && sub != "" {
		chain = append(chain, sub)
	}

	// Bounded rather than while-true: act is attacker-adjacent input, and a self-nesting
	// object should not become an infinite loop in a demo that is being screen-shared.
	for depth := 0; node != nil && depth < 16; depth++ {
		if sub, ok := node["sub"].(string); ok && sub != "" {
			chain = append(chain, sub)
		}
		node, _ = node["act"].(map[string]any)
	}
	return chain, true
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
