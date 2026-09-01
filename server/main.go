// Command fleetops is a small MCP server standing in for a real operational system.
//
// It has the shape that makes agent identity matter: cheap reads on one side, and on
// the other a command that moves something in the physical world. Reading telemetry
// and dispatching a vehicle are not the same risk, so they are not protected by the
// same scope, and in this demo they are not even served by the same authorization
// server.
//
// The server validates every token itself. It does not trust the gateway in front of
// it, which is the point: if the gateway were the only thing checking, then bypassing
// the gateway would bypass authorization entirely.
//
// Every tool result ends with the delegation chain read out of the token, because that
// is the thing a shared service account can never show you.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// The scope each tool demands is configurable, because scope names live in the tenant
// rather than in this code. If they are hardcoded here and the tenant publishes
// different ones, this server rejects a token the gateway has just legitimately minted,
// and the failure reads like a gateway bug when it is only a naming mismatch.
//
// The defaults are the original names, so an existing deployment is unaffected.
var (
	scopeTelemetryRead = envOr("FLEETOPS_SCOPE_TELEMETRY_READ", "fleet.telemetry.read")
	scopeRoutesRead    = envOr("FLEETOPS_SCOPE_ROUTES_READ", "fleet.routes.read")
	scopeDispatchCmd   = envOr("FLEETOPS_SCOPE_DISPATCH", "fleet.dispatch.command")
)

// tool describes one callable tool and the scope it demands.
type tool struct {
	Name        string
	Description string
	Scope       string
	Schema      map[string]any
	Handle      func(args map[string]any, c *Claims) (string, error)
}

func main() {
	// Comma-separated, one entry per lane. The read lane and the command lane are
	// separate authorization servers addressing separate resources, so this server
	// trusts both and lets scope decide which tool a token can reach.
	issuers := splitList(mustEnv("FLEETOPS_ISSUERS"))
	audiences := splitList(mustEnv("FLEETOPS_AUDIENCES"))
	addr := envOr("FLEETOPS_ADDR", ":8080")

	fleet := newFleet()
	v, err := NewValidator(issuers, audiences)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	srv := &server{validator: v, tools: buildTools(fleet)}

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", srv.handleMCP)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	log.Printf("fleetops listening on %s", addr)
	log.Printf("  trusted issuers   %s", strings.Join(v.Issuers(), ", "))
	log.Printf("  accepted audiences %s", strings.Join(v.Audiences(), ", "))
	log.Printf("  tools             get_telemetry (%s), list_routes (%s), dispatch_vehicle (%s)",
		scopeTelemetryRead, scopeRoutesRead, scopeDispatchCmd)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

type server struct {
	validator *Validator
	tools     map[string]tool
}

// rpcRequest is the subset of JSON-RPC this server handles.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (s *server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCError(w, nil, -32700, "could not parse request: "+err.Error())
		return
	}

	// Logged for every request, because "did the gateway even reach this server, and did
	// this server accept the token" is the question the whole demo turns on, and silence
	// is indistinguishable from a connection that was never attempted.
	log.Printf("<- %s %s", r.Method, req.Method)

	switch req.Method {
	case "initialize":
		// The handshake is unauthenticated on purpose. A client has to be able to
		// discover the server before it can be told it is not allowed to use it, and
		// nothing is disclosed here beyond the server's own name.
		writeRPCResult(w, req.ID, map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "fleetops", "version": "0.1.0"},
		})

	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)

	case "tools/list":
		writeRPCResult(w, req.ID, map[string]any{"tools": s.toolList()})

	case "tools/call":
		s.handleToolCall(w, r, req)

	default:
		writeRPCError(w, req.ID, -32601, "unknown method "+req.Method)
	}
}

func (s *server) toolList() []map[string]any {
	// Listing is not gated. A listed tool still cannot run without the right scope, so
	// hiding names buys nothing and makes the server harder to work with. The scope
	// each tool demands is published in its description, which is honest and saves a
	// round of guessing.
	out := make([]map[string]any, 0, len(s.tools))
	for _, name := range []string{"get_telemetry", "list_routes", "dispatch_vehicle"} {
		t := s.tools[name]
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": fmt.Sprintf("%s (requires scope %s)", t.Description, t.Scope),
			"inputSchema": t.Schema,
		})
	}
	return out
}

func (s *server) handleToolCall(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(w, req.ID, -32602, "bad params: "+err.Error())
		return
	}

	t, ok := s.tools[params.Name]
	if !ok {
		writeRPCError(w, req.ID, -32602, "unknown tool "+params.Name)
		return
	}

	// Authorization happens here, in the resource server, not only in the gateway.
	raw, err := bearerFrom(r)
	if err != nil {
		log.Printf("   tools/call %s DENIED: %v", t.Name, err)
		writeToolError(w, req.ID, "unauthorized: "+err.Error())
		return
	}

	claims, err := s.validator.Validate(raw)
	if err != nil {
		log.Printf("   tools/call %s DENIED, token rejected: %v", t.Name, err)
		writeToolError(w, req.ID, "token rejected: "+err.Error())
		return
	}

	if !claims.HasScope(t.Scope) {
		log.Printf("   tools/call %s DENIED, needs %s, token carries [%s]",
			t.Name, t.Scope, strings.Join(claims.Scp, " "))
		writeToolError(w, req.ID, fmt.Sprintf(
			"forbidden: %s requires scope %s, this token carries [%s]",
			t.Name, t.Scope, strings.Join(claims.Scp, " "),
		))
		return
	}

	// The accepted case, with the delegation chain that authorized it. This is the line
	// worth putting on screen: it names the acting agent, which a shared service account
	// could never distinguish.
	log.Printf("   tools/call %s ACCEPTED, scope %s, chain %s, jti %s",
		t.Name, t.Scope, strings.Join(claims.Chain(), " <- "), claims.Jti)

	body, err := t.Handle(params.Arguments, claims)
	if err != nil {
		writeToolError(w, req.ID, err.Error())
		return
	}

	writeRPCResult(w, req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": body + attribution(claims)}},
		"isError": false,
	})
}

// attribution renders who authorized this call. This is the payoff: on a shared service
// account every line below would be identical for every agent in the fleet.
func attribution(c *Claims) string {
	var b strings.Builder
	b.WriteString("\n\n--- authorized by Okta ---\n")
	b.WriteString("delegation chain : " + strings.Join(c.Chain(), "  <-  ") + "\n")
	if c.Act == nil {
		b.WriteString("                   (no act claim: this token names no acting agent)\n")
	}
	b.WriteString("scopes granted   : " + strings.Join(c.Scp, " ") + "\n")
	b.WriteString("audience         : " + strings.Join(c.Aud, " ") + "\n")
	b.WriteString("token id         : " + c.Jti + "\n")
	if c.Exp != 0 {
		b.WriteString("expires          : " + time.Unix(c.Exp, 0).UTC().Format(time.RFC3339) +
			fmt.Sprintf(" (in %s)\n", time.Until(time.Unix(c.Exp, 0)).Round(time.Second)))
	}
	return b.String()
}

func buildTools(f *fleet) map[string]tool {
	noArgs := map[string]any{"type": "object", "properties": map[string]any{}}

	return map[string]tool{
		"get_telemetry": {
			Name:        "get_telemetry",
			Description: "Current position, fuel and status for one vehicle",
			Scope:       scopeTelemetryRead,
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"vehicle_id": map[string]any{"type": "string", "description": "e.g. FL-114"},
				},
				"required": []string{"vehicle_id"},
			},
			Handle: func(args map[string]any, _ *Claims) (string, error) {
				id, _ := args["vehicle_id"].(string)
				return f.telemetry(id)
			},
		},

		"list_routes": {
			Name:        "list_routes",
			Description: "All routes currently assigned across the fleet",
			Scope:       scopeRoutesRead,
			Schema:      noArgs,
			Handle: func(_ map[string]any, _ *Claims) (string, error) {
				return f.routes(), nil
			},
		},

		"dispatch_vehicle": {
			Name:        "dispatch_vehicle",
			Description: "Send a vehicle to a destination. This moves a real asset",
			Scope:       scopeDispatchCmd,
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"vehicle_id":  map[string]any{"type": "string", "description": "e.g. FL-114"},
					"destination": map[string]any{"type": "string", "description": "e.g. Depot 4"},
				},
				"required": []string{"vehicle_id", "destination"},
			},
			Handle: func(args map[string]any, c *Claims) (string, error) {
				id, _ := args["vehicle_id"].(string)
				dest, _ := args["destination"].(string)
				// The acting agent is recorded on the dispatch, so the audit trail says
				// which agent moved the asset rather than which gateway relayed it.
				actor := "unknown"
				if c.Act != nil {
					actor = c.Act.Sub
				}
				return f.dispatch(id, dest, c.Sub, actor)
			},
		},
	}
}

// fleet is the toy state this server serves.
type fleet struct {
	mu       sync.Mutex
	vehicles map[string]*vehicle
	log      []string
}

type vehicle struct {
	ID     string
	Status string
	Route  string
	Fuel   int
	Lat    float64
	Lon    float64
}

func newFleet() *fleet {
	return &fleet{vehicles: map[string]*vehicle{
		"FL-114": {ID: "FL-114", Status: "idle", Route: "none", Fuel: 82, Lat: 47.6062, Lon: -122.3321},
		"FL-207": {ID: "FL-207", Status: "en route", Route: "Depot 2 to Yard 9", Fuel: 46, Lat: 47.6205, Lon: -122.3493},
		"FL-330": {ID: "FL-330", Status: "maintenance", Route: "none", Fuel: 12, Lat: 47.5952, Lon: -122.3316},
	}}
}

func (f *fleet) telemetry(id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.vehicles[id]
	if !ok {
		return "", fmt.Errorf("no such vehicle %q; known vehicles are FL-114, FL-207, FL-330", id)
	}
	return fmt.Sprintf("vehicle %s\n  status : %s\n  route  : %s\n  fuel   : %d%%\n  position: %.4f, %.4f",
		v.ID, v.Status, v.Route, v.Fuel, v.Lat, v.Lon), nil
}

func (f *fleet) routes() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	b.WriteString("assigned routes\n")
	for _, id := range []string{"FL-114", "FL-207", "FL-330"} {
		v := f.vehicles[id]
		b.WriteString(fmt.Sprintf("  %-7s %-12s %s\n", v.ID, v.Status, v.Route))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (f *fleet) dispatch(id, dest, onBehalfOf, actor string) (string, error) {
	if dest == "" {
		return "", fmt.Errorf("destination is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.vehicles[id]
	if !ok {
		return "", fmt.Errorf("no such vehicle %q; known vehicles are FL-114, FL-207, FL-330", id)
	}
	if v.Status == "maintenance" {
		return "", fmt.Errorf("vehicle %s is in maintenance and cannot be dispatched", id)
	}

	v.Status = "en route"
	v.Route = "dispatch to " + dest
	entry := fmt.Sprintf("%s  DISPATCH %s -> %s  on behalf of %s, acted by %s",
		time.Now().UTC().Format(time.RFC3339), id, dest, onBehalfOf, actor)
	f.log = append(f.log, entry)

	return fmt.Sprintf("dispatched %s to %s\n  audit: %s", id, dest, entry), nil
}

// --- JSON-RPC plumbing ---

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rawOrNull(id), "result": result})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": rawOrNull(id),
		"error": map[string]any{"code": code, "message": msg},
	})
}

// writeToolError returns an authorization failure as a tool result with isError set,
// not as a JSON-RPC error. MCP clients surface tool errors to the model and the user,
// whereas a protocol error tends to be swallowed as a transport fault. The refusal is
// the thing worth reading, so it must be visible.
func writeToolError(w http.ResponseWriter, id json.RawMessage, msg string) {
	writeRPCResult(w, id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	})
}

func rawOrNull(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("%s is required", k)
	}
	return v
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// splitList parses a comma-separated env var, dropping empties so a trailing comma or
// stray whitespace does not become a silently-trusted empty issuer.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
