# Fleet Ops

A runnable demonstration that **Okta can be the policy decision point** for AI agent
tool calls while **Bifrost remains the enforcement point**, without forking Bifrost and
without replacing the gateway.

Fleet Ops is a deliberately small stand-in for a real operational system. It has the
shape that makes agent identity matter: cheap reads on one side, and on the other a
command that moves a physical asset. Reading telemetry and dispatching a vehicle are not
the same risk, so here they are not protected by the same scope, and not even served by
the same authorization server.

Everything runs on one host. No cloud dependency, no frontend, nothing to keep warm.

## What this proves

Three things, in order of how much they matter.

**1. A revoked agent is refused at the gateway.** Call `dispatch_vehicle` and it works.
Deactivate the agent in Okta. Call it again and Bifrost refuses, while the connection is
still open and the token it holds has not expired. This is the one that matters, because
an issued bearer token cannot be withdrawn. Something has to ask.

**2. Per-action separation is enforced by Okta, not asserted by the app.** The read lane
and the command lane are separate authorization servers. Asking for a command scope on
the read lane fails at the lane boundary, and the refusal is Okta's own words rather than
a message this demo invented.

**3. Every call names both the caller and the agent.** Each tool result ends with the
delegation chain read straight out of the token. On a shared service account, every one
of those lines would be identical for every agent in the fleet.

## Quickstart

The plugin lives in a sibling repo, because it is reusable and this demo is not.

```bash
git clone https://github.com/oktaforai-okta/okta-bifrost-plugin
git clone https://github.com/oktaforai-okta/fleetops-bifrost-demo

cd fleetops-bifrost-demo
cp .env.example .env      # fill in from your own tenant, see docs/RUNBOOK.md
make up
```

Then point a client at it:

```bash
claude mcp add --transport http fleetops http://localhost:8080/mcp
```

`make logs` follows the gateway and the server. `make revoke` prints the console path for
the punchline.

Okta setup is not optional and not guessable. Work through
[docs/RUNBOOK.md](docs/RUNBOOK.md) first.

## The three tools

| Tool | Lane | Required scope |
|---|---|---|
| `get_telemetry` | read | `fleet.telemetry.read` |
| `list_routes` | read | `fleet.routes.read` |
| `dispatch_vehicle` | command | `fleet.dispatch.command` |

Listing is not gated. A listed tool still cannot run without the right scope, so hiding
names buys nothing and makes the server harder to work with. Each tool publishes the
scope it demands in its own description.

## How it fits together

```
  calling service                Bifrost                    Fleet Ops
  (client_credentials)      (enforcement point)            (resource server)
         |                          |                             |
         |-- token, aud = agent --->|                             |
         |                          |                             |
         |                   [okta-agent-identity]                 |
         |                     connect: mint  --------> Okta       |
         |                     each call: re-ask -----> Okta       |
         |                          |                             |
         |                          |-- Bearer, sub + act ------->|
         |                          |                             | validates
         |                          |                             | iss, aud,
         |                          |                             | exp, scope
```

The gateway enforces. Okta decides. And the Fleet Ops server **validates the token
itself** rather than trusting the gateway, because if the gateway were the only thing
checking then bypassing the gateway would bypass authorization entirely.

## Why the per-call check exists

A Bifrost MCP request carries no headers. Headers are only mutable when a connection is
established, so a token can only be attached at connect time. An issued bearer token
cannot be withdrawn.

So the plugin does two different things at two different moments: it **mints** at
connect, and it **re-asks Okta** on every tool call. The second is not redundant. It is
the only thing standing between a deactivated agent and a connection holding a token
that is still technically valid.

One limitation, stated rather than hidden: this catches revocation, not scope narrowing.
The token in flight still carries the scopes it was granted at connect, so tightening a
connection's scope list takes effect on the next connection rather than the next call.

## What is deliberately not here

- **No frontend.** A terminal showing the real JSON-RPC refusal and the real decoded
  token is harder to wave away than a web page.
- **No per-object authorization.** A scope can say an agent may dispatch. It cannot say
  whether it may dispatch *this particular* vehicle. That belongs in a fine-grained
  authorization layer above this one.
- **No DPoP.** Tokens are bearer tokens.
- **No production claims.** Fleet state is in memory and resets when the container does.

## Licence

Apache 2.0.
