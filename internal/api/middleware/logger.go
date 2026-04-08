package middleware

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Logger returns a gin middleware that logs each request with username, method,
// path, status, latency, client IP, user-agent, and request body (for mutating requests).
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.RequestURI()
		method := c.Request.Method
		userAgent := c.Request.UserAgent()

		// Capture request body for mutating requests (POST/PUT/PATCH)
		var bodySnippet string
		if method == "POST" || method == "PUT" || method == "PATCH" {
			bodyBytes, _ := io.ReadAll(io.LimitReader(c.Request.Body, 512))
			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
			s := strings.TrimSpace(string(bodyBytes))
			if len(s) > 200 {
				s = s[:200] + "..."
			}
			// collapse whitespace
			bodySnippet = strings.Join(strings.Fields(s), " ")
		}

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()
		clientIP := c.ClientIP()

		username, _ := c.Get("username")
		user := "-"
		if u, ok := username.(string); ok && u != "" {
			user = u
		}

		errs := c.Errors.ByType(gin.ErrorTypePrivate).String()

		line := fmt.Sprintf("[GIN] %s | %3d | %12v | %15s | %-7s %s | user=%s | ua=%s",
			time.Now().Format("2006/01/02 - 15:04:05"),
			status,
			latency,
			clientIP,
			method,
			path,
			user,
			userAgent,
		)
		if bodySnippet != "" {
			line += " | body=" + bodySnippet
		}
		if errs != "" {
			line += " | err=" + errs
		}
		fmt.Println(line)
	}
}
