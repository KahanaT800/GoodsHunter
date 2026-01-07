package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const guestActiveKeyPrefix = "guest:active:"

// GuestActivityMiddleware marks guest users as active so the scheduler can stop tasks on idle.
func GuestActivityMiddleware(rdb *redis.Client, ttl time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		roleVal, ok := c.Get("role")
		if !ok || roleVal != "guest" {
			c.Next()
			return
		}
		userIDVal, ok := c.Get("userID")
		if !ok {
			c.Next()
			return
		}
		userID, ok := userIDVal.(int)
		if !ok {
			c.Next()
			return
		}

		if ttl <= 0 {
			ttl = 10 * time.Minute
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		key := fmt.Sprintf("%s%d", guestActiveKeyPrefix, userID)
		_ = rdb.Set(ctx, key, "1", ttl).Err()

		c.Next()
	}
}
