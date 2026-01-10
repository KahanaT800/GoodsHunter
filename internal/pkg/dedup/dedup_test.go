package dedup

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestDeduplicator_IsDuplicate(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() {
		if err := rdb.Close(); err != nil {
			t.Fatalf("close redis: %v", err)
		}
	})

	d := NewDeduplicator(rdb, time.Minute)
	ctx := context.Background()

	dup, err := d.IsDuplicate(ctx, "https://example.com/search?q=abc")
	if err != nil {
		t.Fatalf("first dedup: %v", err)
	}
	if dup {
		t.Fatalf("expected first to be non-duplicate")
	}

	dup, err = d.IsDuplicate(ctx, "https://example.com/search?q=abc")
	if err != nil {
		t.Fatalf("second dedup: %v", err)
	}
	if !dup {
		t.Fatalf("expected second to be duplicate")
	}
}
