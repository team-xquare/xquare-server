package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type allowlistProvider interface {
	AllowedUserIDs() (map[int64]struct{}, error)
}

// Allowlist checks the requesting user's GitHub ID against allowed-users.yaml.
// If the file does not exist, all authenticated users are allowed.
func Allowlist(gitops allowlistProvider) gin.HandlerFunc {
	return func(c *gin.Context) {
		allowed, err := gitops.AllowedUserIDs()
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "failed to load allowlist"})
			return
		}
		if allowed == nil {
			c.Next()
			return
		}
		githubID, _ := c.Get("githubId")
		id, ok := githubID.(int64)
		if !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "access denied"})
			return
		}
		if _, permitted := allowed[id]; !permitted {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "access denied"})
			return
		}
		c.Next()
	}
}
