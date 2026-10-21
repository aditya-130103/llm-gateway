# Request Lifecycle

## Happy path — cache hit

```
Client sends: POST /v1/chat/completions
              Authorization: Bearer <team-token>
              {"model": "gpt-4.1-mini", "messages": [...]}

1. AUTH
   Extract bearer token → SHA-256 → lookup in team map
   No match → 401, stop

2. RATE LIMIT
   Redis: read token bucket (tokens, last_refill_time)
   Refill tokens based on elapsed time
   tokens >= 1 → subtract 1, save state (atomic Lua), continue
   tokens < 1  → 429, stop

3. SEMANTIC CACHE LOOKUP
   Embed last user message → float32 vector
   Redis KNN search: nearest stored vector
   cosine distance <= (1 - similarity_threshold) → CACHE HIT
     └── Write stored response to client, log provider="cache", stop

4. ROUTE TO PROVIDER  (cache miss only)
   For each upstream in order:
     Circuit breaker OPEN? → skip
     index > 0? → swap model field in request body to upstream's configured model
     proxy.Forward → upstream provider
     200 → RecordSuccess, write response to client, store in cache (goroutine), stop
     4xx → return to client immediately, no fallback (permanent client error)
     5xx → RecordFailure, try next upstream

   All upstreams exhausted → 502

5. LOG
   One structured JSON line: team, requestID, provider, latency, statusCode, tokens
```

---

## Failure paths

### Rate limit exceeded
```
Step 2: tokens < 1
→ 429 {"error": "rate limit exceeded"}
→ No LLM call, no token consumed, no cost
```

### Primary provider circuit open
```
Step 4: openai CB is OPEN
→ Skip openai entirely (no timeout wasted)
→ Try anthropic → swap model → send request
→ Log shows provider="anthropic"
→ If all upstreams open/failing → 502
```

### Primary provider returns 5xx
```
Step 4: request sent to openai → 5xx
→ RecordFailure(openai)
→ If failure count >= threshold → CB trips to OPEN
→ Try next upstream (anthropic), swap model
→ On success: client sees 200, log shows provider="anthropic"
```

### All providers down
```
Step 4: openai → 5xx, anthropic → 5xx, gemini → 5xx
→ 502 {"error": "all upstream providers unavailable"}
→ No cache write
```

### Client sends bad request
```
Step 4: provider returns 400
→ Return 400 to client immediately
→ No fallback (same body will fail on every provider)
→ CB not updated (not a provider health issue)
```

### Embedding fails (cache miss, provider call still works)
```
Step 3: embedding API call fails
→ Treat as cache miss, proceed to provider call
→ Skip cache write after response (no vector to store)
→ Client still gets a response — embedding failure is non-fatal
```

---

## Circuit breaker state transitions

```
CLOSED ──(N failures)──→ OPEN ──(cooldown)──→ HALF-OPEN
  ↑                                                │
  └──────────(probe success)──────────────────────┘
                                                   │
  OPEN ←──────────────(probe failure)──────────────┘
```

- **CLOSED**: normal, all requests go through
- **OPEN**: provider failing, all requests skipped; no timeouts wasted
- **HALF-OPEN**: exactly one probe allowed (concurrent probes blocked via `probeInFlight` guard); success → CLOSED, failure → OPEN again

Threshold and cooldown are set at startup (`cb.NewCB(threshold, cooldown)`). Current defaults: 10 failures, 10s cooldown.

---

## Streaming (not implemented)

SSE passthrough is not implemented. The gateway buffers the full upstream response before writing to the client. Streaming would require casting `http.ResponseWriter` to `http.Flusher` and flushing each chunk, accumulating the body until the final `[DONE]` event for cache write and token counting.
