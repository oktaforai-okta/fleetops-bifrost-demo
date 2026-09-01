# Sentinel chain of custody

A one-button demonstration of an agent-to-agent chain of custody in Okta, with the
decoded token at every hop.

The claim it exists to make legible is narrow: **Okta is the policy decision point, and a
gateway is only the enforcement point.** There is no gateway anywhere in this app. Every
outcome it shows came straight from Okta, and every refusal is Okta's own wording, because
the decision is the thing being demonstrated and a gateway in the middle would only
obscure whose decision it was.

## The scenario

```
Sentinel Watch Service  ──▶  Sentinel Intake Agent  ──▶  Sentinel Tasking Agent
   (service client)              (agent A)                 (agent B, privileged)
```

**Hop 1**, one call, made by this app. The Watch Service mints its own token via
`client_credentials` at a custom authorization server, with `resource` set to the Intake
Agent's resource URL. Registered agents cannot use `client_credentials` at all, which is
exactly why the chain has to be started by something that is not an agent.

**Hop 2**, two calls, made by the **plugin**, not by this app. The Intake Agent exchanges
that token at the **org** authorization server for an ID-JAG, then redeems the ID-JAG at
the Tasking Agent's authorization server for the final access token.

Hop 2 is `Exchange` from the sibling `okta-bifrost-plugin` module, imported rather than
reimplemented. That is deliberate and it is the point: a run that reaches ISSUED is direct
evidence that the plugin's own exchange code works. Had the exchange been rewritten here,
a passing run would have proved nothing about the code that actually ships.

`Exchange` rather than the narrower `MintResourceToken`, which is now a thin wrapper over
it. Both run the same two calls, but `Exchange` also returns the intermediate ID-JAG, and
that assertion is what actually asserts the delegation; the access token is only what it
was redeemed for. So both are decoded and shown, and if an `act` claim exists anywhere in
this flow, the assertion is the likelier place to find it.

## What it will not do

The RFC 8693 `act` claim is what would carry a delegation chain. It is **not documented in
any of Okta's published pages, and has never been observed from this tenant.**

So the app renders the claims that are actually present and nothing else:

- If `act` is present, the chain is read from it and shown outermost-subject-first.
- If `act` is absent, the page says so plainly, and shows `sub`, `cid` and `uid` instead,
  labelled as an inference rather than an assertion.
- Absent claims among the important ones are drawn as rows reading *not on this token*,
  because "no `uid` here" is itself the evidence that no user is involved.

Nothing about the chain is hardcoded, assumed, or drawn from a shape the token does not
support. The credibility of the demo is the only thing it has.

## File tree

```
sentinel/
├── .env.example          what sentinel/ adds on top of ../.env.example
├── README.md
├── api/                  Go, standard library only (plus the plugin)
│   ├── go.mod            replace -> ../../../okta-bifrost-plugin (sibling clone)
│   ├── go.sum
│   ├── main.go           routes, CORS, SSE
│   ├── config.go         environment -> plugin Config, and the non-secret /api/config shape
│   ├── chain.go          hop 1, then the plugin's Exchange for hop 2
│   ├── jwt.go            claim decoding, token preview, endpoint masking
│   └── render.yaml       Render blueprint, every value unset
└── web/                  static, no build step
    ├── index.html
    ├── app.js            D3 v7 from CDN, SSE parsed from fetch
    └── style.css
```

## Endpoints

| Method | Path           | Returns                                                        |
| ------ | -------------- | -------------------------------------------------------------- |
| GET    | `/api/healthz` | `{"ok":true}`                                                  |
| GET    | `/api/config`  | principal names, resource URLs, scopes, and what is unset       |
| POST   | `/api/run`     | Server-Sent Events, one per step                                |

`POST` rather than `GET` for the run, because it mints live tokens and that is neither
safe nor idempotent. The cost is that the browser cannot use `EventSource`, which is
GET-only, so `app.js` parses the stream out of `fetch` instead.

`/api/config` carries no tenant host, agent id or authorization server id, so the
frontend can draw the entire diagram without ever holding a tenant identifier.

Token values are never returned. Every step reports the decoded claims plus a preview of
the first 12 and last 8 characters.

## Running it locally

Two processes. Nothing is deployed and nothing is committed.

```sh
# 1. The API. Needs the sibling plugin clone at ../../okta-bifrost-plugin.
cd sentinel/api
set -a; . ../../.env; . ../.env; set +a
go run .                       # http://localhost:8090

# 2. The frontend, any static server.
cd sentinel/web
python3 -m http.server 8000    # http://localhost:8000
```

On `localhost` the frontend defaults to `http://localhost:8090`, so the two find each
other with no configuration. Elsewhere, set the base once with
`?api=https://your-api.example.com`; it is remembered per browser, and `?api=` with
nothing after it forgets it. **The API base is never compiled in**, which is what lets the
same static files work with a backend on Render.

With no API running at all, the page still draws its idle diagram and says the API is
unreachable. It does not invent resource URLs or scopes to fill the gap.

If there is no local Go toolchain, run the API containerised:

```sh
docker run --rm -p 8090:8090 --env-file ../../.env --env-file ../.env \
  -v "$HOME/code":/w -w /w/fleetops-bifrost-demo/sentinel/api \
  --platform linux/arm64 golang:1.27 go run .
```

## Environment

`sentinel/` reuses the repo root's variable names wherever one already means the right
thing, so a filled-in `../.env` that already drives the Fleet Ops driver needs only the
three `SENTINEL_TASKING_*` values added.

| Variable                      | Required | Meaning here                                                   |
| ----------------------------- | -------- | -------------------------------------------------------------- |
| `OKTA_DOMAIN`                 | yes      | tenant hostname, no scheme                                     |
| `OKTA_SERVICE_CLIENT_ID`      | yes      | the Watch Service, an API Services client                      |
| `OKTA_SERVICE_CLIENT_SECRET`  | yes      | ditto                                                          |
| `OKTA_SERVICE_CLIENT_SCOPE`   | no       | defaults to `agent.invoke`                                     |
| `OKTA_AGENT_OWN_AS_ID`        | yes      | the AS protecting the **Intake** Agent as a resource; hop 1 happens here |
| `OKTA_AGENT_ID`               | yes      | the Intake Agent's workload principal id                       |
| `OKTA_AGENT_RESOURCE_URL`     | yes      | the Intake Agent's resource URL, e.g. `api://sentinel-intake`   |
| `OKTA_AGENT_PRIVATE_KEY_FILE` | yes      | the Intake Agent's private key as a JWK on disk                |
| `SENTINEL_TASKING_AS_ID`      | yes      | the AS protecting the Tasking Agent                            |
| `SENTINEL_TASKING_RESOURCE_URL` | yes    | sent as `resource`; becomes the final token's `aud`             |
| `SENTINEL_TASKING_SCOPES`     | yes      | space separated                                                |
| `SENTINEL_WATCH_NAME` and the two siblings | no | display only                                       |
| `SENTINEL_MASK_IDS`           | no       | `true` hides the tenant host and `wlp`/`aus`/`0oa` ids in reported endpoints. Claims are never masked. |
| `SENTINEL_ALLOWED_ORIGIN`     | no       | `Access-Control-Allow-Origin`, defaults to `*`                  |
| `PORT`                        | no       | defaults to 8090, since Bifrost owns 8080 in this repo          |

The Intake Agent reuses the driver's `OKTA_AGENT_*` names because it is the same kind of
thing in the same position: the audience of hop 1 and the actor of hop 2. The Tasking
Agent needed new names because the driver's target is a resource server behind a read or
command lane, and no existing variable means "the other agent".

There are no defaults on anything that changes what Okta is asked for. A silent default
there would produce a refusal whose cause is the default rather than the tenant, which is
the opposite of what this app is for.

## Deploying

Not deployed. Both configs are prepared.

**Frontend, Vercel.** Nothing is needed. Set the project's Root Directory to
`sentinel/web`, framework preset *Other*, no build command, no install command. There is
no `package.json` and no bundler, so Vercel serves the three files as static output. No
`vercel.json` was added because none is genuinely required, and the API base is a runtime
value rather than a build-time one.

**API, Render.** `api/render.yaml` is a blueprint with every environment value `sync:
false`, so Render prompts once and keeps them in the service rather than reading them from
this repo.

One blocker is called out in that file and worth repeating: `go.mod` uses a `replace`
pointing at the **sibling** `../../../okta-bifrost-plugin` clone. Render checks out only
this repository, so the build will fail on the missing replace target until the plugin is
either vendored into this repo as a submodule (with the replace rewritten at build time)
or published somewhere the builder can reach. The committed `replace` was left
sibling-relative on purpose: it is what makes a local run evidence about the plugin source
you are about to build.

`OKTA_AGENT_PRIVATE_KEY_FILE` needs a Render secret file, since the plugin reads the key
from disk once at startup.

## Reading a refusal

A refusal is an outcome, not an error, and often the more interesting result. Okta's
`error` and `error_description` are passed through verbatim; the app adds a note about
where the fix usually lives, which is frequently not where the message points.

| Okta says        | What it usually means                                                    |
| ---------------- | ------------------------------------------------------------------------ |
| `invalid_scope`  | The scope is not on the agent's managed **connection**. Publishing it on the authorization server and its policy is not enough. Okta does not down-scope, so one ungrantable scope fails the whole request. |
| `invalid_target` | No ACTIVE connection matches the `resource` sent. Byte-compare the URL and confirm the connection is ACTIVE, not staged. Reads like permission, is almost always configuration. |
| `access_denied`  | Policy evaluation failed: the acting agent is deactivated, or the caller is not a permitted client of that authorization server. |
| `subject_token`  | The subject token must come from a **custom** authorization server with a resource-scoped `aud`. An org authorization server token is refused by design. |
| `invalid_client` | Hop 1: the service client's id or secret. Hop 2: the agent's `private_key_jwt` does not match the key registered on the agent. |

When the failure is *not* Okta's, the panel says so and labels the wording as this app's.
A DNS failure is not a denial, and presenting one as though it were would be the same
category of dishonesty as drawing an `act` chain that is not there.
