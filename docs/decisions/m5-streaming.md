# M5 Decisions — Streaming

M5 adds SSE streaming: tokens forwarded to the client as the provider produces them, instead of buffering the full response first.

---

## Decision 005 — Protocol: HTTP/1.1 SSE over gRPC

### What we decided

Use HTTP/1.1 with Server-Sent Events (SSE / chunked transfer encoding) for streaming responses. The gateway acts as a transparent SSE passthrough between the client and the provider.

### What we rejected

**gRPC (HTTP/2) streaming** — use a bidirectional or server-streaming gRPC connection between client and gateway.

### Why

**Compatibility**: OpenAI, Anthropic, and Gemini all expose HTTP/1.1 SSE streaming APIs. Every major LLM provider converged on this format. Mimicking it means existing clients — Python `openai` SDK, JavaScript `fetch` + `ReadableStream`, `curl` — work against the gateway without any changes or stub generation.

**Directionality**: LLM streaming is one-directional. The client sends one request; the server streams tokens back. HTTP/1.1 chunked transfer handles this exactly. gRPC's bidirectional streaming is designed for cases where both sides stream concurrently — unnecessary overhead here.

**Client diversity**: gRPC requires generated client stubs. Every team using the gateway in a new language needs to regenerate stubs whenever the proto definition changes. HTTP/1.1 has no such requirement — any HTTP client handles it.

**Latency**: serialization overhead (JSON vs protobuf) is negligible compared to LLM call latency (500ms–2000ms). There is no measurable performance advantage to gRPC here.

**When gRPC makes sense**: internal service-to-service communication where both endpoints are controlled, latency is sub-millisecond, bidirectional streaming matters, and generated stubs are not a burden.

### Implementation requirements (not yet implemented)

Streaming requires two Go-specific pieces:

**1. `http.Flusher`**

Go's default `http.ResponseWriter` buffers writes internally. To stream, you must cast to `http.Flusher` and call `Flush()` after each chunk:

```go
flusher, ok := w.(http.Flusher)
if !ok {
    // client doesn't support streaming, fall back to buffered
}
// after each chunk write:
flusher.Flush()
```

Without `Flush()`, the buffer accumulates all chunks and sends them at once when the handler returns — defeating the point of streaming entirely.

**2. Cache and metering on `[DONE]`**

Streaming complicates cache writes and token counting. Individual chunks carry partial content; the full response and token usage metadata only appear in the final `[DONE]` chunk. The streaming handler must:
- Accumulate chunk content as it passes through
- Parse the final `[DONE]` chunk for `usage.prompt_tokens` and `usage.completion_tokens`
- Only then trigger `cache.Store` and log via `slog.Info`

This means the goroutine-based fire-and-forget cache store from M4 (Decision 021) still applies — it just triggers at `[DONE]` instead of after a buffered response.
