# Okta setup runbook

Everything here happens once, in your own tenant. Roughly 45 minutes the first time.

Values you collect go into `.env`. Nothing from this runbook belongs in a committed file.

The steps are sequenced so each one's output is the next one's input. Skipping ahead mostly
produces errors that point at the wrong object.

---

## 0. Decide which topology you are building

There are two shapes, and they need different numbers of authorization servers. Getting this
wrong is the most expensive mistake available here, because the symptom is a misleading error
on a different object.

| Shape | What the agent reaches | Authorization servers |
|---|---|---|
| **Agent to agent.** What this demo runs, and what is proven live | another registered agent | **two**, one per invoked agent |
| **Agent to API.** The variant, kept in the appendix | a resource server behind read and command lanes | **three** |

**This runbook builds the agent-to-agent shape.** The three-server layout is in
[Appendix A](#appendix-a-the-agent-to-api-variant), clearly marked, because it is a
legitimate variant and some of the repo's variable names still carry its vocabulary.

### The three parties in the agent-to-agent shape

| Role | What it is | Where its values go |
|---|---|---|
| **Caller Service** | An API Services app. Starts the chain | `OKTA_SERVICE_CLIENT_ID`, `OKTA_SERVICE_CLIENT_SECRET` |
| **Acting Agent** | The agent Bifrost authenticates as. Does the delegating | `OKTA_AGENT_ID`, `OKTA_AGENT_RESOURCE_URL`, `OKTA_AGENT_OWN_AS_ID`, key in `secrets/` |
| **Target** | The invoked agent, whose authorization server issues the final token. The MCP server in this repo stands in for its protected surface | `OKTA_COMMAND_LANE_AS_ID`, `FLEETOPS_COMMAND_RESOURCE_URL` |

> **A naming wart, stated so it does not confuse you.** Those last two variables still say
> `COMMAND_LANE` and `FLEETOPS_COMMAND_`, from the agent-to-API design. In this topology they
> name the **Target's** authorization server and resource URL. `OKTA_READ_LANE_AS_ID` and
> `FLEETOPS_READ_RESOURCE_URL` are still validated by `scripts/render-config.sh` but are no
> longer substituted into the config template, so they need a value and that value has no
> effect. Renaming them is a pending cleanup.

### The three steps, and who performs each

| Step | Who | What |
|---|---|---|
| 1 | Caller Service | `client_credentials` at the **Acting Agent's own** authorization server, with `resource` set to the Acting Agent's resource URL |
| 2 | Acting Agent, via the plugin | Exchange that token at the **org** authorization server for an ID-JAG |
| 3 | Acting Agent, via the plugin | Redeem the ID-JAG at the **Target's** authorization server for the access token that goes upstream |

Step 1 cannot happen in the plugin. **Registered agents are not permitted the
`client_credentials` grant at all**, only token-exchange and jwt-bearer, which is exactly why
something that is not an agent has to start the chain.

Step 2 goes to the **org** authorization server, whose token endpoint has no authorization
server id in its path. That is the only place an ID-JAG can be obtained.

---

## 1. Check the feature is on

**Settings > Features**, find **Secure AI A2A Servers**, enable it.

Hard prerequisite. Without it the exchange in step 2 fails outright, and the error will not
mention a feature flag. Confirm it first, because everything below depends on it and it costs
ten seconds.

---

## 2. Create the two authorization servers

**Security > API > Authorization Servers > Add Authorization Server**, twice. One per agent.

| | Protects | Who mints there | Audience |
|---|---|---|---|
| Acting Agent's server | the Acting Agent, as a resource | the Caller Service, via `client_credentials` | the Acting Agent's resource URL |
| Target's server | the Target, as a resource | the Acting Agent, by redeeming the ID-JAG | the Target's resource URL |

Two facts about the audience value, both of which cost time if you meet them cold:

- **The audience and the agent's resource URL are the same string.** One value per agent,
  used in both places.
- **The console requires an `https://` URL** and rejects the `api://` style, even though
  older objects in the same tenant use it. Use `https://`.

The URLs are identifiers. Nothing ever fetches them, so they do not have to resolve.

Record the ids as `OKTA_AGENT_OWN_AS_ID` and `OKTA_COMMAND_LANE_AS_ID`.

### Scopes

On the **Acting Agent's server**, add the scope the Caller Service will ask for, for example
`agent.invoke`. Record it as `OKTA_SERVICE_CLIENT_SCOPE`.

On the **Target's server**, add every scope the Target's tools demand. This demo uses:

| Scope | Purpose |
|---|---|
| `task.read` | reads. **Granted** to the agent |
| `task.dispatch` | the command that moves an asset. **Deliberately never granted** |

`task.dispatch` exists on the server and is withheld on the connection in step 6. That
separation is the demo: the same agent asks for both and Okta permits one.

### Access policies and rules

Each server needs **Access Policies > Add Policy**, then **Add Rule** on that policy. Four
things here are easy to get wrong.

**The `clients` condition lives at the POLICY level, not on the rule.** And it must name the
**caller**, the party presenting credentials at that endpoint.

| On this server | The client to list |
|---|---|
| Acting Agent's server | the **Caller Service** |
| Target's server | the **Acting Agent** |

**A rule's `people.groups` must not be a specific user group.** An agent is a workload
principal, not a user, so it will never match one and the rule will never fire. Use
**EVERYONE**.

> That is permissive, and it is worth tightening before production. It is written this way
> here because the alternative is a rule that silently never matches, which is a worse thing
> to hand someone learning the flow.

**Grant types differ between the two servers.**

| Server | Grant type on the rule | Why |
|---|---|---|
| Acting Agent's | **Client Credentials** | a service client minting for itself |
| Target's | **JWT Bearer** | the agent redeeming an assertion |

**List the scopes explicitly** rather than using a wildcard. A wildcard works, but it hides
which server grants what, which is the thing you are trying to demonstrate.

---

## 3. Register both agents

**Directory > AI Agents > Register AI Agent**, twice: the Acting Agent and the Target.

For each:

- Give it a name and description.
- **Assign owners.** Not optional: activation fails with an opaque `E0000001` until an owner
  exists, and the owners endpoint returns 405 so this cannot be done over the API. **Okta
  recommends at least two.**
- Generate a key pair under client credentials.

**The private key is shown exactly once.** Save the Acting Agent's immediately, as the JSON
object Okta shows you. If you miss it, generate a new key pair rather than trying to recover
the old one. Only the Acting Agent's key is needed by this demo, since the Target never
authenticates here.

Save it as `secrets/sentinel-intake-key.jwk`. That is the one canonical name, used by
`bifrost/config.template.json`, by `scripts/render-config.sh`, and by the `make demo-*` targets
through the Makefile's `AGENT_KEY_FILE` variable.

```bash
cp /path/to/downloaded.jwk secrets/sentinel-intake-key.jwk
chmod 600 secrets/sentinel-intake-key.jwk
```

`secrets/` is gitignored apart from its README.

If you register a JWK over the API instead, it **must carry `use: "sig"`** or it 400s with
"Key 'use' must be 'sig'".

Record the Acting Agent's `wlp...` id as `OKTA_AGENT_ID`.

### Activating

Available over the API, contrary to some older notes:

```
POST /workload-principals/api/v1/ai-agents/{id}/lifecycle/activate
```

Returns 202. Only merge-patching `status` fails.

---

## 4. Register each agent as a resource

This is the step that is genuinely hard to find, and it is **console-only**. The API returns
405 on every verb and body shape.

**Directory > AI Agents > (agent) > Delegations > Non-human identity > Configure.**

It is **not** called "register as an A2A server", which is why searching for that phrase
finds nothing. That single action is what creates the registration.

Pick the authorization server from step 2, then set **Audience / resource URL**.

Two things about this step:

- **The resource URL is immutable once saved.** It cannot be edited, only deleted and
  recreated. The authorization server *can* be changed later, after removing callers. Choose
  the URL deliberately.
- **It blocks everything downstream.** The resource connection and, if you use one, the
  delegation link both fail until this exists. The connection reports `A2A server not found`;
  the delegation link reports a misleading `INVALID_FORMAT` on its
  `authorizationServerOrn`, which is really this same missing prerequisite rather than a
  malformed ORN.

Record the Acting Agent's as `OKTA_AGENT_RESOURCE_URL` and the Target's as
`FLEETOPS_COMMAND_RESOURCE_URL`.

---

## 5. Create the Caller Service

**Applications > Create App Integration > API Services.**

This exists because registered agents cannot use `client_credentials`. Something else has to
mint the first token, and that something stands in for the scheduler or pipeline that would
trigger the agent in production.

Two things to wire up:

1. Grant it access to the **Acting Agent's** authorization server from step 2, so it can mint
   there with `agent.invoke`.
2. Add it as a permitted caller on the **Acting Agent**, so it is allowed to delegate to it.
   In the console this is the agent's inbound callers list, which you may also see labelled
   Linked Applications.

Record the client id and secret as `OKTA_SERVICE_CLIENT_ID` and `OKTA_SERVICE_CLIENT_SECRET`.

The secret **is** retrievable later, at `GET /api/v1/apps/{appId}/credentials/secrets`,
unmasked. `GET /api/v1/apps/{appId}` omits the field entirely, which is what makes it look
unavailable.

---

## 6. Connect the Acting Agent to the Target

On the **Acting Agent**: **Resource connections > Add resource connection**. Resource type
**Authorization server**, pointing at the **Target's** server.

Use **Only allow** and list the scopes, rather than **Allow all**:

| Grant | Withhold |
|---|---|
| `agent.invoke`, `task.read` | `task.dispatch` |

**This is the step people get wrong.** The scope list on the **connection** is what an
exchange is validated against, not only the authorization server's policy rule. A server
fully configured for a scope, with that scope on its policy, is still refused if the
connection does not list it:

```
400 invalid_scope
"The following scopes are not allowed for this request: [task.dispatch]."
```

which reads like a server misconfiguration and sends you to the wrong object. When you see
it, **check the connection.**

That refusal is not a bug here. It is the demonstration, and it is exactly what running
`dispatch_vehicle` produces.

**Okta does not down-scope.** An ungrantable scope fails the whole request rather than
returning the grantable subset, so there is no partial success to accidentally accept.

To change connection scopes over the API:

```
PATCH /workload-principals/api/v1/ai-agents/{wlp}/connections/{mcn}
Content-Type: application/merge-patch+json
```

```json
{ "scopes": ["agent.invoke", "task.read"], "scopeCondition": "INCLUDE_ONLY" }
```

- `application/json` and `application/json-patch+json` both return `E0000021`. `PUT` returns
  405.
- The body must include `scopeCondition` alongside `scopes`, or you get `E0000001`,
  "Resource connection validation failed".

**The connection must be ACTIVE.** A staged connection produces `invalid_target`, which looks
like a typo in a URL.

Deleting a connection needs deactivation first: `DELETE` on an ACTIVE connection returns 409.

---

## 7. Fill in .env

```bash
cp .env.example .env
```

| Variable | Value |
|---|---|
| `OKTA_DOMAIN` | tenant hostname, **no scheme** |
| `OKTA_SERVICE_CLIENT_ID` / `_SECRET` | the Caller Service, step 5 |
| `OKTA_SERVICE_CLIENT_SCOPE` | the scope on the Acting Agent's server, e.g. `agent.invoke` |
| `OKTA_AGENT_OWN_AS_ID` | the Acting Agent's authorization server, step 2 |
| `OKTA_AGENT_ID` | the Acting Agent's `wlp...` id, step 3 |
| `OKTA_AGENT_RESOURCE_URL` | the Acting Agent's resource URL, step 4 |
| `OKTA_COMMAND_LANE_AS_ID` | the **Target's** authorization server |
| `FLEETOPS_COMMAND_RESOURCE_URL` | the **Target's** resource URL |
| `OKTA_READ_LANE_AS_ID`, `FLEETOPS_READ_RESOURCE_URL` | validated but unused in this topology. Set them to the Target's values |
| `FLEETOPS_ISSUERS` | `https://<domain>/oauth2/<Target AS id>` |
| `FLEETOPS_AUDIENCES` | the Target's resource URL |
| `FLEETOPS_SCOPE_TELEMETRY_READ`, `_ROUTES_READ` | `task.read` |
| `FLEETOPS_SCOPE_DISPATCH` | `task.dispatch` |
| `BIFROST_IMAGE` | your **dynamically-linked** Bifrost. See the main README, Step 1 |
| `EXTRA_CA_BUNDLE` | only on networks that intercept TLS |

Three traps worth internalising.

**The resource URLs must match byte for byte.** A hyphen where there should be a colon, a
trailing slash, or a case difference all produce `invalid_target`.

**`aud` comes from the `resource` parameter on the exchange,** not from the authorization
server's `audiences` field. Two servers can share an `audiences` value and still issue tokens
with completely different `aud`. The MCP server validates against the resource URL, which is
the correct one of those two.

**The agent key never goes in `.env`.** It lives in `secrets/`, read directly by the plugin.
A JWK pasted unquoted into an env file is destroyed the moment that file is sourced by a
shell: the quotes are stripped, the commas become brace expansion. Keeping it in a file also
means the rendered Bifrost config is not itself a secret. `.env` in general is not
shell-sourceable; use `scripts/load-env.sh`, which parses it the way docker does.

---

## 8. Run it

### First, without the gateway

Start with the driver. It exercises the exchange with nothing else in the way, so a
misconfiguration shows you Okta's own answer rather than a gateway's interpretation of it.

```bash
make demo             # both outcomes in one run
```

```
lane read     ISSUED    chain 0oa135... <- wlp135...   scopes task.read agent.invoke
lane command  REFUSED   invalid_scope [task.dispatch]
```

The driver reads the same `FLEETOPS_SCOPE_*` variables as the MCP server, and targets the same
authorization server and resource as the gateway config, so it cannot drift from what Bifrost
does. Individual targets:

| Target | Expected outcome |
|---|---|
| `make demo-read` | **ISSUED.** A token naming the Caller Service and the Acting Agent |
| `make demo-command` | **REFUSED.** The connection does not grant dispatch. This is correct, not a broken target |
| `make demo-deny` | **REFUSED.** Asks the read lane for an ungranted scope explicitly |

On a success, the line worth reading is the delegation chain: the Caller Service as subject,
the Acting Agent as actor. If the actor is absent, the `act` claim did not come back and the
central claim of the demo is not being made. Stop and fix that before going further.

> `act`, and the `sub_profile` that types each party as `service` or `ai_agent`, are **not in
> Okta's published developer documentation.** Both were verified empirically by decoding real
> tokens from this tenant. Present them that way: verified, not documented.

### Then through the gateway

```bash
make up
make logs
```

This is the coexistence half of the argument: Okta decides, Bifrost enforces.

```bash
TOKEN=$(./scripts/get-caller-token.sh)
```

Then call the two tools. See the main README for the verification tiers and the expected
outcomes. In short: `get_telemetry` succeeds, `dispatch_vehicle` is refused by name, and
nothing changes in the tenant between them.

> ### Never `docker compose restart`. Always `down -v`.
>
> **Tools are discovered only on FIRST registration.** On a restart, Bifrost reloads its MCP
> clients from sqlite **without re-running discovery**, so `/api/mcp/clients` still reports all
> three tools while the live registry is empty and every call 404s with `tool not found`.
>
> There is no log line saying discovery was skipped. The symptom points nowhere near the
> cause, and you will check the plugin, Okta and your bindings first, and all three will be
> fine.
>
> ```bash
> make clean && make up     # down -v, then rebuild and start
> ```
>
> Do this after **every** config change.

### The revocation argument

Deactivating an agent is caught by the plugin's per-call check, because Okta stops issuing and
the plugin asks Okta again on every call. `make revoke` prints the console path.

`agent_status_ttl` bounds how stale that answer can be. It ships at `10s`, so a deactivation
can take up to ten seconds to bite.

That number is a demo default, not a tuned production value, and it does two jobs at once: it
is the cache lifetime **and** the revocation staleness bound. **Raising it widens the window in
which a deactivated agent still passes.** See the main README before changing it.

That is the architectural reason the per-call check exists, and it is what the code does. It
is worth stating separately from the scope refusal above, which is the pair actually proven
end to end on a live tenant.

A sharper version of the same idea, worth having ready: the token the Target's server issues
is an ordinary Okta access token, so it can be revoked directly rather than by deactivating
the whole agent. Useful if someone asks whether the only control is an all-or-nothing kill
switch.

---

## Reading a failure

| Symptom | Cause |
|---|---|
| `400 invalid_scope`, naming a scope | The **connection** does not list it. Check the connection, not the authorization server |
| `400 invalid_target` | No **ACTIVE** connection matches `resource`. Byte-compare the URL, and check the connection is active rather than staged |
| `401 access_denied`, policy evaluation failed | The caller is not a listed client of that authorization server, or the acting agent is deactivated. Remember the `clients` condition is at policy level and names the caller |
| A rule that never seems to match | `people.groups` names a user group. An agent is a workload principal. Use EVERYONE |
| `'subject_token' is invalid` | The caller presented an ID token, or a token from the **org** authorization server. It must be an access token from a **custom** server with a resource-scoped `aud` |
| `invalid_client` | Step 1: the Caller Service's id or secret. Steps 2 and 3: the agent's key does not match the one registered on the agent |
| `A2A server not found` on a connection | Step 4 has not been done for that agent |
| `INVALID_FORMAT` on `authorizationServerOrn` | Also step 4. The ORN is fine; the registration is missing |
| `E0000001` on activation | The agent has no owner |
| `E0000021` on a connection PATCH | Wrong content type. It must be `application/merge-patch+json` |
| `missing secrets/sentinel-intake-key.jwk` from `make config` | The agent key is not at the canonical path. See step 3 |
| Plugin does not load, no obvious error | Go version, `bifrost/core` version, or architecture mismatch on the `.so`. Run `make compat` in the plugin repo against the exact image you are loading into |
| Every call returns `tool not found` | Either `allow_connect_without_caller` is false, or you restarted instead of `down -v` |
| `no caller identity token` | No `Authorization: Bearer` reached the gateway |
| `wrong audience` from the MCP server | You validated against the authorization server's `audiences` field rather than the resource URL |

### Two shell traps that produce convincing fake errors

**zsh mangles ORNs.** `$ORG:apps` triggers zsh's `:a` history modifier and silently expands
to a filesystem path mid-string, producing a plausible `RESOURCE_ORN_ORG_MISMATCH` that has
nothing to do with your tenant. Always brace: `${ORG}:apps`.

**ORN namespaces are inconsistent.** Filters need `orn:oktapreview:...`, responses return
`orn:okta:...`. Never round-trip a returned ORN straight into a filter.

---

## Appendix A. The agent-to-API variant

Kept because it is a legitimate shape and because the repo's variable names and the `driver/`
scope names still come from it. **This is not what the live proof ran on.**

Here the agent reaches an **API behind a resource server** rather than another agent, and the
read and command paths are split across separate authorization servers. That needs **three**
servers.

| | Protects | Who mints there | Why |
|---|---|---|---|
| **Agent server** | the agent itself, as a resource | the Caller Service, via `client_credentials` | produces the first token, whose audience is the agent |
| **Read lane** | telemetry | the agent, by redeeming the ID-JAG | grants the read scopes |
| **Command lane** | dispatch | the agent, by redeeming the ID-JAG | grants the command scope |

The agent server is the one people miss. Its existence is why the caller's grant is specific
to invoking *this* agent rather than being ambient authority.

| | Name | Audience | Scopes |
|---|---|---|---|
| Read lane | `Fleet Ops Read` | `https://fleetops.atko.example/telemetry` | `fleet.telemetry.read`, `fleet.routes.read` |
| Command lane | `Fleet Ops Command` | `https://fleetops.atko.example/dispatch` | `fleet.dispatch.command` |

Keep `fleet.dispatch.command` **off** the read server entirely. If it exists on both, the read
lane can mint it and the point of the split evaporates.

Both lane servers need a rule allowing the **JWT Bearer** grant. The agent server needs
**Client Credentials**.

Then one resource connection per lane, each listing only its own scopes, and
`FLEETOPS_ISSUERS` / `FLEETOPS_AUDIENCES` carrying both lanes comma separated.

**Where the two shapes differ in what they demonstrate.** In this variant an over-broad ask
fails at the **lane boundary**, because the read lane cannot mint a command scope at all. In
the agent-to-agent shape that ran live, both requests go to the same authorization server and
the refusal comes from the **connection's scope list**. Both produce `invalid_scope` naming
the scope. They are different mechanisms reaching the same place, and it is worth knowing
which one you are actually showing.

Everything else in this runbook, the console-only steps, the immutable resource URL, the
policy-level `clients` condition, the `people.groups` trap, connection scopes, and the failure
table, applies unchanged.
