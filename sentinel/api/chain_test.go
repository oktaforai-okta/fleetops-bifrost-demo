package main

import (
	"errors"
	"fmt"
	"testing"

	oktabifrost "github.com/oktaforai-okta/okta-bifrost-plugin/plugin"
)

// redemptionFailed decides which of the two calls in hop 2 gets blamed for a failure.
// Getting it wrong sends whoever is debugging to the wrong object, so it is worth pinning
// down rather than eyeballing.
//
// The property being protected: the decision reads the RETURN VALUE's shape first and only
// falls back to matching the plugin's error wording. That matters because error strings are
// prose and prose gets reworded, whereas the plugin's contract is structural. Exchange
// returns nil when the exchange itself failed, and a partial result carrying the assertion
// when redemption failed. Those two cases are unambiguous without reading any text.
//
// The cases that earn their keep are the two feeding it a deliberately WRONG error string:
// they prove a future reword can only cost the tiebreaker, on a shape that should not occur.
func TestRedemptionFailedPrefersStructureOverErrorText(t *testing.T) {
	const assertion = "eyJhbGciOiJSUzI1NiJ9.YXNzZXJ0aW9u.sig"

	cases := []struct {
		name   string
		result *oktabifrost.ExchangeResult
		err    error
		want   bool
	}{
		{
			name:   "assertion returned: redemption is what failed",
			result: &oktabifrost.ExchangeResult{IDJAG: assertion},
			err:    errors.New("id-jag redemption: invalid_scope: not allowed"),
			want:   true,
		},
		{
			name:   "nil result: nothing was asserted, so the exchange failed",
			result: nil,
			err:    errors.New("id-jag exchange: invalid_client: kid is invalid"),
			want:   false,
		},
		{
			// The error text lies. Structure must win.
			name:   "assertion present but the text blames the exchange",
			result: &oktabifrost.ExchangeResult{IDJAG: assertion},
			err:    errors.New("id-jag exchange: something misleading"),
			want:   true,
		},
		{
			// The error text lies the other way. Structure must win again.
			name:   "nil result but the text blames redemption",
			result: nil,
			err:    errors.New("id-jag redemption: something misleading"),
			want:   false,
		},
		{
			// A shape the plugin's contract does not produce. Falls back to the wording,
			// which is the only signal left.
			name:   "non-nil result with no assertion, wording says redemption",
			result: &oktabifrost.ExchangeResult{},
			err:    errors.New("id-jag redemption: unexpected"),
			want:   true,
		},
		{
			name:   "non-nil result with no assertion, wording says exchange",
			result: &oktabifrost.ExchangeResult{},
			err:    errors.New("id-jag exchange: unexpected"),
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redemptionFailed(tc.result, tc.err); got != tc.want {
				t.Errorf("redemptionFailed = %v, want %v", got, tc.want)
			}
		})
	}
}

// A refusal is a decision Okta made. A transport failure is not. Conflating them claims
// Okta denied something it was never asked, which is the same class of error as drawing a
// delegation chain that is not on the token.
//
// outcomeState decides this with errors.As, so what matters is the error's TYPE somewhere
// in its chain, not its text. The four cases below pin both halves of that: unwrapping
// works, and text alone is not enough.
func TestOutcomeStateDistinguishesRefusalFromNoDecision(t *testing.T) {
	oktaSaidNo := &oktabifrost.OktaError{
		StatusCode:  400,
		Code:        "invalid_scope",
		Description: "The following scopes are not allowed for this request: [task.dispatch].",
	}

	t.Run("a bare Okta error is a refusal", func(t *testing.T) {
		if got := outcomeState(oktaSaidNo); got != stateRefused {
			t.Errorf("got %q, want %q", got, stateRefused)
		}
	})

	// This is the real path, and the case that actually exercises errors.As. The plugin
	// wraps with %w, so the OktaError is still reachable through the wrapper and the
	// outcome must survive being wrapped. If this regressed, every genuine Okta refusal
	// would be misreported as a call that never reached a decision.
	t.Run("an Okta error wrapped with %w is still a refusal", func(t *testing.T) {
		wrapped := fmt.Errorf("id-jag redemption: %w", oktaSaidNo)
		if got := outcomeState(wrapped); got != stateRefused {
			t.Errorf("got %q, want %q: errors.As must see through the plugin's wrapping", got, stateRefused)
		}
	})

	// A lookalike. Same text, no OktaError anywhere in the chain, because errors.New does
	// not wrap. Carrying Okta's wording is not the same as being Okta's decision, and
	// deciding on substrings rather than types is how that distinction gets lost.
	t.Run("a plain error merely quoting Okta is not a refusal", func(t *testing.T) {
		lookalike := errors.New("id-jag redemption: " + oktaSaidNo.Error())
		if got := outcomeState(lookalike); got == stateRefused {
			t.Errorf("got %q: text alone must not qualify as an Okta decision", got)
		}
	})

	t.Run("a transport failure is not a refusal", func(t *testing.T) {
		if got := outcomeState(errors.New(`Post "https://x.invalid": no such host`)); got == stateRefused {
			t.Errorf("got %q, want anything but %q", got, stateRefused)
		}
	})
}
