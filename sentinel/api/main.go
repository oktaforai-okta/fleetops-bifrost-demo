// Command sentinel-api runs an agent-to-agent chain of custody against a real Okta
// tenant and streams what happened, one step at a time, to a browser.
//
// The claim it exists to make legible is a narrow one: Okta is the policy decision
// point, and a gateway is only the enforcement point. There is no gateway in this
// program at all. Every decision it displays came from Okta, and every refusal is
// Okta's own wording, because the decision is the thing being demonstrated and a
// gateway sitting in the middle of it would only obscure whose decision it was.
//
// Three principals, two hops:
//
//	Sentinel Watch Service  ->  Sentinel Intake Agent  ->  Sentinel Tasking Agent
//	   (service client)            (agent A)                 (agent B, privileged)
//
// Hop 1 is one call and this program makes it: client_credentials at a custom
// authorization server, with resource set to the Intake Agent's resource URL. Agents
// cannot use client_credentials, which is why the chain must be started by something
// that is not an agent.
//
// Hop 2 is two calls and this program does NOT implement them. It calls the plugin's
// Exchange, so a run that reaches ISSUED is evidence that the plugin's own exchange code
// works. Reimplementing it here would have made a passing run prove nothing.
//
// Exchange rather than the narrower MintResourceToken because it also returns the
// intermediate ID-JAG, and that assertion is what actually asserts the delegation. The
// access token is only what it was redeemed for.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

func main() {
	cfg := loadConfig()

	if cfg.ready() {
		log.Printf("configuration complete")
	} else {
		// Not fatal. The frontend should still be able to draw its idle diagram and show
		// what is missing, which is more useful than a process that will not start.
		log.Printf("configuration incomplete, /api/run will refuse until these are set: %s",
			strings.Join(cfg.missing, ", "))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/healthz", withCORS(cfg, handleHealthz))
	mux.HandleFunc("/api/config", withCORS(cfg, handleConfig(cfg)))
	mux.HandleFunc("/api/run", withCORS(cfg, handleRun(cfg)))

	addr := ":" + cfg.port
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

// withCORS answers preflights and stamps the allowed origin. The frontend is static on
// one host and this API is on another, so every real request is cross-origin.
func withCORS(cfg *config, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", cfg.allowedOrigin)
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleConfig returns the shape of the chain: principal names, resource URLs, scopes.
// Deliberately no tenant host, agent id or authorization server id, so the frontend can
// draw the whole diagram without ever holding a tenant identifier.
func handleConfig(cfg *config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use GET"})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, cfg.view())
	}
}

// handleRun executes the chain and streams one Server-Sent Event per step.
//
// POST rather than GET, because running it mints live tokens and that is not a safe or
// idempotent read. The cost is that the browser cannot use EventSource, which is
// GET-only, so the frontend parses the stream from fetch instead. That is a few lines
// there and the right trade here.
func handleRun(cfg *config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
			return
		}
		if !cfg.ready() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error":   "not configured",
				"missing": cfg.missing,
			})
			return
		}

		mode, err := parseMode(r.URL.Query().Get("mode"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		path, err := parsePath(r.URL.Query().Get("path"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if mode == modeDeny && path == pathOkta && !cfg.canDeny() {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "no refusal run is configured: set SENTINEL_DENY_SCOPES to a scope " +
					"the agent's connection does not grant",
			})
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeJSON(w, http.StatusInternalServerError,
				map[string]string{"error": "this server cannot stream"})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		// Stops an intermediate proxy buffering the stream into one lump at the end,
		// which would defeat the point of streaming.
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		send := func(event string, payload any) {
			b, err := json.Marshal(payload)
			if err != nil {
				log.Printf("could not marshal %s event: %v", event, err)
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
				// Usually the browser navigated away mid-run. Nothing to do about it.
				return
			}
			flusher.Flush()
		}

		states := map[string]string{}
		emit := func(ev stepEvent) {
			states[ev.Step] = ev.State
			// Step and state only. A token value must never reach the log.
			log.Printf("step %s: %s", ev.Step, ev.State)
			send("step", ev)
		}

		if path == pathGateway {
			runGateway(cfg, mode, emit)
		} else {
			runChain(cfg, mode, emit)
		}

		tool, _ := cfg.gatewayCall(mode)
		payload := map[string]any{
			// ok reports only whether the run reached its end successfully. It is
			// deliberately NOT the same question as whether the run did what was asked: a
			// refusal run that is refused has ok false and has succeeded completely. The
			// frontend words the verdict from mode and outcome together, because only it
			// knows which was intended.
			"ok":      states[finalStep(path)] == stateIssued,
			"outcome": outcome(states, path),
			"mode":    string(mode),
			"path":    string(path),
		}
		if path == pathGateway {
			payload["requested_tool"] = tool
		} else {
			payload["requested_scopes"] = cfg.bindingFor(mode).Scopes
		}
		send("done", payload)
	}
}

// stepOrder is the sequence of steps a path emits, in the order they run.
func stepOrder(path runPath) []string {
	if path == pathGateway {
		return []string{stepCallerToken, stepGatewayCall, stepResourceResult}
	}
	return []string{stepWatchToken, stepIDJAG, stepAccessToken}
}

// finalStep is the step whose success means the whole run succeeded.
func finalStep(path runPath) string {
	order := stepOrder(path)
	return order[len(order)-1]
}

// outcome summarises a finished run in one line, naming the step that decided it and
// keeping a refusal distinct from a call that never reached a decision.
func outcome(states map[string]string, path runPath) string {
	if states[finalStep(path)] == stateIssued {
		if path == pathGateway {
			return "the tool call went through: Okta authorized it, Bifrost enforced that " +
				"decision, and the MCP server accepted the token"
		}
		return "issued: the Tasking Agent's authorization server granted a token to the chain"
	}
	for _, step := range stepOrder(path) {
		switch states[step] {
		case stateRefused:
			return "refused at " + step
		case stateFailed:
			return "no decision reached at " + step + ": the call did not complete, which is not a denial"
		}
	}
	return "incomplete"
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("could not write response: %v", err)
	}
}
