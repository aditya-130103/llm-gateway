# Runbook

## Running locally

```bash
# 1. Start Redis with vector search support
docker run -p 6380:6379 redis/redis-stack-server:latest

# 2. Copy and fill in config
cp config.example.yaml config.yaml
# edit config.yaml with your API keys and team tokens

# 3. Run
go run .
# → Gateway running on http://localhost:8030

# 4. Send a test request
curl -X POST http://localhost:8030/v1/chat/completions \
  -H "Authorization: Bearer <your-team-token>" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4.1-mini", "messages": [{"role": "user", "content": "hello"}]}'
```

---

## Health check

```bash
curl http://localhost:8030/
# → LLM Gateway is running!
```

Use `GET /` as the liveness probe target. In Kubernetes this is configured in `k8s/deployment.yaml`.

---

## Environment variable overrides

Override Redis config without changing `config.yaml`. Useful in Docker Compose and Kubernetes where secrets are injected as env vars.

| Variable | Effect |
|----------|--------|
| `REDIS_HOST` | Overrides `redis.host` |
| `REDIS_PORT` | Overrides `redis.port` |
| `REDIS_PASSWORD` | Overrides `redis.password` |

---

## Graceful shutdown

The server handles `SIGINT` and `SIGTERM`. On signal:
1. Stops accepting new connections
2. Waits up to 10 seconds for in-flight requests to complete
3. Exits cleanly

```bash
kill -SIGTERM <pid>
```

---

## Tuning knobs

### Similarity threshold (`embedding.similarity_threshold`)

Controls how similar a cached prompt must be to trigger a cache hit.

| Value | Behaviour |
|-------|-----------|
| `0.95` | Very strict — only near-identical prompts hit the cache |
| `0.85` | Recommended starting point |
| `0.70` | Loose — semantically related but differently worded prompts hit the cache; risk of stale/off-topic answers |

Start at `0.85`. Monitor cache hit rate; lower if too few hits, raise if clients report wrong cached answers.

### Cache TTL (`embedding.ttl_seconds`)

How long a cached prompt→response pair lives in Redis.

- `86400` (24h) — good default for general-purpose queries
- `3600` (1h) — use for time-sensitive topics (news, prices, live data)
- `604800` (7d) — use for stable reference queries (docs, definitions)

### Circuit breaker threshold

Set in `main.go` at startup: `cb.NewCB(threshold, cooldownPeriod)`.

Current defaults: 10 failures to trip, 10s cooldown before probe.

- Lower threshold (e.g. 5) — trips faster, protects the system sooner, but more sensitive to transient errors
- Higher threshold (e.g. 20) — more tolerant, better for flaky providers with occasional errors

### Rate limits (`teams[].rpm_limit`)

Per-team requests per minute. Enforced via Redis token bucket. Changes require a config reload (restart).

---

## Logs

Every request emits one structured JSON line to stdout:

```json
{
  "time": "2026-10-11T10:00:00Z",
  "level": "INFO",
  "msg": "request logged",
  "team": "team-a",
  "requestID": "a1b2c3d4e5f60708",
  "provider": "openai",
  "latency": 842,
  "statusCode": 200,
  "promptTokens": 120,
  "completionTokens": 85
}
```

On failure, level is `ERROR` and an `error` field is added.

`provider: "cache"` means the response was served from the semantic cache — no LLM call was made.

---

## Redis index

The semantic cache index is created automatically on startup (`FT.CREATE idx:cache`). If it already exists, startup continues silently.

To reset the cache (drop and recreate the index):

```bash
redis-cli FT.DROPINDEX idx:cache DD   # DD also deletes all cached keys
# restart the gateway to recreate the index
```
