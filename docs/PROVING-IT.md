# Proving it, rather than asserting it

A web page can be made to say anything. This document is how you show that what the page
says actually happened, from sources that are not the page.

There are **three independent accounts of the same event**. Two you can show from a terminal
in seconds. The third needs the Okta Admin Console.

You do not need all three. One is usually enough, and the second one below is the strongest
and the cheapest.

---

## 1. Bifrost's own log: the gateway recording its decision

This is the one that matters most, because it is the enforcement point speaking for itself
rather than the demo app describing it.

Run this in a second window **before** you present:

```sh
./scripts/watch-gateway.sh
```

One line per tool call. On an allowed call:

```
16:57:32Z  tool handler start tool="fleetops_read-list_routes" arg_count=0
16:57:34Z  tool handler success tool="fleetops_read-list_routes"
```

On a refused call:

```
16:57:44Z  tool handler start tool="fleetops_command-dispatch_vehicle" arg_count=2
16:57:44Z  tool handler error tool="fleetops_command-dispatch_vehicle"
           error=okta denied "fleetops_command-dispatch_vehicle" on "fleetops_command":
           id-jag exchange: invalid_scope: The following scopes are not allowed for this
           request: [task.dispatch]. (HTTP 400)
```

**Read the wording, it is load-bearing.** `okta denied` is the plugin's per-call hook, which
means the check ran on *this call*. If you instead see `okta refused to issue a token for`,
that is the connect-time path: also correct, but it only proves the session could not start,
which is the weaker claim.

`invalid_scope: ... [task.dispatch]` is Okta's own error text, passed through rather than
composed by the gateway.

### If you want the raw log instead

```sh
docker logs --tail 40 fleetops-bifrost-demo-bifrost-1
```

Mostly HTTP request-completed noise, which is why the script filters to the decision lines.

---

## 2. The resource server's silence: the refused call never arrived

The strongest single fact in the demo, and it is a count.

```sh
docker logs fleetops-bifrost-demo-fleetops-1 | grep -c "tools/call.*dispatch_vehicle"
```

**Expect `0`.**

Not "the server received it and rejected it". It never got there. The gateway stopped it
before the protected system was touched at all.

> **One trap, worth knowing before you type it.** Grepping the bare tool name gives `1`, not
> `0`, because the server prints a startup banner listing the tools it serves and their
> required scopes. That line is not a call. Grep for `tools/call.*dispatch_vehicle` as above,
> or read the match and see that it is the banner.

To show the contrast, the allowed calls plainly did arrive:

```sh
docker logs fleetops-bifrost-demo-fleetops-1 | grep "ACCEPTED" | tail -3
```

Each carries the scope, the delegation chain, and the token id, written by the server after
it validated the token itself.

### Why this is not the gateway being trusted

The resource server does its own cryptographic validation and does not take the gateway's
word for anything: RSA signature verified against Okta's published keys, `RS256` pinned so a
token claiming `alg: none` is rejected, plus issuer, audience and expiry checks. See
`server/auth.go`. Going around the gateway does not get you in; it just means no token.

### The integrity check, and why not to run it live

In principle, gateway successes and calls the server logged receiving should be the same
number: if the gateway claimed to forward more than the server received, something would be
inventing traffic. This was verified during QC over a matched window, 30 and 30 exactly.

**Do not run it as a live one-liner.** The two containers were started at different times, so
their logs cover different windows, and a naive comparison across their whole lifetimes
currently reports something like 84 against 59. That difference is entirely calls that
happened before the resource server was last restarted, and it is not evidence of anything,
but it looks alarming on a shared screen and takes several minutes to explain.

If you genuinely need it, compare only calls after the later container's start time. Otherwise
lean on the count of `0` above, which is stable, needs no window arithmetic, and is the
stronger fact anyway.

---

## 3. Okta's System Log: the decision at the source

**Admin Console only.** The credentials in this repo are scoped `agent.invoke` on the demo's
custom authorization server, so `GET /api/v1/logs` returns **401**. That is expected: a demo
client has no business holding management-API scopes. It is not a gap in the integration.

So do not offer to pull Okta's log from a terminal. Log in to the Okta Admin Console
instead, which is the normal way to read it:

**Reports > System Log**, time range: last 5 minutes.

Event types worth filtering for:

| eventType | What it is |
|---|---|
| `app.oauth2.token.grant.id_jag` | the ID-JAG being issued, step 2 of the exchange |
| `app.oauth2.as.token.grant.access_token` | a token granted at a custom authorization server |

Look at `outcome.result` and `outcome.reason` on each.

> **Check this yourself before you present, it takes a minute.** Run the refusal once, then
> look for it in the System Log. Confirm with your own eyes whether the `invalid_scope`
> denial appears as its own event, because a granted-token event and a refused-grant event
> are not guaranteed to be logged the same way. If it is not there as a discrete event, that
> is fine: you still have sources 1 and 2. What you want to avoid is finding out while a
> customer is watching.

---

## What the QC pass actually verified

Run against the live system, not reasoned about:

| Claim | Status |
|---|---|
| The refused call never reached the resource server | **Verified.** Count of 0 |
| The refusal is Okta's decision, relayed | **Verified.** Okta's error text, in the gateway's log |
| The per-call check fires, not just connect-time | **Verified.** 36 per-call, 0 connect-time |
| It is a real round trip, not a cache hit | **Verified.** Denials take 320 to 710ms |
| The server's account is of the same token | **Verified.** Token id matches byte for byte |
| The server validates independently | **Verified in code.** Real signature check, RS256 pinned |
| Nothing changed in the tenant between allow and deny | **Verified.** Repeatable in either order |

---

## What NOT to claim

Getting caught overstating one thing costs you the credibility of everything else.

**Do not say Okta is contacted over the network on literally every call.** The gateway
*evaluates* every call, and it caches Okta's answer for a few seconds, so two identical calls
in quick succession may reuse the previous answer. Say: *the gateway checks every call, and
remembers the answer for a few seconds.*

That same window is also how long a deactivated agent could still get through, so it is a
real tradeoff rather than a free performance setting. Shorter means faster to bite and more
traffic to Okta.

**Do not claim revocation was demonstrated** unless you actually run that act. The kill
switch works, but it is optional in the run sheet and was not part of the verified set above.

**Do not claim the server was shown refusing a bad token.** The code path is verified and the
protection is real, but no bad token was presented during this session, so that second line
of defence is present and untested today. Say it that way.

**Expect to be asked why the agent can still SEE a tool it may not use.** Listing tools is
not gated; running them is. Hiding it would add no control, since a hidden tool still has to
be refused when called directly, and gating discovery stops the gateway registering tools at
all. This is a deliberate choice, not a leak.

**Scope narrowing is not caught per call.** Switching an agent off is caught on the next call.
*Reducing* what it may do takes effect when the connection is next established, because the
token already in flight carries the scopes it was granted at connect.
