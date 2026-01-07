package middleware

import (
	"time"

	"log/slog"

	"github.com/gin-gonic/gin"
)

// RequestLogger logs HTTP request/response metadata.
func RequestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		status := c.Writer.Status()
		latency := time.Since(start)
		path := c.Request.URL.Path
		method := c.Request.Method
		clientIP := c.ClientIP()

		if logger != nil {
			logger.Info("http request",
				slog.String("method", method),
				slog.String("path", path),
				slog.Int("status", status),
				slog.String("client_ip", clientIP),
				slog.String("latency", latency.String()),
			)
		}
	}
}
