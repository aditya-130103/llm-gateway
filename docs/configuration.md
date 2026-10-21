# Configuration Reference

Copy `config.example.yaml` to `config.yaml` and fill in values. `config.yaml` is gitignored.

---

## `upstream[]`

List of LLM providers tried in order. Primary is index 0; the rest are fallbacks.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Identifier used in logs and circuit breaker map |
| `base_url` | string | yes | Provider base URL (without path) |
| `api_key` | string | yes | Provider API key — never logged or returned to clients |
| `model` | string | yes | Model to use when this upstream is a fallback (index > 0). For index 0, the client's `model` field is used as-is |

---

## `teams[]`

One entry per team that can send requests to the gateway.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Team identifier — appears in logs |
| `key_hash` | string | yes | SHA-256 hex of the team's bearer token. Generate: `echo -n "token" \| sha256sum` |
| `email` | string | yes | Oncall contact — used for alerts |
| `rpm_limit` | int | yes | Max requests per minute for this team |

---

## `redis`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `host` | string | — | Redis hostname |
| `port` | int | — | Redis port (typically 6379) |
| `password` | string | `""` | Redis auth password; leave empty if not required |

Redis is used for both rate limiter token buckets and the semantic cache vector index.

---

## Environment variable overrides

These override the corresponding `redis` block values at startup. Useful in containerised deployments where secrets come from environment variables rather than config files.

| Variable | Overrides |
|----------|-----------|
| `REDIS_HOST` | `redis.host` |
| `REDIS_PORT` | `redis.port` |
| `REDIS_PASSWORD` | `redis.password` |

---

## `embedding`

| Field | Type | Description |
|-------|------|-------------|
| `base_url` | string | Embedding API endpoint (e.g. `https://api.openai.com/v1/embeddings`) |
| `api_key` | string | API key for the embedding service |
| `model` | string | Embedding model name (e.g. `text-embedding-3-small`). Must produce 1536-dimensional vectors to match the Redis index |
| `similarity_threshold` | float | 0.0–1.0. A lookup is a cache hit when cosine similarity >= this value. Start with `0.85`; lower it if too many misses, raise it if cached answers are too dissimilar |
| `ttl_seconds` | int | Cache entries expire after this many seconds. `86400` = 24 hours |

> **Note:** the Redis vector index is created with `DIM=1536`. If you switch to a model that produces a different dimensionality (e.g. `text-embedding-3-large` = 3072), drop and recreate the index, or update the `embeddingDim` constant in `internal/cache/cache.go`.
