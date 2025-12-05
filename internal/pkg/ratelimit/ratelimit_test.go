package ratelimit

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRateLimiter_BasicAcquireReducesTokens(t *testing.T) {
	rdb := newMiniRedis(t)
	defer closeRedis(t, rdb)

	limiter := NewRedisRateLimiter(rdb, nil, "test:ratelimit:basic", 10, 2)
	if err := limiter.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	tokensStr, err := rdb.HGet(context.Background(), limiter.key, "tokens").Result()
	if err != nil {
		t.Fatalf("hget tokens: %v", err)
	}
	tokens, err := strconv.ParseFloat(tokensStr, 64)
	if err != nil {
		t.Fatalf("parse tokens: %v", err)
	}
	if tokens > 1.1 {
		t.Fatalf("expected tokens to decrease, got %.2f", tokens)
	}
}

func TestRateLimiter_AcquireBlocksUntilToken(t *testing.T) {
	rdb := newMiniRedis(t)
	defer closeRedis(t, rdb)

	limiter := NewRedisRateLimiter(rdb, nil, "test:ratelimit:block", 10, 1)
	if err := limiter.Acquire(context.Background()); err != nil {
		t.Fatalf("warm acquire: %v", err)
	}

	start := time.Now()
	if err := limiter.Acquire(context.Background()); err != nil {
		t.Fatalf("blocked acquire: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 90*time.Millisecond {
		t.Fatalf("expected blocking, elapsed=%v", elapsed)
	}
}

func TestRateLimiter_ContextTimeout(t *testing.T) {
	rdb := newMiniRedis(t)
	defer closeRedis(t, rdb)

	limiter := NewRedisRateLimiter(rdb, nil, "test:ratelimit:timeout", 1, 1)
	if err := limiter.Acquire(context.Background()); err != nil {
		t.Fatalf("warm acquire: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := limiter.Acquire(ctx)
	if !errors.Is(err, ErrRateLimitTimeout) {
		t.Fatalf("expected ErrRateLimitTimeout, got %v", err)
	}
}

func TestRateLimiter_ConcurrentAcquire(t *testing.T) {
	rdb := newMiniRedis(t)
	defer closeRedis(t, rdb)

	limiter := NewRedisRateLimiter(rdb, nil, "test:ratelimit:concurrent", 5, 5)

	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	timeout := 0

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := limiter.Acquire(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
				return
			}
			if errors.Is(err, ErrRateLimitTimeout) {
				timeout++
			}
		}()
	}

	wg.Wait()

	if success != 5 {
		t.Fatalf("expected 5 immediate successes, got %d (timeout=%d)", success, timeout)
	}
}

func newMiniRedis(t *testing.T) *redis.Client {
	t.Helper()
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(s.Close)
	return redis.NewClient(&redis.Options{Addr: s.Addr()})
}

func closeRedis(t *testing.T, rdb *redis.Client) {
	t.Helper()
	if err := rdb.Close(); err != nil {
		t.Fatalf("close redis: %v", err)
	}
}
