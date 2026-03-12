package middleware

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// bucket tracks request timestamps for one rate-limit key.
type bucket struct {
	mu        sync.Mutex
	times     []time.Time
	windowLen time.Duration
	maxReqs   int
}

func newBucket(maxReqs int, window time.Duration) *bucket {
	return &bucket{windowLen: window, maxReqs: maxReqs}
}

func (b *bucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-b.windowLen)
	j := 0
	for _, t := range b.times {
		if t.After(cutoff) {
			b.times[j] = t
			j++
		}
	}
	b.times = b.times[:j]
	if len(b.times) >= b.maxReqs {
		return false
	}
	b.times = append(b.times, now)
	return true
}

// rateLimiter maps arbitrary string keys to buckets.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	maxReqs int
	window  time.Duration
}

func newRateLimiter(maxReqs int, window time.Duration) *rateLimiter {
	rl := &rateLimiter{
		buckets: make(map[string]*bucket),
		maxReqs: maxReqs,
		window:  window,
	}
	go func() {
		for range time.Tick(5 * time.Minute) {
			rl.mu.Lock()
			cutoff := time.Now().Add(-window)
			for k, b := range rl.buckets {
				b.mu.Lock()
				allOld := true
				for _, t := range b.times {
					if t.After(cutoff) {
						allOld = false
						break
					}
				}
				b.mu.Unlock()
				if allOld {
					delete(rl.buckets, k)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *rateLimiter) getBucket(key string) *bucket {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[key]
	if !ok {
		b = newBucket(rl.maxReqs, rl.window)
		rl.buckets[key] = b
	}
	return b
}

// RateLimitByIP limits requests by client IP.
// Use for pre-authentication endpoints where no user identity is available.
func RateLimitByIP(maxReqs int, window time.Duration) gin.HandlerFunc {
	rl := newRateLimiter(maxReqs, window)
	return func(c *gin.Context) {
		if !rl.getBucket(c.ClientIP()).allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded — try again later"})
			return
		}
		c.Next()
	}
}

// RateLimitByAccount limits requests by authenticated GitHub account ID.
// Must be placed after the Auth middleware so "githubId" is set in context.
func RateLimitByAccount(maxReqs int, window time.Duration) gin.HandlerFunc {
	rl := newRateLimiter(maxReqs, window)
	return func(c *gin.Context) {
		githubID := c.GetInt64("githubId")
		key := fmt.Sprintf("account:%d", githubID)
		if !rl.getBucket(key).allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded — try again later"})
			return
		}
		c.Next()
	}
}
