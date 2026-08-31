# Okta setup runbook

Everything here happens once, in your own tenant. Roughly 45 minutes the first time.

Values you collect go into `.env`. Nothing from this runbook belongs in a committed file.

A note on ordering: the steps are sequenced so that each one's output is the next one's
input. Skipping ahead mostly produces errors that point at the wrong object.

---

## 0. Check the feature is on

**Settings > Features**, find **Secure AI A2A Servers**, and enable it.

This is a hard prerequisite for the agent-to-agent exchange. Without it the exchange in
step 2 of the flow fails outright, and the error will not mention a feature flag. Check
this first, because everything below depends on it and it costs ten seconds to confirm.

---

## Three authorization servers, not two

Worth stating up front, because it is the easiest thing to get wrong. This demo needs
**three** custom authorization servers, doing different jobs:

| | Protects | Who mints there | Why |
|---|---|---|---|
| **Agent server** | the agent itself, as a resource | the service client, via `client_credentials` | Produces the first token, whose audience is the agent. This is what the delegation chain starts from |
| **Read lane** | Fleet Ops telemetry | the agent, via ID-JAG redemption | Grants the read scopes |
| **Command lane** | Fleet Ops dispatch | the agent, via ID-JAG redemption | Grants the dispatch scope |

The agent server is the one people miss. Its existence is why the caller's grant is
specific to invoking *this* agent rather than being ambient authority, and without it
there is nothing for step one to mint against.

---

## 1. The two Fleet Ops authorization servers

**Security > API > Authorization Servers > Add Authorization Server**, twice.

| | Name | Audience |
|---|---|---|
| Read lane | `Fleet Ops Read` | `https://fleetops.atko.example/telemetry` |
| Command lane | `Fleet Ops Command` | `https://fleetops.atko.example/dispatch` |

Two servers rather than one is the whole point of the lane split. Okta does **not**
down-scope a token-exchange request: a scope the agent cannot be granted fails the entire
request rather than returning the grantable subset. Separating the lanes means an
over-broad ask fails at a boundary you can point at, instead of quietly succeeding with
less than was asked for.

Record both authorization server ids (`aus...`) as `OKTA_READ_LANE_AS_ID` and
`OKTA_COMMAND_LANE_AS_ID`.

### Scopes

On the **read** server's **Scopes** tab, add:

- `fleet.telemetry.read`
- `fleet.routes.read`

On the **command** server:

- `fleet.dispatch.command`

Keep `fleet.dispatch.command` off the read server entirely. If it exists on both, the
read lane can mint it and the demo's central claim evaporates.

### Access policy

Each server needs **Access Policies > Add Policy**, then **Add Rule** on that policy.

The rule must allow the **JWT Bearer** grant type. That is what lets the server accept
the ID-JAG assertion in step three of the exchange. Without it the redemption fails with
an unhelpful client error.

List the scopes explicitly on the rule rather than using a wildcard. A wildcard works,
but it hides which lane grants what, which is the thing you are trying to demonstrate.

---

## 2. Register the agent

**Directory > AI Agents > Register AI Agent.**

- Give it a name and description
- **Assign an owner.** This is not optional: activation fails with an opaque
  `E0000001` until an owner exists, and the owners endpoint returns 405 so you cannot do
  it over the API
- Generate a key pair under client credentials

**The private key is shown exactly once.** Save it immediately to
`secrets/agent-key.jwk`, as the JSON object Okta shows you. If you miss it, generate a new
key pair rather than trying to recover the old one.

It goes in a file rather than in `.env` on purpose. A JWK pasted unquoted into an env file
is destroyed the moment that file is sourced by a shell, because the quotes are stripped
and the commas are treated as brace expansion. Keeping it in a file also means the
rendered Bifrost config never contains key material, so the config is not itself a secret.

Activate the agent once the owner is assigned. Record the `wlp...` id as
`OKTA_AGENT_ID`.

### Register it as a resource too (dual citizenship)

From the agent's profile, register it as a resource with a resource URL, for example
`https://fleetops.atko.example/agent`. Record it as `OKTA_AGENT_RESOURCE_URL`.

Two things about this step:

- **It is console-only.** `PUT`/`POST` to the `a2a-servers` endpoint returns 405.
- **The resource URL cannot be edited afterwards**, only deleted and recreated. Choose
  it deliberately.

This is required because the calling service's token must carry this URL as its audience.
Without it there is nothing for the caller's grant to be specific to.

---

## 3. The agent's own authorization server

This is the third server from the table above, and the one that is easy to skip.

**Security > API > Authorization Servers > Add Authorization Server.**

| | |
|---|---|
| Name | `Fleet Ops Agent` |
| Audience | the agent's resource URL from step 2, e.g. `https://fleetops.atko.example/agent` |

On its **Scopes** tab add `agent.invoke`. On **Access Policies**, add a policy and a rule
allowing the **Client Credentials** grant type, and list `agent.invoke`.

Record its id as `OKTA_AGENT_OWN_AS_ID`, and the scope as
`OKTA_SERVICE_CLIENT_SCOPE=agent.invoke`.

Note the grant type here is Client Credentials, not JWT Bearer. This server is minting a
token *for a service client acting as itself*, which is a different job from the two lane
servers, where the agent redeems an assertion.

---

## 4. A service client to start the chain

**Applications > Create App Integration > API Services.**

This exists because **registered agents cannot use `client_credentials` at all**. Their
grant types are token-exchange and jwt-bearer only. So something else has to mint the
first token, and that something is a service client standing in for the scheduler or
pipeline that would trigger the agent in production.

Two things to wire up:

1. Grant it access to the agent server from step 3, so it can mint there with the
   `agent.invoke` scope.
2. Add it as a permitted caller on the **agent**, so it is allowed to delegate to it. In
   the console this is the agent's inbound callers list, which you may also see labelled
   Linked Applications.

Record the client id and secret as `OKTA_SERVICE_CLIENT_ID` and
`OKTA_SERVICE_CLIENT_SECRET`.

---

## 5. Connect the agent to both lanes

On the agent, **Resource connections > Add connection**, twice. Resource type
**Authorization server**.

| Lane | Server | Scopes |
|---|---|---|
| Read | Fleet Ops Read | `fleet.telemetry.read`, `fleet.routes.read` |
| Command | Fleet Ops Command | `fleet.dispatch.command` |

Use **Only allow** and list the scopes rather than **Allow all**.

**This is the step people get wrong.** The scope list on the *connection* is what an
exchange is validated against, not only the authorization server's policy rule. A server
fully configured for a scope, with that scope on its policy, will still be refused if the
connection does not list it:

```
400 invalid_scope
"The following scopes are not allowed for this request: [fleet.dispatch.command]."
```

which reads like a server misconfiguration and sends you to the wrong object. When you
see it, check the connection.

To change connection scopes over the API:

- `PATCH /workload-principals/api/v1/ai-agents/{wlp}/connections/{mcn}`
- `Content-Type` must be `application/merge-patch+json`. Plain `application/json` and
  `application/json-patch+json` both return `E0000021`. `PUT` returns 405
- The body must include `scopeCondition` alongside `scopes`, or you get `E0000001`
  "Resource connection validation failed"

```json
{ "scopes": ["fleet.telemetry.read", "fleet.routes.read"], "scopeCondition": "INCLUDE_ONLY" }
```

Both connections must be **ACTIVE**. A staged connection produces `invalid_target`, which
looks like a typo in a URL.

---

## 6. Fill in .env

`FLEETOPS_ISSUERS` is `https://<domain>/oauth2/<aus id>` for each lane, comma separated.

`FLEETOPS_AUDIENCES` and the two `*_RESOURCE_URL` values must match the connections'
resource indicators **byte for byte**. A hyphen where there should be a colon, a trailing
slash, or a case difference all produce `invalid_target`.

A trap worth internalising: the token's `aud` comes from the `resource` parameter on the
exchange, **not** from the authorization server's `audiences` field. Two servers can share
an `audiences` value and still issue tokens with completely different `aud`. The Fleet Ops
server validates against the resource URL, which is the correct one of those two.

---

## 7. Run it

Start with the driver, not the gateway. It exercises the real exchange with nothing else
in the way, so when something is misconfigured you see Okta's own answer rather than a
gateway's interpretation of it.

```bash
make demo-read          # expect a token naming the caller and the agent
make demo-command       # expect a token with the dispatch scope
make demo-deny          # read lane asked for a command scope: expect a refusal
```

On `demo-read`, the line worth reading is the delegation chain: the calling service as
subject, the agent as actor. If the actor is absent, the `act` claim did not come back and
the central claim of the demo is not being made. Stop and fix that before going further.

Then the revocation moment, which is the point of the whole thing:

1. `make demo-command` and watch it succeed.
2. Deactivate the agent: **Directory > AI Agents > your agent > Deactivate**.
3. `make demo-command` again. Okta now refuses to issue.

There is a second, sharper version of the same idea. The token the lane issues is an
ordinary Okta access token, so it can be revoked directly rather than by deactivating the
whole agent. That distinction is worth having ready if someone asks whether the only
control is an all-or-nothing kill switch.

### Through the gateway

Once the driver is green, the same story runs through Bifrost:

```bash
make up
make logs
```

This is the coexistence half of the argument: Okta decides, Bifrost enforces. It depends
on Bifrost being configured so the caller's token reaches the connect hook, which is a
constraint of Bifrost's connection model rather than of Okta. If the driver works and the
gateway does not, the problem is in the gateway configuration, not in the exchange.

---

## Reading a failure

| Symptom | Cause |
|---|---|
| `400 invalid_scope`, naming a scope | The connection does not list it. Check the connection, not the authorization server |
| `400 invalid_target` | No ACTIVE connection matches `resource`. Byte-compare the URL, and check the connection is active |
| `401 access_denied`, policy evaluation failed | The caller is not a permitted client of that authorization server |
| `'subject_token' is invalid` | The caller presented an ID token, or a token from the org authorization server. It must be an access token from a custom authorization server carrying a resource-scoped `aud` |
| Plugin does not load, no obvious error | Architecture or dependency mismatch on the `.so`. Rebuild with `PLATFORM` matching the Bifrost host, and pin `BIFROST_IMAGE` |
| `no caller identity token` | The Bifrost client is in `oauth` mode. It needs `headers` or `both` so the caller's own token reaches the gateway |
| `wrong audience` from Fleet Ops | You validated against the authorization server's `audiences` value rather than the resource URL |
