# Okta setup runbook

Everything here happens once, in your own tenant. Roughly 45 minutes to build, plus 20 to
verify.

Values you collect go into `.env`. Nothing from this runbook belongs in a committed file.

The steps are sequenced so each one's output is the next one's input. Skipping ahead mostly
produces errors that point at the wrong object.

**Steps 0 to 7 build the Okta objects. [Step 8](#8-verify-the-okta-side-before-involving-bifrost)
proves they are right, using nothing but curl and your admin session.** Do not skip it. It is
the difference between knowing which half of the system is broken and guessing.

> **This is unsupported sample code.** It is not an Okta product, it carries no SLA, and
> nothing here is covered by an Okta support contract. The console paths and error strings
> below were observed in one tenant at one point in time. Okta may change either.

---

## What you need before you start

Check these four things first. Three of them are cheap, and the first one cannot be worked
around by configuring anything.

**1. An org entitled to Okta for AI Agents.** Two places tell you: **Directory > AI Agents**
exists in the left-hand navigation, and **Settings > Features** contains a toggle called
**Secure AI A2A Servers**. If either is absent, the org is not entitled, and nothing in this
runbook will work. That is an account question for your Okta contact, not a configuration
problem, and it is worth resolving before you spend time on anything else.

**2. An admin account that can create authorization servers, applications and AI agents.**
Super Administrator is the safe choice. A narrower role may be sufficient; we did not
establish the minimum.

**3. A separate API credential, if you intend to use the API steps.** The demo's own
credentials are deliberately scoped to `agent.invoke` on a custom authorization server and
carry no management-API access at all, so they cannot list agents, read connections or read
the System Log. Anything below that calls `/api/v1/...` or `/workload-principals/...` needs
its own token with management scopes. This is not a gap in the integration: a demo client
has no business holding management scopes.

**4. Somewhere to put a private key file.** One key, one file, shown to you once.

### What can be automated, and what cannot

Two actions in this runbook are **console-only**. Both were confirmed by exhausting the API:
every verb and body shape returns 405. If you are scripting your tenant build, plan for two
manual pauses rather than discovering them mid-script.

| Object | API | Notes |
|---|---|---|
| Authorization server, policy, rule | **Yes** | `/api/v1/authorizationServers` |
| API Services app, the Caller Service | **Yes** | `/api/v1/apps` |
| AI agent, the registration itself | **Yes** | `/workload-principals/api/v1/ai-agents` |
| Agent JWK | **Yes** | must carry `use: "sig"`, see step 3 |
| Agent activation | **Yes** | `POST .../lifecycle/activate`, see step 3 |
| Resource connection, including its scope list | **Yes** | `PATCH` with `merge-patch+json`, see step 6 |
| **Agent owners** | **No, console-only** | the owners endpoint returns 405. Activation fails without an owner |
| **Registering an agent as a resource** | **No, console-only** | **Delegations > Non-human identity > Configure**. Blocks the connection and the delegation link until it exists |

---

## 0. Decide which topology you are building

There are two shapes, and they need different numbers of authorization servers. Getting this
wrong is the most expensive mistake available here, because the symptom is a misleading error
on a different object.

| Shape | What the agent reaches | Authorization servers |
|---|---|---|
| **Agent to agent.** What this demo runs, and what is proven live | another registered agent | **two**, one per invoked agent |
| **Agent to API.** The variant, kept in the appendix | a resource server behind read and command lanes | **three** |

**This runbook builds the agent-to-agent shape**, which is the one that was proven live. The
three-server layout is in [Appendix A](#appendix-a-the-agent-to-api-variant-which-is-not-the-main-path), clearly marked
as the alternative, because it is a legitimate variant and some of the repo's variable names
still carry its vocabulary.

### Two servers, but only one of them is a target

This trips people up, so it is worth stating before you build anything.

There are **two authorization servers**, one per agent. But the gateway defines **two
bindings**, one per tool group, and **both bindings address the same authorization server and
the same resource URL: the Target's.** They differ in exactly one thing, the scopes they ask
for.

| | Read binding | Command binding |
|---|---|---|
| Authorization server | the Target's | **the same one** |
| Resource URL | the Target's | **the same one** |
| Scopes requested | `agent.invoke`, `task.read` | `agent.invoke`, `task.dispatch` |

You can see this in `bifrost/config.template.json`: both bindings substitute
`${OKTA_COMMAND_LANE_AS_ID}` and `${FLEETOPS_COMMAND_RESOURCE_URL}`.

**That is not a simplification of the topology. It is the point.** Because both requests go to
the same place, a refusal cannot be explained away as having addressed the wrong server. The
only remaining variable is what the agent is permitted to ask for, which is the connection's
scope list from step 6. Splitting the two across separate servers would prove something
weaker. Appendix A does exactly that, and says so.

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

**The `sentinel/` demo in this repo is the same three-party shape under its own names**: its
Watch Service is a Caller Service, its Intake Agent is an Acting Agent, and its Tasking Agent is
a Target. It reads the same `OKTA_*` variables for the first two, and adds three of its own for
the third: `SENTINEL_TASKING_AS_ID`, `SENTINEL_TASKING_RESOURCE_URL` and
`SENTINEL_TASKING_SCOPES`. Whether you point those at the Target you build here or at a
separate agent is your choice; either way the objects needed are the ones in this runbook, built
once more. See `sentinel/README.md`.

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

Hard prerequisite. Without it the **ID-JAG exchange**, hop 2 of the three in the table above,
fails outright, and the error will not mention a feature flag. Confirm it first, because
everything below depends on it and it costs ten seconds.

If the toggle is not there at all, the org is not entitled to this. See
[what you need before you start](#what-you-need-before-you-start): that is an account question
and no amount of configuration below will substitute for it.

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

Three scopes, across two servers. The names are arbitrary and yours can differ; what matters
is which server carries which, and that is not arbitrary.

On the **Acting Agent's server**, add the scope the Caller Service will ask for on the
`client_credentials` call, for example `agent.invoke`. Record it as
`OKTA_SERVICE_CLIENT_SCOPE`.

On the **Target's server**, add every scope any binding will request there. This demo needs
all three:

| Scope | Purpose | On the Target's server |
|---|---|---|
| `agent.invoke` | present in **both** bindings' requested scope list | **Yes.** Easy to miss |
| `task.read` | reads. **Granted** on the connection | Yes |
| `task.dispatch` | the command that moves an asset. **Deliberately never granted** on the connection | Yes |

**`agent.invoke` has to exist on both servers, and that surprises people.** It is not only the
scope the Caller Service asks for at step 1. The plugin requests it again, alongside the read
or dispatch scope, on every exchange against the Target's server. See the two `scopes` arrays
in `bifrost/config.template.json`, and the issued token in step 9, whose granted scopes read
`task.read agent.invoke`. Publish it on the Acting Agent's server only and the read lane asks
the Target's server for a scope it cannot grant. Since Okta does not down-scope, that fails the
whole request rather than quietly dropping the one scope, so the read lane stops working
entirely and the fault is on a server you have no reason to suspect.

`task.dispatch` exists on the server and is withheld on the connection in step 6. That
separation is the demo: the same agent asks for both and Okta permits one.

> **There are two gates on a scope, and this is the first one.** Publishing a scope here, and
> granting it in the policy rule below, makes it *possible* to obtain. It does not make it
> permitted for any particular agent. That second decision is the connection's scope list in
> [step 6](#6-connect-the-acting-agent-to-the-target), and it is where the demo's refusal
> comes from. Passing this gate and failing the second is the single most common way to lose
> an afternoon here.

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

**Both agents must end up ACTIVE. A newly registered agent is not.** Registration leaves it
`STAGED`, which looks finished in the list view and is not usable for anything: Okta will not
issue against it, so the exchange fails later at a point that gives no hint that activation
was the problem. Do this now, not when something breaks.

Available over the API, contrary to some older notes:

```
POST /workload-principals/api/v1/ai-agents/{id}/lifecycle/activate
```

Returns 202. Only merge-patching `status` fails.

**Verifiable end state:** both agents read **ACTIVE** in **Directory > AI Agents**. If
activation returns `E0000001` instead, the agent has no owner. Go back and assign one, then
activate again.

Deactivating is the same call with `/deactivate`, and it is the demo's kill switch in
[step 9](#the-revocation-argument). It is worth knowing now that deactivation is instant in
Okta but does **not** invalidate a token already issued. Nothing can. That is why the gateway
re-asks rather than trusting a token it already holds.

---

## 4. Register each agent as a resource

This is the step that is genuinely hard to find, and it is **console-only**. The API returns
405 on every verb and body shape.

**Directory > AI Agents > (agent) > Delegations > Non-human identity > Configure.**

It is **not** called "register as an A2A server", which is why searching for that phrase
finds nothing. That single action is what creates the registration.

Pick the authorization server from step 2, then set **Audience / resource URL**. It must be
the **same string** you gave that server as its audience.

Three things about this step. Read all three before you type the URL, because the first two
are unforgiving.

- **It must be `https://`.** The console rejects the `api://` form outright, even in a tenant
  where older objects use it.
- **The resource URL is immutable once saved.** It cannot be edited, only deleted and
  recreated. A typo here costs a recreate, not an edit. The authorization server *can* be
  changed later, after removing callers. Choose the URL deliberately, and choose something you
  would be content validating tokens against for years.
- **It blocks everything downstream.** The resource connection in step 6 and, if you use one,
  the delegation link both fail until this exists. The connection reports `A2A server not
  found`; the delegation link reports a misleading `INVALID_FORMAT` on its
  `authorizationServerOrn`, which is really this same missing prerequisite rather than a
  malformed ORN. **Do this before step 6, not after.**

**Verifiable end state:** re-opening **Delegations > Non-human identity** shows the
authorization server and the resource URL you set, rather than a **Configure** prompt.

Record the Acting Agent's as `OKTA_AGENT_RESOURCE_URL` and the Target's as
`FLEETOPS_COMMAND_RESOURCE_URL`.

---

## 5. Create the Caller Service

**Applications > Create App Integration > API Services.**

This exists because registered agents cannot use `client_credentials`. Something else has to
mint the first token, and that something stands in for the scheduler or pipeline that would
trigger the agent in production.

Two things to wire up. **Both, not one.** They are separate grants in separate places, and the
first one succeeding is what makes the second one easy to forget.

1. Grant it access to the **Acting Agent's** authorization server from step 2, so it can mint
   there with `agent.invoke`. This is what step 8 Tier 1 tests.
2. Add it as a permitted caller on the **Acting Agent**, so it is allowed to delegate to it.
   In the console this is the agent's inbound callers list, which you may also see labelled
   Linked Applications.

The two are checked at different hops: the first at the `client_credentials` call, the second
when the agent tries to delegate. So doing only the first still passes step 8 Tier 1, and the
failure surfaces one hop later, on the agent rather than on the grant that is missing from it.

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

**This is the second of the two gates, and it is the one that decides.** Step 2 published
these scopes on the authorization server, which made them *obtainable*. This list decides
whether **this agent** may obtain them. Both gates must pass. The first one passing is what
makes a failure here so confusing: the server is configured correctly, the policy rule grants
the scope, and the request is still refused.

The setting has three values, and the console labels do not match the API strings, so both are
worth knowing:

| Console | API `scopeCondition` | Effect |
|---|---|---|
| **Allow all** | `ALL_SCOPES` | every scope the server publishes. Convenient, and it destroys the demo: `task.dispatch` becomes grantable and nothing is refused |
| **Only allow** | `INCLUDE_ONLY` | only the listed scopes. **Use this.** The list is the allowlist |
| **Disallow** | `EXCLUDE` | everything except the listed scopes. A denylist, and it fails open as the server gains scopes |

**Only allow** is the right choice for more than tidiness. It is the difference between an
agent whose permissions are stated and one whose permissions are whatever the server happens
to publish this month.

A server fully configured for a scope, with that scope on its policy, is still refused if the
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
Deactivate, delete, then re-create.

**Verifiable end state**, and this is the last Okta object, so it is worth checking properly:

```
GET /workload-principals/api/v1/ai-agents/{wlp}/connections
```

- `status` is `ACTIVE`, not `STAGED`
- `scopeCondition` is `INCLUDE_ONLY`
- `scopes` contains `agent.invoke` and `task.read`, and **does not contain** `task.dispatch`
- the resource identifier byte-matches `FLEETOPS_COMMAND_RESOURCE_URL`, including the presence
  or absence of a trailing slash

That last one is worth a literal comparison rather than a glance. A trailing-slash difference
presents as `invalid_target`, which reads like the connection is missing rather than
mismatched.

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
| `FLEETOPS_SCOPE_TELEMETRY_READ`, `_ROUTES_READ` | `task.read`. **Set these explicitly**, see below |
| `FLEETOPS_SCOPE_DISPATCH` | `task.dispatch`. **Set this explicitly**, see below |
| `OKTA_AGENT_INVOKE_SCOPE` | optional, defaults to `agent.invoke`. Only needed if your tenant names it something else |
| `BIFROST_IMAGE` | your **dynamically-linked** Bifrost. See the main README, Step 1 |
| `EXTRA_CA_BUNDLE` | only on networks that intercept TLS |

`OKTA_AGENT_PRIVATE_KEY_FILE` is not in the table because the `make demo-*` targets supply it
for you, pointing at the canonical key path from step 3. Running `driver/` by hand is the one
case where you set it yourself.

Four traps worth internalising.

**The resource URLs must match byte for byte.** A hyphen where there should be a colon, a
trailing slash, or a case difference all produce `invalid_target`.

**Set `FLEETOPS_AUDIENCES` to the Target's resource URL.** The issued token's `aud` is observed
to equal the `resource` value sent on the exchange, so validating against the resource URL is
what works.

> Do **not** conclude from that observation that `resource` is what determines `aud`. Both
> bindings here share one authorization server, so if its configured `audiences` holds the same
> string then `resource` and `audiences` predict the same `aud` and the observation cannot tell
> them apart. The mechanism is an open question. The operational instruction above is correct
> either way.

**Set the three `FLEETOPS_SCOPE_*` variables even though they have defaults.** Their built-in
defaults are the *Appendix A* scope names, `fleet.telemetry.read` and friends, kept for that
variant. Leave them unset in this topology and the resource server will demand
`fleet.telemetry.read` while Okta issues `task.read`. Okta's half succeeds, the gateway's half
succeeds, and the call is refused at the last hop by the one component you were not
suspecting. Nothing in the Okta configuration is wrong at that point, which is what makes it
expensive.

**The agent key never goes in `.env`.** It lives in `secrets/`, read directly by the plugin.
A JWK pasted unquoted into an env file is destroyed the moment that file is sourced by a
shell: the quotes are stripped, the commas become brace expansion. Keeping it in a file also
means the rendered Bifrost config is not itself a secret. `.env` in general is not
shell-sourceable; use `scripts/load-env.sh`, which parses it the way docker does.

---

## 8. Verify the Okta side, before involving Bifrost

**Do this before you start a single container.** Everything above is Okta configuration, and
every one of the objects can be checked on its own. Once Bifrost is in the path there are two
halves that can be broken and one error message, and the message usually names the wrong half.
Twenty minutes here saves an afternoon of debugging a gateway that is working correctly.

Three tiers, cheapest first. Each one proves a specific set of objects, so a failure tells you
where to look.

### Tier 0. The objects exist and say what you think they say

No credentials beyond your admin session, or a management API token if you prefer the API.

| Check | Where | Pass condition |
|---|---|---|
| Both authorization servers exist | **Security > API** | `audiences` on each byte-matches that agent's resource URL |
| The Target's server publishes all three scopes | its **Scopes** tab | `agent.invoke`, `task.read`, `task.dispatch` all present |
| Each server's policy names the right caller | its **Access Policies** | Caller Service on the Acting Agent's server, Acting Agent on the Target's. The `clients` condition is at **policy** level, not on the rule |
| Grant types differ per server | the policy rule | **Client Credentials** on the Acting Agent's server, **JWT Bearer** on the Target's |
| Both agents are ACTIVE | **Directory > AI Agents** | `ACTIVE`, not `STAGED` |
| Both agents have an owner | each agent's page | at least one. Activation fails without it |
| Both agents are registered as resources | **Delegations > Non-human identity** | shows a server and a resource URL, not a **Configure** prompt |
| The agent's key is registered | the agent's keys, or `GET .../ai-agents/{id}/jwks` | one key, `use: "sig"` |
| The connection is correct | `GET .../ai-agents/{wlp}/connections` | `ACTIVE`, `INCLUDE_ONLY`, grants `agent.invoke` and `task.read`, **omits** `task.dispatch` |

The last row is the one that produces the demo. If `task.dispatch` appears there, the refusal
will not happen and the demo proves nothing.

### Tier 1. Mint the Caller Service's token by hand

**The single most valuable check in this document**, because it needs nothing but curl, and it
exercises the whole first hop: the Caller Service's credentials, the Acting Agent's
authorization server, its policy and rule, the client grant, and the resource URL.

Run this from the repo root, since `load-env.sh` reads `.env` from the current directory:

```sh
. ./scripts/load-env.sh

RESP=$(curl -s -X POST "https://$OKTA_DOMAIN/oauth2/$OKTA_AGENT_OWN_AS_ID/v1/token" \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode grant_type=client_credentials \
  --data-urlencode "scope=$OKTA_SERVICE_CLIENT_SCOPE" \
  --data-urlencode "resource=$OKTA_AGENT_RESOURCE_URL" \
  --data-urlencode "client_id=$OKTA_SERVICE_CLIENT_ID" \
  --data-urlencode "client_secret=$OKTA_SERVICE_CLIENT_SECRET")

printf '%s\n' "$RESP"
```

An `access_token` in the response is a pass. Now read its claims, because a 200 alone does not
tell you whether the `resource` parameter took effect:

```sh
printf '%s' "$RESP" | python3 -c '
import base64, json, sys
body = json.load(sys.stdin)
if "access_token" not in body:
    sys.exit("no token: " + json.dumps(body))
p = body["access_token"].split(".")[1]
print(json.dumps(json.loads(base64.urlsafe_b64decode(p + "=" * (-len(p) % 4))), indent=2))
'
```

The padding arithmetic is not decoration: JWT segments use base64url with the padding stripped,
and `base64 -d` on the command line rejects them for exactly that reason.

| Claim | Expected |
|---|---|
| `aud` | equals `OKTA_AGENT_RESOURCE_URL`, exactly |
| `scp` | contains your invoke scope |
| `iss` | `https://$OKTA_DOMAIN/oauth2/$OKTA_AGENT_OWN_AS_ID` |
| `cid` | the Caller Service's client id |

**If `aud` is not the resource URL, stop here** and fix it before going further. This token
becomes the `subject_token` of the next hop, and Okta rejects a subject token whose audience is
not the agent being invoked. The error it returns then names the subject token, which sends you
looking at the agent's key rather than at this.

Common failures at this tier, and what each implicates:

| Response | The object at fault |
|---|---|
| `invalid_client` | the Caller Service's client id or secret. Nothing else has been reached yet |
| `access_denied` | the Acting Agent's server policy does not list the Caller Service, or the rule does not allow Client Credentials |
| `invalid_scope` | the scope is not published on the **Acting Agent's** server |
| `invalid_target` or an `aud` that is not your URL | the `resource` value does not match the resource URL registered in step 4. Byte-compare them |

### Tier 2. The two agent-side hops

These sign a `private_key_jwt` with the agent's key, so they are not a one-line curl. Use the
driver, which calls the plugin's own exchange code rather than a parallel implementation:

```bash
make demo
```

This is the first step that needs Docker and the sibling plugin repo. Everything above did not.

| Target | Expected | What a failure implicates |
|---|---|---|
| `make demo-read` | **ISSUED** | the agent's key, its JWK registration, the Target's server policy, and the connection's scope list |
| `make demo-command` | **REFUSED**, `invalid_scope [task.dispatch]` | if this is ISSUED instead, `task.dispatch` is grantable on the connection. Fix step 6 |

**Both outcomes are pass conditions.** A refusal here is the demo working, not the demo broken.

If `make demo` cannot find the plugin, or the driver cannot see the repo, that is a
build-and-paths problem rather than an Okta one. The driver's container mounts your home
`code/` directory and expects this repo at `~/code/fleetops-bifrost-demo`; a clone elsewhere
needs the `DRIVER` mount in the `Makefile` adjusted. Tiers 0 and 1 have already told you the
Okta side is sound, which is exactly the isolation this section exists to give you.

### When all three tiers pass

Your Okta configuration is correct and independently demonstrated. Anything that breaks from
here is the gateway, the config rendering, or the resource server, and the main
[README](../README.md) and [TROUBLESHOOTING.md](TROUBLESHOOTING.md) cover those. That is a much
smaller search space than "something in the stack is wrong".

---

## 9. Run it

### First, without the gateway

Start with the driver. It exercises the exchange with nothing else in the way, so a
misconfiguration shows you Okta's own answer rather than a gateway's interpretation of it.

```bash
make demo             # both outcomes in one run
```

```
lane read     ISSUED    chain 0oa<your-service-id> <- wlp<your-agent-id>   scopes task.read agent.invoke
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
| `401 access_denied`, policy evaluation failed | The caller is not a listed client of that authorization server, or its rule does not allow the grant type. Remember the `clients` condition is at policy level and names the caller. **A deactivated agent may also present this way, see below** |
| A rule that never seems to match | `people.groups` names a user group. An agent is a workload principal. Use EVERYONE |
| `'subject_token' is invalid` | The caller presented an ID token, or a token from the **org** authorization server. It must be an access token from a **custom** server with a resource-scoped `aud` |
| `invalid_client` | Step 1: the Caller Service's id or secret. Steps 2 and 3: the agent's key does not match the one registered on the agent. **A deactivated agent may also present this way, see below** |
| `A2A server not found` on a connection | Step 4 has not been done for that agent |
| `INVALID_FORMAT` on `authorizationServerOrn` | Also step 4. The ORN is fine; the registration is missing |
| `E0000001` on activation | The agent has no owner |
| `E0000021` on a connection PATCH | Wrong content type. It must be `application/merge-patch+json` |
| `missing secrets/sentinel-intake-key.jwk` from `make config` | The agent key is not at the canonical path. See step 3 |
| Plugin does not load, no obvious error | Go version, `bifrost/core` version, or architecture mismatch on the `.so`. Run `make compat` in the plugin repo against the exact image you are loading into |
| Every call returns `tool not found` | Either `allow_connect_without_caller` is false, or you restarted instead of `down -v` |
| `no caller identity token` | No `Authorization: Bearer` reached the gateway |
| `wrong audience` from the MCP server | You validated against the authorization server's `audiences` field rather than the resource URL |

### A deactivated agent: `invalid_client` or `access_denied`

Both have been seen. **We have not established which conditions produce which**, so treat
either as a possible deactivation and check the agent's status before you spend time on the
credential it appears to be complaining about.

That ambiguity matters because the two errors otherwise point at completely different objects.
`invalid_client` reads as "your key or secret is wrong" and `access_denied` reads as "your
policy is wrong", and a deactivated agent is neither. **The cheap check comes first:** open
**Directory > AI Agents** and confirm the agent is `ACTIVE`. It takes five seconds and rules out
the case that would otherwise cost you an hour of comparing key thumbprints.

### Two shell traps that produce convincing fake errors

**zsh mangles ORNs.** `$ORG:apps` triggers zsh's `:a` history modifier and silently expands
to a filesystem path mid-string, producing a plausible `RESOURCE_ORN_ORG_MISMATCH` that has
nothing to do with your tenant. Always brace: `${ORG}:apps`.

**ORN namespaces are inconsistent.** Filters need `orn:oktapreview:...`, responses return
`orn:okta:...`. Never round-trip a returned ORN straight into a filter.

---

## Appendix A. The agent-to-API variant, which is NOT the main path

> **Do not follow this appendix if you are working through the runbook.** It describes a
> different topology from steps 0 to 9 above, with three authorization servers instead of two
> and different scope names. Mixing the two produces a tenant that matches neither. This is
> here as a reference for a team whose agent reaches an API rather than another agent.

Kept because it is a legitimate shape and because the repo's variable names and the `driver/`
scope defaults still come from it. **This is not what the live proof ran on.**

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
