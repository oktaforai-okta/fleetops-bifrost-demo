# How it works

Written for a reader who will never run this. No code, no commands, no product jargon.
If you can read this page you should be able to explain the idea to a colleague.

---

## The problem

An AI agent cannot do anything useful until it is allowed to reach a real system. Reading
a record, sending a message, moving a vehicle. Something has to grant that access.

Today, almost every deployment grants it the same way: one account, shared by everything.
The agents are handed a single set of credentials, and every request they make arrives at
the target system looking identical.

That has three consequences, and they compound.

**You cannot tell which agent did what.** The audit log records the shared account. If
twelve agents use it, an incident narrows to twelve suspects and stops there.

**You cannot stop one without stopping all of them.** Turning off the shared account turns
off every agent that depends on it. So in practice nobody turns it off, and the
credential stays live because revoking it is too expensive.

**Permissions drift to the widest agent's needs.** The shared account has to cover
everything anyone might do, so the read-only agents carry write access they never use.

None of this is a configuration mistake. It is what happens when the identity given to an
agent has no room to say who is acting.

---

## What changes here

Every call now carries two separate facts: **who asked**, and **who acted**.

Those are different parties, and keeping them apart is the whole point. A scheduling
service starts a job. An agent carries it out. The system on the other end is told both,
and can log both, and can decide on both.

The credential that carries those two facts is issued by Okta, one call at a time, and it
expires in minutes rather than living in a config file. If Okta will not issue it, the
call does not happen.

---

## The four parties

```
        1. THE SERVICE                      2. THE AGENT
        that asked for the work             that did the work
        e.g. a nightly scheduler            e.g. this one agent,
        or a case-management app            not the fleet of them
                  |                                  |
                  +----------------+-----------------+
                                   |
                                   v
                        +--------------------+
                        |     3.  OKTA       |
                        |      DECIDES       |
                        |                    |
                        | "may this service  |
                        |  have this agent   |
                        |  do this thing?"   |
                        +--------------------+
                                   |
                             yes  /  no
                                   |
                                   v
                        +--------------------+
                        |   4.  THE GATEWAY  |
                        |      ENFORCES      |
                        |                    |
                        | asks Okta on every |
                        | single call, and   |
                        | refuses when the   |
                        | answer changes     |
                        +--------------------+
                                   |
                              only if yes
                                   |
                                   v
                        +--------------------+
                        |   the real system  |
                        | vehicles, records, |
                        |    money, orders   |
                        +--------------------+
```

### The division of labour

**Okta decides.** It holds the policy, and it answers one question: would I issue this
agent a credential for this thing, right now.

**The gateway enforces.** It asks that question and it obeys the answer. It holds no
policy of its own.

That split is deliberate. Policy in one place, enforcement in the path of the traffic.
Neither can be quietly bypassed by changing the other.

---

## Why the gateway is load-bearing, not incidental

This is the part that is easy to skip and expensive to skip.

**A credential that has been issued cannot be taken back.** That is not a flaw in Okta.
It is how the whole industry's access tokens work. The credential is a signed statement
that was true when it was made. Nothing can reach out and un-say it.

So consider what happens when an agent misbehaves and you deactivate it.

- The deactivation is instant in Okta. It will not issue that agent anything new.
- The credential the agent is already holding keeps working until it expires on its own.

The gap between those two is the whole risk. It is short, minutes rather than days,
because these credentials are deliberately short-lived. But "the agent keeps working for
a few more minutes after you switch it off" is not something you want to discover during
an incident.

Closing that gap needs one thing: **something that keeps asking.** Not once at the start
of the session, but on every single call.

That something is the gateway. It sits in the path, so it is the only component that sees
every call and can refuse one. This is why the gateway is not simply a convenient place
to put the integration. It is the only place the integration can go.

---

## What this does and does not do

Being precise here matters more than sounding impressive. Anyone who buys the wider claim
and later discovers the narrower reality will trust the rest of it less.

### It does

| | |
|---|---|
| Names both parties on every call | The service that asked, and the agent that acted, separately |
| Puts the decision in Okta | Policy lives with your identity provider, not in the gateway |
| Re-checks continuously | Every call, not once per session |
| Refuses by permission, not by outage | A denied call is denied because policy says so, and the refusal says which permission was missing |
| Lets the target system verify independently | The receiving system checks the credential itself, so bypassing the gateway does not bypass authorization |

### It does not

| | Why it matters to you |
|---|---|
| **No per-object permissions** | Okta can say an agent may dispatch vehicles. It cannot say whether it may dispatch *this particular* vehicle. Object-level rules need a separate fine-grained authorization layer above this one |
| **Credentials are bearer credentials** | Anyone holding one can use it. There is no cryptographic binding to the holder. The mitigation here is that they are short-lived and re-checked, not that they are unstealable |
| **No sender-constrained tokens (DPoP)** | Not implemented. Worth asking about if your threat model includes credential theft in transit |
| **Tightening permissions applies to the next session** | Turning an agent off is caught on the next call. Narrowing what an agent may do is picked up when the connection is next established |
| **This is a demonstration, not a product** | It proves the integration path works against a real tenant. It is not hardened, monitored, or supported |

---

## What was actually proven, and how

Claims on this page are not architectural aspiration. The following ran against a real
Okta tenant, through a real gateway, to a real tool.

**One agent, two tools, opposite outcomes, no tenant change in between.**

| The call | The permission it needs | Was the agent granted it | Result |
|---|---|---|---|
| Read telemetry | read | Yes | **Succeeded.** The tool returned data, and named both the service and the agent |
| Dispatch a vehicle | dispatch | No, never granted | **Refused by Okta**, naming the missing permission |

Nothing was reconfigured between those two. The difference is entirely what the agent was
permitted to do. Run them in either order, as many times as you like, and each behaves the
same way.

That is the claim in its strongest available form. A demo that only shows a success proves
that plumbing works. A demo where the same agent is allowed one thing and refused another,
with nothing changed in between, shows that a decision is being made.

**One detail worth flagging honestly.** The credential carries a structure naming each
party and labelling what kind of thing it is, a service or an agent. That structure is not
described in Okta's published developer documentation. We confirmed it by decoding real
credentials from a live tenant. It behaves consistently, and we are telling you where the
knowledge comes from rather than implying it is documented.

---

## Questions you are likely to be asked

**Does this replace our gateway?** No. It adds to one. The gateway keeps doing its job and
gains the ability to ask Okta about the agent.

**What happens if Okta is unreachable?** Calls are refused. That is configurable, and the
default is to refuse. Failing open is available as a deliberate, recorded choice, which is
the only way it should ever be on.

**How quickly does turning an agent off take effect?** On the next call. As configured today
there is no caching of the answer at all, so every single call asks Okta afresh. The
trade-off is one extra round trip per call, which is a deliberate choice.

**Can we see which agent did something, after the fact?** Yes. Both parties are on the
credential and in the target system's own logs, not only in the gateway's.

**Is any of this specific to one vendor's agents?** No. The agent is registered in Okta as
its own identity. What matters is that calls pass through the gateway.

---

For the engineering detail, the setup steps, and the failure modes, see
[../README.md](../README.md) and [RUNBOOK.md](RUNBOOK.md).
