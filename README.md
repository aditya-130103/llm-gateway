# LLM Gateway

A production-grade reverse proxy for LLM providers, written in Go. Teams send requests through one endpoint; the gateway handles authentication, rate limiting, semantic caching, resilience, and observability.

Built to demonstrate senior backend engineering: stateless horizontal scaling, distributed rate limiting, vector similarity search, and circuit-breaker-based resilience — all running on a single binary.

---

## Architecture

```
                        ┌──────────────────────────────────────────┐
Client                  │              LLM Gateway (Go)             │
POST /v1/chat/          │                                           │
completions  ──────────→│  Auth → Rate Limit → Cache → Router      │
Bearer <token>          │                          │                │
                        │                   Circuit Breaker         │
                        │                          │                │
                        │                       Proxy               │
                        └──────────────────────────────────────────┘
                                   │                    │
                                   ▼                    ▼
                                 Redis           LLM Providers
                           (token buckets +    (OpenAI / Anthropic
                            vector index)           / Gemini)
```

Each request flows through a fixed pipeline: authenticate the team, enforce the per-team rate limit, check the semantic cache, and if all miss — route to a provider with automatic fallback.

---

## What it does

| Capability | Implementation |
|-----------|---------------|
| **Multi-team auth** | SHA-256 hashed bearer tokens; zero plaintext secrets in config |
| **Per-team rate limiting** | Redis token bucket via atomic Lua script; correct under concurrent replicas |
| **Semantic caching** | Embed prompt → HNSW KNN search in Redis → cosine similarity threshold |
| **Multi-provider routing** | Tries upstreams in order; swaps model field on fallback |
| **Circuit breaker** | Per-provider CLOSED/OPEN/HALF-OPEN; single probe on recovery |
| **Structured logging** | One JSON line per request: team, requestID, provider, latency, tokens |
| **Prometheus metrics** | requests_total, tokens_total, cache_hits_total, request_duration_seconds |
| **Graceful shutdown** | SIGTERM drains in-flight requests (10s window) before exit |

---

## Quick start

```bash
# 1. Start Redis with vector search
docker run -p 6380:6379 redis/redis-stack-server:latest

# 2. Configure
cp config.example.yaml config.yaml
# fill in API keys and team tokens

# 3. Run
go run .

# 4. Call it
curl -X POST http://localhost:8030/v1/chat/completions \
  -H "Authorization: Bearer <team-token>" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4.1-mini", "messages": [{"role": "user", "content": "hello"}]}'
```

Or run the full stack (gateway + Redis) in one command:

```bash
docker compose up
```

---

## Docker

```bash
# Build
docker build -t llm-gateway:latest .

# Run with compose (gateway + Redis Stack)
docker compose up
```

Two-stage distroless build — final image is ~20 MB, runs as non-root.

---

## Kubernetes

```bash
# 1. Create namespace
kubectl apply -f k8s/namespace.yaml

# 2. Create config secret
kubectl create secret generic llm-gateway-config \
  --from-file=config.yaml=config.yaml \
  --namespace llm-gateway

# 3. Deploy
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
kubectl apply -f k8s/hpa.yaml
```

Deployment runs 2 replicas with Prometheus scrape annotations. HPA scales between 2 and 10 replicas at 70% CPU. Rate limiting stays correct under horizontal scale — all state lives in Redis.

---

## Testing

```bash
# Unit tests + race detector
go test -race ./...

# With coverage
go test -race -cover ./internal/...

# Integration tests (requires Redis)
REDIS_ADDR=localhost:6380 go test -race ./internal/ratelimiter/

# Benchmarks
go test -bench=. -benchmem ./internal/cb/ ./internal/auth/ ./internal/proxy/

# Static analysis
go vet ./...
golangci-lint run ./...
```

| Package | Coverage | What's tested |
|---------|----------|---------------|
| `auth` | 100% | valid token, wrong token, missing header, empty token, case sensitivity |
| `cb` | 100% | state transitions, concurrent access, single probe in HALF-OPEN |
| `config` | 100% | YAML loading, env var overrides, invalid input |
| `proxy` | 100% | 200/4xx/5xx responses, unreachable server, connection drop mid-body |
| `router` | 100% | primary success, fallback on open CB, 4xx stops chain, model swap |
| `ratelimiter` | — | integration, needs `REDIS_ADDR` |

Benchmark results (Apple M3 Pro):

| Benchmark | Result |
|-----------|--------|
| Circuit breaker `Allow()` | **4 ns/op, 0 allocs** |
| Auth (SHA-256 + map lookup) | 113 ns/op, 2 allocs |
| Proxy round-trip (localhost) | 54 µs/op |

---

## Load test

```bash
k6 run scripts/loadtest.js \
  -e GATEWAY_URL=http://localhost:8030 \
  -e TEAM_TOKEN=your-team-token
```

Results (5 concurrent users, LiteLLM upstream):

| Metric | Value |
|--------|-------|
| Throughput | 3.4 RPS |
| p95 latency | 1.99s |
| Success rate | 99.4% |

Gateway overhead is ~5ms (p95 from circuit-breaker benchmarks). The dominant latency is upstream LLM call time (~1s avg). Bottleneck is the LLM provider, not the gateway.

---

## Implementation notes

**Rate limiting atomicity** — the token-bucket refill and consume happen inside a single Lua script executed atomically in Redis. No race condition possible across multiple gateway replicas.

**Semantic cache** — every cache lookup embeds the query (1536-dim float32 vector), runs an HNSW KNN search, and checks the cosine distance against a configurable threshold. A distance ≤ `1 - similarity_threshold` is a hit. The same question phrased differently ("capital of France?" vs "what's France's capital?") returns the cached answer.

**Circuit breaker** — per-provider state machine with a `probeInFlight` guard: only one request is allowed through in HALF-OPEN state. Concurrent requests during recovery are rejected, preventing thundering-herd against a recovering provider.

**Context propagation** — `r.Context()` flows from the handler through the router into every upstream HTTP call. Requests are cancellable on client disconnect or graceful shutdown.

**Stateless design** — the gateway holds no per-request state. All state (token buckets, vector index) lives in Redis. Replicas can be added or removed without coordination.

---

## Milestones

| # | Focus | Status |
|---|-------|--------|
| M1 | Skeleton — auth, single provider, structured logging | ✅ |
| M2 | Resilience — circuit breaker, fallback, model swap | ✅ |
| M3 | Rate limiting — Redis token bucket, Lua atomicity | ✅ |
| M4 | Semantic cache — embed, HNSW vector search, TTL | ✅ |
| M5 | Observability — Prometheus metrics, /metrics endpoint | ✅ |
| M6 | Ship — Dockerfile, docker-compose, K8s + HPA, load test | ✅ |

---

## Docs

| Doc | What it covers |
|-----|----------------|
| [docs/architecture.md](docs/architecture.md) | Component map, package structure, dependency graph |
| [docs/request-lifecycle.md](docs/request-lifecycle.md) | Happy path, all failure paths, circuit breaker states |
| [docs/configuration.md](docs/configuration.md) | Every config field with type and purpose |
| [docs/runbook.md](docs/runbook.md) | Running locally, tuning knobs, log format, Redis ops |
| [docs/decisions/](docs/decisions/) | ADRs by milestone — what was chosen, what was rejected, why |
