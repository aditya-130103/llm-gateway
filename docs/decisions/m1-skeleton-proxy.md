# M1 Decisions — Skeleton Proxy

M1 established the gateway's identity layer: how teams authenticate, how keys are structured, stored, and managed. These decisions set the security posture for everything built on top.

---

## Decision 007 — Security model: single upstream key, per-team downstream keys

### What we decided

The gateway holds the real provider API keys. Each team gets a separate downstream key that only works against the gateway. Teams never see or hold an OpenAI/Anthropic key directly.

### What we rejected

**Each team holds their own real provider key** — teams manage their own OpenAI keys and pass them directly to the provider.

### Why

The blast radius of a key leak scales with what the key can do. A leaked downstream key can only consume one team's quota — it's useless outside the internal network and revocable in one config change. A leaked real provider key has no cap, no scope, and exposes the entire org's billing account. Beyond blast radius:

- **Rotation**: rotating the upstream key is one change in one config. No team coordination, no cascading redeploys across every team's service.
- **Per-team control**: downstream keys enable per-team rate limiting, per-team metering, and instant revocation without any interaction with the provider.
- **Auditability**: every request is identified by team. With teams holding provider keys directly, there is no central point to attribute usage.

---

## Decision 011 — API key storage: hash only, never raw

### What we decided

Store only the SHA-256 hash of each team's bearer token in config. The raw token is returned to the team once at provisioning and never stored anywhere.

### What we rejected

**Storing raw keys in config/database** — simpler to implement, allows key recovery if a team loses their token.

### Why

Storing raw keys means a single config or database breach hands every team's credentials to an attacker. They can immediately make provider API calls at the org's expense, impersonate teams, and bypass rate limits.

With hash-only storage, a breach exposes only hashes. Without the original token, a hash is useless for authentication. There is no practical way to reverse SHA-256 for a 32-byte random key.

Validation path:
```
incoming token → SHA-256 → lookup in team map by hash → match found = valid team
```

The cost: if a team loses their token, it cannot be recovered — they must be issued a new one. This is the correct tradeoff: key recovery is a social engineering attack surface.

---

## Decision 012 — Key rotation: dual hash with grace period

### What we decided

During rotation, store both the new hash (`active_hash`) and the old hash (`previous_hash`) with an expiry timestamp. Both are valid during the grace period (default 24h). After expiry, the old hash is ignored.

### What we rejected

**Immediate single-hash swap** — the moment a team rotates, the old key stops working instantly.

### Why

Teams run distributed services. When they rotate a key, not every replica or service updates at the same instant. An immediate swap causes downtime during a rolling deploy: Pod A still holds the old key and starts failing while Pod B already uses the new one.

A 24-hour grace period allows zero-downtime rotation:
```
Hour 0:  team generates new key, registers new hash
         old key still valid
Hour 4:  all services updated to new key
Hour 24: old hash expires, removed from team record
```

The grace period is configurable. A tighter window (e.g. 1h) is appropriate when teams have fast deploy pipelines.

---

## Decision 013 — Key revocation: mark revoked, invalidate cache immediately

### What we decided

Revocation marks the team record as `revoked=true` and immediately removes the team's entry from the Redis auth cache. The record itself is not deleted.

### What we rejected

- **Delete the row on revocation** — team loses their account and must re-register.
- **DB update only, no cache invalidation** — the revoked key stays valid until the cache TTL expires.

### Why

Revocation is a security response, not account deletion. A team whose key is compromised needs the key killed immediately, not their account and history wiped. After revocation, they can rotate (generate a new key) to regain access — no re-registration.

The cache invalidation is the critical part. Without it, a revoked key cached in Redis remains valid for minutes — in a compromised-key scenario, every second counts. Deleting the cache entry means the next request forces a DB lookup, sees `revoked=true`, and returns 401.

Auto-revocation trigger (not yet implemented): anomaly detection (e.g. 10× normal traffic in a short window) can auto-revoke and fire email alerts to the team and security contact.

---

## Decision 014 — Key storage by environment: config file in dev, database in prod

### What we decided

- **Dev**: YAML config file, loaded at startup, teams defined statically as a `map[string]Team`.
- **Prod**: Database (PostgreSQL or equivalent) with a Redis cache layer to avoid per-request DB hits.

### What we rejected

Using a database in dev; using config files in prod.

### Why

Dev needs zero infrastructure to run. A YAML file with a few team entries is all that's needed to test auth, rate limiting, and routing. Requiring a database to start locally adds setup friction for no benefit.

Prod has different requirements: teams are added, keys are rotated, and keys are revoked — all without redeployment. A config file can't do this: every key management operation becomes a deploy. A database with a Redis cache layer (cache TTL: a few minutes) gives live updates while keeping per-request latency at sub-millisecond.

The current implementation uses the config file approach. The interface between `auth.Auth` and its data source is deliberately thin — swapping in a DB-backed team store later requires changing only one function.

---

## Decision 015 — Key format: prefixed random bytes

### What we decided

Key format: `llmgw-<32 random bytes, base62-encoded>`. Example: `llmgw-a7Kf2mNpQ3vR8xYz...`

### What we rejected

**UUID** — e.g. `550e8400-e29b-41d4-a716-446655440000`. No prefix, no entropy guarantee, no secret scanning support.

### Why

**The prefix is the important part.** Secret scanning tools (GitHub Advanced Security, GitLeaks, TruffleHog) detect leaked credentials by matching known prefixes. Registering `llmgw-` as a pattern means a key accidentally committed to a public repo triggers an automatic alert before anyone can use it.

A UUID in a log or bug report is ambiguous — it could be a request ID, a database row ID, anything. `llmgw-...` is immediately identifiable as a gateway credential.

**Entropy**: 32 random bytes = 256 bits of entropy. A brute force attack trying 1 trillion keys per second would take longer than the age of the universe to exhaust the space.
