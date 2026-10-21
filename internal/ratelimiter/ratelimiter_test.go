package ratelimiter_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
	"llm-gateway/internal/ratelimiter"
)

// requireRedis skips the test if REDIS_ADDR is not set or Redis is unreachable.
// Run integration tests with: REDIS_ADDR=localhost:6380 go test ./internal/ratelimiter/
func requireRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR not set — skipping integration test")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skipf("Redis unavailable at %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func uniqueTeam(t *testing.T) string {
	return fmt.Sprintf("test-%s", t.Name())
}

func TestAllowRequest_Allow(t *testing.T) {
	client := requireRedis(t)
	team := uniqueTeam(t)
	t.Cleanup(func() { client.Del(context.Background(), "ratelimit:"+team) })

	ok := ratelimiter.AllowRequest(context.Background(), client, team, 100)
	if !ok {
		t.Fatal("expected allow for first request under limit")
	}
}

func TestAllowRequest_DenyWhenBucketEmpty(t *testing.T) {
	client := requireRedis(t)
	team := uniqueTeam(t)
	t.Cleanup(func() { client.Del(context.Background(), "ratelimit:"+team) })

	limit := 5
	ctx := context.Background()

	// Drain the entire bucket
	for i := 0; i < limit; i++ {
		ratelimiter.AllowRequest(ctx, client, team, limit)
	}

	// Next request should be denied
	ok := ratelimiter.AllowRequest(ctx, client, team, limit)
	if ok {
		t.Fatal("expected deny after bucket is exhausted")
	}
}

func TestAllowRequest_FailOpenOnRedisDown(t *testing.T) {
	// Client pointing to a port with nothing running
	badClient := redis.NewClient(&redis.Options{Addr: "localhost:19999"})
	defer func() { _ = badClient.Close() }()

	// Should fail open (return true) so legitimate traffic isn't blocked
	ok := ratelimiter.AllowRequest(context.Background(), badClient, "team", 100)
	if !ok {
		t.Fatal("expected fail-open (true) when Redis is unreachable")
	}
}

func BenchmarkAllowRequest(b *testing.B) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		b.Skip("REDIS_ADDR not set — skipping benchmark")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	b.Cleanup(func() { client.Del(ctx, "ratelimit:bench") })

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ratelimiter.AllowRequest(ctx, client, "bench", 1000000)
	}
}
