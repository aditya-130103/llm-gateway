# M4 Decisions — Semantic Cache

M4 added a semantic cache layer: instead of exact string matching on prompts, the gateway embeds each prompt into a vector and finds semantically similar cached responses. "What is the capital of France?" and "Tell me the capital city of France" should hit the same cache entry. These decisions define how the cache is stored, when it's populated, and what counts as a hit.

---

## Decision 006 — Storage: Redis vector search over pgvector

### What we decided

Use Redis vector search (RediSearch) for the semantic cache — the same Redis instance used by the rate limiter.

### What we rejected

**pgvector** — a PostgreSQL extension that adds vector similarity search to Postgres.

### Why

**TTL**: cache entries should expire. Cached answers go stale — a response about current events from last week may be wrong today. Redis supports per-key TTL natively (`EXPIRE`). In PostgreSQL, TTL requires either a background cleanup job or a scheduled `DELETE WHERE created_at < now() - interval`. That's extra moving parts for a system that's explicitly a cache, not a source of truth.

**Existing dependency**: Redis is already in the stack for rate limiting. Adding pgvector means a new dependency (PostgreSQL) alongside Redis. Using Redis for both means one fewer system to deploy, monitor, and back up.

**Speed**: Redis is an in-memory store. pgvector queries hit disk for most deployments. For a cache, in-memory lookup latency is order of magnitudes lower.

**Durability**: a cache is not a source of truth. Losing cache data on a Redis restart is acceptable — the cache simply warms back up as new requests come in. PostgreSQL's durability guarantees are wasted on cache data.

---

## Decision 019 — Embedding config: separate from upstream config

### What we decided

The embedding API has its own config block (`embedding:` in config.yaml), independent of the `upstream:` list.

### What we rejected

**Reusing `cfg.Upstreams[0].BaseUrl`** — embedding requests piggyback on the primary upstream's base URL and key.

### Why

Chat completions and embeddings are different API endpoints that may point to different services, use different keys, or be billed separately. A deployment might:
- Route chat to a fine-tuned internal model but use `text-embedding-3-small` from OpenAI directly
- Use a different API key for embeddings to separate billing
- Point chat at a LiteLLM proxy but embeddings at the raw OpenAI endpoint for lower latency

Coupling embedding config to `upstream[0]` means any of these configurations require a workaround. Separate config is explicit, independently rotatable, and doesn't break when upstreams are reordered.

The embedding config is also the right place to control cache behaviour (`similarity_threshold`, `ttl_seconds`) — concepts that have no meaning in the upstream chat config.

---

## Decision 020 — Similarity scoring: cosine distance, not cosine similarity

### What we decided

Cache hit condition: `score <= (1 - similarity_threshold)`, where `score` is the raw value returned by Redis KNN search.

### What we rejected

**Using the Redis score directly as cosine similarity.**

### Why

This is a precision bug, not an opinion. Redis RediSearch KNN returns **cosine distance**, not cosine similarity:

- Cosine similarity range: [-1, 1] where 1 = identical
- Cosine distance range: [0, 2] where 0 = identical

For normalized text embedding vectors (which `text-embedding-3-small` produces), the range is [0, 1].

A configured `similarity_threshold: 0.85` means: "accept responses where the prompts are at least 85% similar." In cosine distance terms, that's a maximum allowed distance of `1 - 0.85 = 0.15`.

The check: `score <= 0.15` → cache hit.

If you inverted this and used `score >= 0.85` (treating the distance as similarity), nearly identical prompts — which produce distances close to 0 — would fail the check (`0.001 >= 0.85` is false). The cache would almost never hit. This bug is subtle because it's not a crash — the cache silently stops working.

---

## Decision 010 — Cache write timing: after full response, not during streaming

### What we decided

Write the prompt → response pair to the cache only after the complete response is received from the provider.

### What we rejected

**Writing partial chunks to the cache during streaming** — store each chunk as it arrives, assemble in the cache.

### Why

A partial response is not a useful cache entry. If a future request hits this entry mid-stream, it would receive an incomplete answer — truncated mid-sentence.

The full response is only available after the provider sends `[DONE]`. At that point, the assembled response is complete, coherent, and safe to cache. Any future request that hits this cache entry gets a complete answer.

**Performance note**: this means cache hits are actually faster than streaming — a cache hit returns the full response in a single write, while a live LLM call produces tokens one by one over 500ms–2s. Cache hits feel instant.

---

## Decision 021 — Cache store: fire-and-forget goroutine with context.Background()

### What we decided

After sending the LLM response to the client, call `cache.Store()` in a goroutine using `context.Background()`.

### What we rejected

**Blocking store** — wait for the cache write to complete before returning the response to the client.

**Using `r.Context()` in the goroutine** — pass the request's context to the background cache write.

### Why

**Why a goroutine:** `cache.Store` makes an embedding API call (~30–50ms round trip) before writing to Redis. Blocking the client response on this adds 30–50ms of latency to every cache-miss request for no benefit. The client already has their answer. Caching is a best-effort background operation.

**Why `context.Background()` not `r.Context()`:** The request context is cancelled immediately when the HTTP handler returns — which happens the moment the response is written to the client. If the goroutine uses `r.Context()`, the embedding API call is cancelled mid-flight: the goroutine starts, sends the HTTP request to the embedding API, and before the response comes back the context is cancelled and the call fails silently. The entry never gets stored. Using `context.Background()` gives the goroutine an unbounded context that survives the handler returning.

The tradeoff: if the gateway shuts down before the goroutine completes, the cache entry is lost. This is acceptable — the cache is not a source of truth, and the graceful shutdown waits 10 seconds for in-flight request handlers, not for background cache writes.

---

## Decision 022 — Cache hit response: return stored body verbatim

### What we decided

On a cache hit, write the stored response bytes to the client unchanged.

### What we rejected

**Re-wrapping the cached body** — parse the stored response and reconstruct a new response object with a fresh `id`, updated `created` timestamp, or `cached: true` field.

### Why

The stored body is the exact JSON response the provider returned, including all metadata: `id`, `object`, `model`, `usage`, `choices`. Clients that parse provider responses expect this schema. Returning it verbatim means a cache hit is structurally identical to a live LLM response — no special handling required.

Token counts in the `usage` field reflect the original call that populated the cache. This is intentional: the tokens were generated (and paid for) at that time. A cache hit costs zero tokens, but reporting `0` in `usage.prompt_tokens` would break clients that track their own usage.

One consequence: the `id` field is the original request's ID, not a fresh one. If clients log response IDs and need uniqueness per request, this will show duplicate IDs on cache hits. This is a known limitation — a future improvement could stamp a new ID on cache hits without altering the rest of the response.
