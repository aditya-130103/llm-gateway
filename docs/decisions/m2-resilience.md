# M2 Decisions — Resilience

M2 added fault tolerance: the gateway now survives provider outages without exposing failures to clients. These decisions define how the gateway detects, responds to, and recovers from upstream failures.

---

## Decision 004 — Circuit breaker per provider

### What we decided

Implement a per-provider circuit breaker. When a provider fails repeatedly, the circuit trips to OPEN and subsequent requests skip that provider entirely — no HTTP call, no timeout wait.

### What we rejected

**Timeout-based fallback only** — try the provider, wait for it to fail or timeout, then move on.

### Why

Without a circuit breaker, an outage at OpenAI means every incoming request still tries OpenAI first. At 100 req/sec and a 30-second timeout, that's 3,000 requests simultaneously waiting for a timeout before failing over. The gateway accumulates 3,000 goroutines, each holding a connection — memory pressure builds, latency spikes for all teams, and the fallback provider also starts falling behind.

A circuit breaker detects the pattern after N consecutive failures and opens. From that point, requests skip the provider in microseconds — no goroutine held, no connection consumed, no timeout waited. The total cost of the failed provider becomes near-zero.

This is the difference between an outage being invisible to clients (traffic silently routes to the fallback) versus being visible as a slowdown (every request takes timeout seconds longer than normal).

---

## Decision 017 — Circuit breaker: three states, not two

### What we decided

Three states: **CLOSED** (healthy, requests go through), **OPEN** (failing, skip entirely), **HALF-OPEN** (one recovery probe allowed).

### What we rejected

**Two-state breaker** (only CLOSED and OPEN, manual reset to reopen).

### Why

A two-state breaker has a fundamental operational problem: once OPEN, it stays OPEN until something resets it. That means either:
- Manual intervention every time a provider recovers (not acceptable at scale), or
- An automatic timer that flips it back to CLOSED, which immediately floods a recovering provider with full traffic.

HALF-OPEN solves this with a controlled handshake: after a cooldown period, one probe request goes through. If it succeeds, the provider is healthy and the circuit closes. If it fails, the circuit re-opens for another full cooldown. This prevents flooding a fragile provider that just came back up.

```
CLOSED ──(N failures)──→ OPEN
                          │
                    (cooldown passes)
                          │
                          ▼
                       HALF-OPEN
                       │       │
                (probe OK) (probe fails)
                       │       │
                    CLOSED    OPEN
```

**Why fixed cooldown, not exponential backoff between probes**: the provider either recovered or it hasn't. Waiting progressively longer doesn't help — a fixed window (e.g. 10s) is simpler and equally effective.

---

## Decision 009 — Failover target: different provider, not different model

### What we decided

On provider failure, route to the next provider in the upstream list — a different company, different infrastructure, independent failure domain.

### What we rejected

**Switching to a different model on the same failing provider** — e.g. if `gpt-4o` fails, try `gpt-4o-mini`.

### Why

If OpenAI has an infrastructure outage, all their endpoints are affected. `gpt-4o`, `gpt-4o-mini`, `gpt-3.5-turbo` all route to the same servers. Switching models on the same provider during an outage is noise — it hits the same broken system. The point of failover is escaping the failing dependency entirely.

A different provider (OpenAI → Anthropic → Gemini) means a different company's API, different infrastructure, independent failure modes. OpenAI going down does not take Anthropic down.

**Model swap on fallback**: when routing to a fallback provider, the request body's `model` field is rewritten to the fallback provider's configured model. A client sending `gpt-4o-mini` cannot be sent to Anthropic verbatim — Anthropic doesn't know that model name. The router handles this transparently.

---

## Decision 016 — Retry policy: 5xx only, never 4xx

### What we decided

On a 5xx response from a provider, move to the next provider. On a 4xx response, return the error to the client immediately with no retry or fallback.

### What we rejected

**Retrying 4xx responses on other providers**.

### Why

5xx means transient server-side failure — the provider is struggling, not the request. The same request might succeed on a different provider or at a different time.

4xx means permanent client-side failure — the request itself is wrong. A malformed prompt, an invalid model name, an exceeded context window. The same request will produce the same 4xx on every provider. Retrying wastes time and delays the error the client needs to act on.

The circuit breaker is also not updated on 4xx. A provider returning 400 is healthy — it's doing its job correctly by rejecting a bad request. Counting 4xx toward the failure threshold would trip the circuit for the wrong reason.

```
Request → OpenAI
  200 → success, done
  400 → return 400 to client, no retry, CB unchanged
  500 → RecordFailure(openai), try Anthropic
  503 → RecordFailure(openai), try Anthropic
```

---

## Decision 008 — Retry scope: move to next provider, no within-provider retries

### What we decided

On failure, move to the next configured upstream immediately. No retries against the same provider before failing over. Accept rare duplicate LLM calls as a tolerable cost.

### What we rejected

**Heavy idempotency machinery** — a request deduplication store that prevents any provider from receiving the same request twice.

### Why

LLM calls are not idempotent in the general case — two identical prompts can produce different outputs. But the *consequence* of a duplicate call is a wasted token generation, not a correctness violation. Compare to a payment API where a duplicate charge is a real harm.

An idempotency store adds significant complexity: every request needs a unique ID, a shared store (another Redis key or DB row) to check before forwarding, and cleanup logic. The semantic cache already absorbs most accidental duplicates — a retry on the same prompt hits the cache if the first call completed and was stored.

**Current behaviour**: if OpenAI returns 500, we immediately try Anthropic. If the OpenAI call actually completed on their side but the 500 was a network error on the response path, the client gets a fresh response from Anthropic. OpenAI did work that was paid for but discarded. This happens rarely and costs a few cents — not worth the idempotency machinery.

---

## Decision 018 — Package split: `internal/cb` and `internal/router`

### What we decided

Resilience logic lives in two packages: `internal/cb` owns only health state; `internal/router` owns provider orchestration and fallback order.

### What we rejected

**Putting fallback logic inside `proxy.Forward`** — proxy tries the next provider itself when the current one fails.

### Why

`proxy.Forward` has a clear, testable contract: make one HTTP call to one provider, return the result. If it also handles fallback, it needs to know about other providers, the CB map, and fallback order — all concerns that belong one layer up.

`internal/cb` answers exactly one question: is this provider healthy right now? It has no knowledge of URLs, request bodies, or what to do when unhealthy. This makes it independently testable and reusable.

`internal/router` has the full picture — the upstream list, the CB map — and orchestrates: for each upstream, ask the CB, if allowed call the proxy, if the result is bad record the failure and move on. This is the only place that has to understand the full failover flow.

Separation also means changes to fallback strategy (e.g. adding exponential backoff within a provider before failing over) only touch `internal/router`, not `proxy` or `cb`.
