package ratelimiter

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// luaScript runs atomically in Redis — no other command executes between these steps.
// KEYS[1] = Redis key for this team (e.g. "ratelimit:teamA")
// ARGV[1] = limit (rpm_limit as integer)
// ARGV[2] = now (current Unix timestamp as float)
// ARGV[3] = refill_rate (tokens per second = limit / 60)
// Returns: 1 = allow, 0 = deny
var luaScript = redis.NewScript(`
		local data = redis.call('HMGET', KEYS[1], 'tokens', 'last_refill')

		local limit       = tonumber(ARGV[1])
		local now         = tonumber(ARGV[2])
		local refill_rate = tonumber(ARGV[3])

		-- If key doesn't exist yet, start with a full bucket
		local tokens      = tonumber(data[1]) or limit
		local last_refill = tonumber(data[2]) or now

		-- Calculate how many tokens to add based on elapsed time
		local elapsed     = now - last_refill
		local new_tokens  = math.min(limit, tokens + elapsed * refill_rate)

		if new_tokens >= 1 then
			redis.call('HMSET', KEYS[1], 'tokens', new_tokens - 1, 'last_refill', now)
			redis.call('EXPIRE', KEYS[1], 120)
			return 1
		else
			redis.call('HMSET', KEYS[1], 'tokens', new_tokens, 'last_refill', now)
			redis.call('EXPIRE', KEYS[1], 120)
			return 0
		end
	`)

func AllowRequest(ctx context.Context, client *redis.Client, team string, limit int) bool {
	key := fmt.Sprintf("ratelimit:%s", team)
	now := float64(time.Now().UnixNano()) / 1e9 // Unix time as fractional seconds
	rate := float64(limit) / 60.0               // tokens per second

	result, err := luaScript.Run(ctx, client, []string{key}, limit, now, rate).Int()
	if err != nil {
		// Fail open — if Redis is down, don't block legitimate requests
		return true
	}
	return result == 1
}
