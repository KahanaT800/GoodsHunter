package dedup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const keyPrefix = "goodshunter:dedup:url:"

type Deduplicator struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewDeduplicator(rdb *redis.Client, ttl time.Duration) *Deduplicator {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &Deduplicator{
		rdb: rdb,
		ttl: ttl,
	}
}

func (d *Deduplicator) IsDuplicate(ctx context.Context, url string) (bool, error) {
	if d == nil || d.rdb == nil || url == "" {
		return false, nil
	}
	key := keyPrefix + hashURL(url)
	ok, err := d.rdb.SetNX(ctx, key, "1", d.ttl).Result()
	if err != nil {
		return false, fmt.Errorf("dedup setnx: %w", err)
	}
	return !ok, nil
}

func (d *Deduplicator) Delete(ctx context.Context, url string) error {
	if d == nil || d.rdb == nil || url == "" {
		return nil
	}
	key := keyPrefix + hashURL(url)
	if err := d.rdb.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("dedup del: %w", err)
	}
	return nil
}

func hashURL(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}
