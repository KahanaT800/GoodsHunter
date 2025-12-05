package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"time"

	"goodshunter/internal/pkg/metrics"

	"github.com/redis/go-redis/v9"
)

var ErrRateLimitTimeout = errors.New("rate limit wait timeout")

const tokenBucketLua = `
local key = KEYS[1]
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])

if rate <= 0 or burst <= 0 then
  return {1, 0, burst}
end

local data = redis.call("HMGET", key, "tokens", "ts")
local tokens = tonumber(data[1])
local ts = tonumber(data[2])
if tokens == nil then
  tokens = burst
end
if ts == nil then
  ts = now
end

local delta = math.max(0, now - ts)
local refill = (delta * rate) / 1000.0
tokens = math.min(burst, tokens + refill)

local allowed = tokens >= requested
local wait_ms = 0
if allowed then
  tokens = tokens - requested
else
  wait_ms = math.ceil((requested - tokens) * 1000.0 / rate)
end

redis.call("HMSET", key, "tokens", tokens, "ts", now)
redis.call("PEXPIRE", key, math.ceil((burst / rate) * 1000.0 * 2))

return {allowed and 1 or 0, wait_ms, tokens}
`

type RateLimiter struct {
	rdb    *redis.Client
	key    string
	rate   float64
	burst  float64
	logger *slog.Logger
	script *redis.Script
}

func NewRedisRateLimiter(rdb *redis.Client, logger *slog.Logger, key string, rate float64, burst float64) *RateLimiter {
	if key == "" {
		key = "goodshunter:ratelimit:default"
	}
	return &RateLimiter{
		rdb:    rdb,
		key:    key,
		rate:   rate,
		burst:  burst,
		logger: logger,
		script: redis.NewScript(tokenBucketLua),
	}
}

func (r *RateLimiter) Acquire(ctx context.Context) error {
	if r == nil || r.rate <= 0 || r.burst <= 0 {
		return nil
	}

	const jitterMax = 10 * time.Millisecond
	start := time.Now()
	for {
		allowed, waitMs, err := r.tryAcquire(ctx)
		if err != nil {
			return err
		}
		if allowed {
			metrics.RateLimitWaitDuration.Observe(time.Since(start).Seconds())
			return nil
		}

		wait := time.Duration(waitMs) * time.Millisecond
		if wait <= 0 {
			wait = 50 * time.Millisecond
		}
		if jitterMax > 0 {
			wait += time.Duration(rand.Int63n(int64(jitterMax)))
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			metrics.RateLimitWaitDuration.Observe(time.Since(start).Seconds())
			metrics.RateLimitTimeoutTotal.Inc()
			return ErrRateLimitTimeout
		case <-timer.C:
		}
	}
}

func (r *RateLimiter) tryAcquire(ctx context.Context) (bool, int64, error) {
	now := time.Now().UnixMilli()
	res, err := r.script.Run(ctx, r.rdb, []string{r.key}, r.rate, r.burst, now, 1).Result()
	if err != nil {
		return false, 0, fmt.Errorf("ratelimit eval: %w", err)
	}

	values, ok := res.([]interface{})
	if !ok || len(values) < 2 {
		return false, 0, fmt.Errorf("ratelimit invalid result")
	}

	allowed := toInt64(values[0]) == 1
	waitMs := toInt64(values[1])
	return allowed, waitMs, nil
}

func toInt64(v interface{}) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case string:
		if t == "" {
			return 0
		}
		if parsed, err := strconv.ParseInt(t, 10, 64); err == nil {
			return parsed
		}
	}
	return 0
}
