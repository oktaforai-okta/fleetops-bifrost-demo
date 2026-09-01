package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Claims is the part of the access token this server cares about.
//
// Sub and Act are the whole point of the demo. In a machine-context flow Sub is the
// service that initiated the work and Act names the agent that carried it out, so the
// pair answers "who asked, and what acted" for every single tool call. A shared robot
// account can answer neither.
type Claims struct {
	Iss string   `json:"iss"`
	Sub string   `json:"sub"`
	Aud audience `json:"aud"`
	Exp int64    `json:"exp"`
	Iat int64    `json:"iat"`
	Scp []string `json:"scp"`
	Cid string   `json:"cid"`
	Jti string   `json:"jti"`
	Act *Actor   `json:"act,omitempty"`

	// SubProfile is Okta's sub_profile claim, which types the subject as "service" or
	// "ai_agent". It is what makes the delegation self-describing: the token states what
	// KIND of principal each party is, rather than leaving it to be inferred from a
	// naming convention on the id.
	//
	// Not in Okta's published documentation, verified empirically against a live tenant.
	// Treated as optional throughout for that reason: if it ever stops being emitted,
	// output degrades to bare ids rather than breaking.
	SubProfile string `json:"sub_profile,omitempty"`
}

// Actor is the RFC 8693 act claim. It nests: in a multi-hop chain each delegator
// appears inside the previous one, so the full chain of custody is readable from the
// token alone with no log correlation.
type Actor struct {
	Sub        string `json:"sub"`
	SubProfile string `json:"sub_profile,omitempty"`
	Act        *Actor `json:"act,omitempty"`
}

// principal is one party in the delegation chain, with the type Okta assigned it.
type principal struct {
	Sub     string
	Profile string // "service", "ai_agent", or empty when absent
}

// Chain renders the delegation chain, subject first then each actor outward.
//
// Okta terminates the nested act claim by restating the subject as the innermost
// actor's own delegator, so walking act to the end yields one more entry than there are
// principals. On this tenant the raw walk produces
//
//	service  <-  agent  <-  service
//
// for a two-party delegation, which reads as though the service appeared twice and is
// the wrong claim to put on screen: this line is the whole point of the demo.
//
// So a trailing entry is dropped, and ONLY when it is identical to the subject. A
// longer chain that genuinely revisits a principal mid-way is left alone, because
// collapsing duplicates in general would hide a real delegation loop, which is
// something you would want to see rather than tidy away.
// The collapse deliberately lives here, on the raw subject, rather than in Chain below.
// Comparing formatted strings would silently stop matching the moment anything is
// appended to them, and the symptom would be the doubled entry quietly returning.
func (c *Claims) Principals() []principal {
	chain := []principal{{Sub: c.Sub, Profile: c.SubProfile}}
	for a := c.Act; a != nil; a = a.Act {
		chain = append(chain, principal{Sub: a.Sub, Profile: a.SubProfile})
	}
	if n := len(chain); n > 1 && chain[n-1].Sub == c.Sub {
		chain = chain[:n-1]
	}
	return chain
}

// Chain renders the delegation chain for display, as "id (type)" where Okta stamped a
// sub_profile and as the bare id where it did not.
//
// The type is the part worth reading aloud. "service" and "ai_agent" come from the token
// itself, so the claim that one party asked and a different KIND of party acted is
// something the credential states rather than something the audience has to take on
// trust from the shape of an id.
func (c *Claims) Chain() []string {
	ps := c.Principals()
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if p.Profile == "" {
			out = append(out, p.Sub)
			continue
		}
		out = append(out, fmt.Sprintf("%s (%s)", p.Sub, p.Profile))
	}
	return out
}

// HasScope reports whether the token carries a scope.
func (c *Claims) HasScope(want string) bool {
	for _, s := range c.Scp {
		if s == want {
			return true
		}
	}
	return false
}

// audience handles aud arriving as either a string or an array of strings, both of
// which are permitted by the JWT spec and both of which occur in practice.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("aud is neither a string nor an array of strings")
	}
	*a = many
	return nil
}

// Validator verifies access tokens issued by one or more Okta authorization servers.
type Validator struct {
	// issuers are the authorization servers this resource trusts. There is one per
	// lane: read and command are separate authorization servers, so a token from
	// either is legitimate here, and it is the scope rather than the issuer that
	// decides which tool it can reach.
	issuers map[string]bool

	// audiences are the RESOURCE URLs, not the authorization servers' configured
	// audiences values. Okta stamps aud from the RFC 8707 resource parameter on the
	// exchange, so two authorization servers can share an audiences setting and still
	// issue tokens with entirely different aud. Validating against the wrong one of
	// those two is a confusing failure, hence the emphasis.
	//
	// One entry per lane, because each lane addresses its own resource. Sharing one
	// resource indicator across both lanes would make Okta's resource lookup ambiguous.
	audiences map[string]bool

	mu      sync.RWMutex
	keys    map[string]map[string]*rsa.PublicKey // issuer -> kid -> key
	fetched map[string]time.Time                 // issuer -> last successful fetch
	http    *http.Client
}

// NewValidator builds a validator over the trusted issuers and accepted audiences.
func NewValidator(issuers, audiences []string) (*Validator, error) {
	v := &Validator{
		issuers:   map[string]bool{},
		audiences: map[string]bool{},
		keys:      map[string]map[string]*rsa.PublicKey{},
		fetched:   map[string]time.Time{},
		http:      &http.Client{Timeout: 10 * time.Second},
	}
	for _, i := range issuers {
		if i = strings.TrimSpace(strings.TrimSuffix(i, "/")); i != "" {
			v.issuers[i] = true
		}
	}
	for _, a := range audiences {
		if a = strings.TrimSpace(a); a != "" {
			v.audiences[a] = true
		}
	}
	if len(v.issuers) == 0 {
		return nil, fmt.Errorf("at least one trusted issuer is required")
	}
	if len(v.audiences) == 0 {
		return nil, fmt.Errorf("at least one accepted audience is required")
	}
	return v, nil
}

// Issuers returns the trusted issuers, sorted, for logging.
func (v *Validator) Issuers() []string { return sortedKeys(v.issuers) }

// Audiences returns the accepted audiences, sorted, for logging.
func (v *Validator) Audiences() []string { return sortedKeys(v.audiences) }

// Validate checks the signature and the claims, returning the claims on success.
//
// Errors are deliberately specific. "signature does not verify" and "wrong audience"
// are different problems with different fixes, and collapsing them into a generic
// "invalid token" is what makes this class of bug expensive to chase.
func (v *Validator) Validate(raw string) (*Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("not a JWT: expected three segments, got %d", len(parts))
	}

	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &hdr); err != nil {
		return nil, fmt.Errorf("unreadable header: %w", err)
	}
	// Pinning the algorithm is what stops algorithm-confusion, including "none".
	if hdr.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported alg %q, only RS256 is accepted", hdr.Alg)
	}

	// Claims are decoded before signature verification only to learn which issuer to
	// fetch keys from. Nothing is trusted until the signature checks out below.
	var claims Claims
	if err := decodeSegment(parts[1], &claims); err != nil {
		return nil, fmt.Errorf("unreadable claims: %w", err)
	}

	issuer := strings.TrimSuffix(claims.Iss, "/")
	if !v.issuers[issuer] {
		return nil, fmt.Errorf("untrusted issuer %q; this server trusts %s",
			claims.Iss, strings.Join(v.Issuers(), ", "))
	}

	key, err := v.keyFor(issuer, hdr.Kid)
	if err != nil {
		return nil, err
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("unreadable signature: %w", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return nil, fmt.Errorf("signature does not verify against %s", issuer)
	}

	now := time.Now().Unix()
	if claims.Exp != 0 && now >= claims.Exp {
		return nil, fmt.Errorf("token expired at %s",
			time.Unix(claims.Exp, 0).UTC().Format(time.RFC3339))
	}

	matched := false
	for _, a := range claims.Aud {
		if v.audiences[a] {
			matched = true
			break
		}
	}
	if !matched {
		return nil, fmt.Errorf(
			"wrong audience: token is for %v, this server accepts %s "+
				"(note aud comes from the resource parameter on the exchange, not the authorization server's audiences setting)",
			[]string(claims.Aud), strings.Join(v.Audiences(), ", "))
	}

	return &claims, nil
}

// keyFor returns a signing key for an issuer and kid, refreshing that issuer's key set
// on a miss.
func (v *Validator) keyFor(issuer, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	set, haveSet := v.keys[issuer]
	fresh := time.Since(v.fetched[issuer]) < time.Hour
	var key *rsa.PublicKey
	if haveSet {
		key = set[kid]
	}
	v.mu.RUnlock()

	if key != nil {
		return key, nil
	}
	if haveSet && fresh {
		// A kid absent from a recently fetched key set is a genuine mismatch, not
		// staleness. Re-fetching on every miss would let a bad token drive unbounded
		// outbound requests.
		return nil, fmt.Errorf("no signing key %q at %s/v1/keys", kid, issuer)
	}

	if err := v.refresh(issuer); err != nil {
		return nil, err
	}

	v.mu.RLock()
	defer v.mu.RUnlock()
	if key := v.keys[issuer][kid]; key != nil {
		return key, nil
	}
	return nil, fmt.Errorf("no signing key %q at %s/v1/keys", kid, issuer)
}

func (v *Validator) refresh(issuer string) error {
	url := issuer + "/v1/keys"
	resp, err := v.http.Get(url)
	if err != nil {
		return fmt.Errorf("fetching signing keys from %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching signing keys: %s returned HTTP %d", url, resp.StatusCode)
	}

	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("parsing signing keys from %s: %w", url, err)
	}

	set := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err := b64uint(k.N)
		if err != nil {
			continue
		}
		e, err := b64uint(k.E)
		if err != nil {
			continue
		}
		set[k.Kid] = &rsa.PublicKey{N: n, E: int(e.Int64())}
	}
	if len(set) == 0 {
		return fmt.Errorf("no usable RSA keys at %s", url)
	}

	v.mu.Lock()
	v.keys[issuer] = set
	v.fetched[issuer] = time.Now()
	v.mu.Unlock()
	return nil
}

func decodeSegment(seg string, out any) error {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func b64uint(s string) (*big.Int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(raw), nil
}

// bearerFrom pulls the token out of an Authorization header.
func bearerFrom(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", fmt.Errorf("no Authorization header reached this server")
	}
	if !strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return "", fmt.Errorf("Authorization header is not a Bearer token")
	}
	tok := strings.TrimSpace(h[len("bearer "):])
	if tok == "" {
		return "", fmt.Errorf("empty Bearer token")
	}
	return tok, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
