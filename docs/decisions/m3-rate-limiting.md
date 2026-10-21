# M3 Decisions — Rate Limiting

M3 added per-team rate limits enforced across all gateway replicas. The decisions here are about correctness under concurrency and scale — a rate limiter that only works on a single pod is not a rate limiter.

---

## Decision 001 — Algorithm: token bucket

### What we decided

Use a token bucket per team, backed by Redis. Each team has a bucket of capacity `rpm_limit`. Tokens drain on each request and refill continuously at a rate of `rpm_limit / 60` tokens per second.

### What we rejected

**Fixed window counter** — count requests in a rolling time window (e.g. 1-minute buckets). Simple: one Redis integer per team, increment on each request, reset at window boundary.

**Sliding window log** — store the timestamp of every request, count how many fall within the last N seconds. Accurate, but memory grows with request volume.

### Why

**Fixed window has a boundary exploit.** A team with a 100 RPM limit can send 100 requests at 11:59:59 and 100 more at 12:00:00 — 200 requests in 2 seconds, zero rate limiting applied. The window reset is a free pass.

**Sliding window log is memory-expensive.** At 1000 RPM, storing every request timestamp means 1000 Redis entries per team, growing without bound as long as traffic flows.

**Token bucket** rewards idle time. Unused capacity accumulates as tokens, so a burst after a quiet period goes through instead of being rejected. Teams with variable traffic patterns (quiet overnight, bursty during business hours) behave sensibly.

Refill math:
```
elapsed      = now - last_refill_time
refill       = elapsed × (rpm_limit / 60)
new_tokens   = min(rpm_limit, current_tokens + refill)
```

The `min(rpm_limit, ...)` cap is important: without it, a team idle for 10 minutes accumulates 600 tokens on a 60-RPM limit and can immediately burst 600 requests. The cap means idle time earns back up to the configured maximum, never beyond it.

Storage per team: two Redis fields — `tokens` (float) and `last_refill` (Unix timestamp). No per-request entries.

---

## Decision 002 — Storage: Redis over in-process memory

### What we decided

Store token bucket state in Redis, shared across all gateway replicas.

### What we rejected

**In-memory counter per replica** — each gateway pod maintains its own token bucket map.

### Why

The gateway is designed to run as multiple Kubernetes replicas behind a load balancer. In-memory counters are pod-local.

Consider a team with a 100 RPM limit and 4 gateway replicas. If requests distribute evenly, each pod sees 25 RPM — well under the 100 RPM limit each pod enforces. The team can actually send 400 RPM before any pod trips the limit. The rate limiter is measuring the wrong thing.

Redis is a single shared store. Every request from every team, regardless of which replica handles it, hits the same bucket. The count is accurate.

**Why not a distributed lock to synchronize in-memory state?** Lock overhead at high request rates (1000+ req/sec) is significant: every request must acquire, check, and release a lock, serializing throughput across replicas. Redis with a Lua script is faster and simpler.

---

## Decision 003 — Atomicity: Lua scripts over distributed locks

### What we decided

Use a Redis Lua script for the read-modify-write sequence of the token bucket check. The entire operation — read state, compute new tokens, decide allow/deny, write updated state — runs as a single atomic unit.

### What we rejected

**Distributed locks** (`SET key NX EX`) — acquire a lock, read, modify, write, release.

### Why

Redis is single-threaded for command execution. While a Lua script runs, no other client command can execute on that Redis instance. This gives true atomicity with zero coordination overhead.

The read-modify-write sequence without atomicity has a race:

```
Replica A reads: tokens = 1
Replica B reads: tokens = 1   ← race: both see 1 token
Replica A writes: tokens = 0  ← allow
Replica B writes: tokens = 0  ← also allow — double spend
```

Both replicas allowed the request. One token, two requests served.

With a Lua script, one of the two executions waits. The sequence is serialized, no double spend possible.

**Why not distributed locks?** Locks work, but under high traffic every request competes for the same lock key. At 1000 req/sec across 10 pods, 10,000 lock acquisitions per second on a single key creates a thundering herd. Contention adds latency; a lock that expires mid-hold causes correctness bugs. Lua scripts have none of these problems.

**Constraint**: keep the script short. Redis is single-threaded — a slow Lua script blocks every other client for its duration. The token bucket script does one HMGET, one computation, one HMSET, one EXPIRE — microseconds.
