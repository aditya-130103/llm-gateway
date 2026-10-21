# Architecture

## Component map

```
Client
  │
  ▼
┌─────────────────────────────────────────────────────────────┐
│                      LLM Gateway (Go)                       │
│                                                             │
│  Auth → Rate Limiter → Semantic Cache → Router              │
│                                            │                │
│                                     Circuit Breaker         │
│                                            │                │
│                                          Proxy              │
└─────────────────────────────────────────────────────────────┘
         │                       │
         ▼                       ▼
       Redis               LLM Providers
  (token buckets +       (OpenAI / Anthropic
   vector index)              / Gemini)
```

| Component | Package | Owns |
|-----------|---------|------|
| Auth | `internal/auth` | Validate downstream API key, resolve team identity |
| Rate limiter | `internal/ratelimiter` | Token bucket state per team, enforce RPM limits |
| Semantic cache | `internal/cache` | Prompt embeddings, similarity lookup, cache hits/misses |
| Router | `internal/router` | Provider list, fallback order, CB consultation |
| Circuit breaker | `internal/cb` | Per-provider CLOSED/OPEN/HALF-OPEN state; `probeInFlight` guard limits recovery to one concurrent probe |
| Proxy | `internal/proxy` | Single upstream HTTP call with context propagation; preserves actual status code on error |
| Config | `internal/config` | Config structs, YAML loading |

## What the gateway does not own

- **Auth server** — downstream keys are validated here, not issued. Key provisioning is out of band.
- **Billing** — metering exports token counts; cost tracking is the caller's responsibility.
- **Conversation history** — clients send full `messages` history in every request. The gateway is stateless.
- **Upstream provider keys** — held only in `config.yaml`, never exposed in responses or logs.

---

## Data flow

```
Downstream API key     Client → Gateway (Authorization header)
Prompt text            Client → Gateway (request body)
Prompt vector          Gateway → Redis (cache lookup + write)
Upstream API key       Gateway → Provider (header, never leaves gateway)
Response body          Provider → Gateway → Client
Token usage metadata   Provider → Gateway (parsed from response body)
```

### What never crosses the gateway boundary outward

- Upstream provider keys
- Other teams' cached responses
- Raw token bucket state

---

## Package dependency graph

```
main.go
  ├── internal/config
  ├── internal/auth        → internal/config
  ├── internal/ratelimiter → redis
  ├── internal/cache       → internal/config, redis
  ├── internal/cb
  ├── internal/proxy       → internal/config
  └── internal/router      → internal/proxy, internal/cb, internal/config
```

No package imports its caller. No circular dependencies.
