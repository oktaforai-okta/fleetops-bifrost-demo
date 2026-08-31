# Okta setup runbook

Everything here happens once, in your own tenant. Roughly 30 minutes the first time.

Values you collect go into `.env`. Nothing from this runbook belongs in a committed file.

A note on ordering: the steps are sequenced so that each one's output is the next one's
input. Skipping ahead mostly produces errors that point at the wrong object.

---

## 1. Two authorization servers, one per lane

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

**The private key is shown exactly once.** Copy it immediately into `.env` as
`OKTA_AGENT_PRIVATE_KEY_JWK`, on one line. If you miss it, generate a new key rather than
trying to recover the old one.

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

## 3. A service client to start the chain

**Applications > Create App Integration > API Services.**

This exists because **registered agents cannot use `client_credentials` at all**. Their
grant types are token-exchange and jwt-bearer only. So something else has to mint the
first token, and that something is a service client standing in for the scheduler or
pipeline that would trigger the agent in production.

Then add the service client as a permitted caller on the agent, so it is allowed to
delegate to it. In the console this is the agent's inbound callers list, which you may
also see labelled Linked Applications.

Record the client id and secret for whatever drives the demo.

---

## 4. Connect the agent to both lanes

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

## 5. Fill in .env

`FLEETOPS_ISSUERS` is `https://<domain>/oauth2/<aus id>` for each lane, comma separated.

`FLEETOPS_AUDIENCES` and the two `*_RESOURCE_URL` values must match the connections'
resource indicators **byte for byte**. A hyphen where there should be a colon, a trailing
slash, or a case difference all produce `invalid_target`.

A trap worth internalising: the token's `aud` comes from the `resource` parameter on the
exchange, **not** from the authorization server's `audiences` field. Two servers can share
an `audiences` value and still issue tokens with completely different `aud`. The Fleet Ops
server validates against the resource URL, which is the correct one of those two.

---

## 6. Run it

```bash
make up
make logs
```

Then connect a client and try, in order:

1. `get_telemetry` with `vehicle_id: FL-114`. Succeeds. Read the delegation chain at the
   bottom of the result: the calling service is the subject, the agent is the actor.
2. `dispatch_vehicle` with `FL-114` and any destination. Succeeds, and the audit line
   records which agent moved the asset.
3. Deactivate the agent in Okta: **Directory > AI Agents > your agent > Deactivate**.
4. `dispatch_vehicle` again. Within `agent_status_ttl`, 10 seconds by default, Bifrost
   refuses. The connection is still open and the token it holds has not expired. The
   refusal is Okta declining to issue, surfaced through the gateway.

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
