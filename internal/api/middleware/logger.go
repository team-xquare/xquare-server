package middleware

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
)

// Logger returns a gin middleware that logs each request with username, method,
// path, status, latency, and client IP.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.RequestURI()

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()
		clientIP := c.ClientIP()
		method := c.Request.Method

		username, _ := c.Get("username")
		user := "-"
		if u, ok := username.(string); ok && u != "" {
			user = u
		}

		fmt.Printf("[GIN] %s | %3d | %12v | %15s | %-7s %s | user=%s\n",
			time.Now().Format("2006/01/02 - 15:04:05"),
			status,
			latency,
			clientIP,
			method,
			path,
			user,
		)
	}
}
