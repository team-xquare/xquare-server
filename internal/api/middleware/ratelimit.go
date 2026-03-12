package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ipBucket tracks request timestamps for one IP address.
type ipBucket struct {
	mu        sync.Mutex
	times     []time.Time
	windowLen time.Duration
	maxReqs   int
}

func newIPBucket(maxReqs int, window time.Duration) *ipBucket {
	return &ipBucket{windowLen: window, maxReqs: maxReqs}
}

// allow returns true if the request is within the rate limit.
func (b *ipBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-b.windowLen)
	// Evict old entries
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

// rateLimiter maps IPs to their buckets.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*ipBucket
	maxReqs int
	window  time.Duration
}

func newRateLimiter(maxReqs int, window time.Duration) *rateLimiter {
	rl := &rateLimiter{
		buckets: make(map[string]*ipBucket),
		maxReqs: maxReqs,
		window:  window,
	}
	// Periodic cleanup to prevent unbounded map growth
	go func() {
		for range time.Tick(5 * time.Minute) {
			rl.mu.Lock()
			cutoff := time.Now().Add(-window)
			for ip, b := range rl.buckets {
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
					delete(rl.buckets, ip)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *rateLimiter) getBucket(ip string) *ipBucket {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[ip]
	if !ok {
		b = newIPBucket(rl.maxReqs, rl.window)
		rl.buckets[ip] = b
	}
	return b
}

// RateLimit returns a middleware that allows at most maxReqs per IP per window.
// Exceeding the limit returns 429 Too Many Requests.
func RateLimit(maxReqs int, window time.Duration) gin.HandlerFunc {
	rl := newRateLimiter(maxReqs, window)
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if !rl.getBucket(ip).allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded — try again later"})
			return
		}
		c.Next()
	}
}
