package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/domain"
)

// AppAccess verifies that the :app path parameter belongs to the project stored
// in context by ProjectAccess. Returns 404 if the app is not in the project's
// application list, preventing cross-project Vault/K8s access.
func AppAccess() gin.HandlerFunc {
	return func(c *gin.Context) {
		appName := c.Param("app")
		proj, exists := c.Get("project")
		if !exists {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "project context missing"})
			return
		}
		p, ok := proj.(*domain.Project)
		if !ok || p == nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "project context invalid"})
			return
		}
		for _, a := range p.Applications {
			if a.Name == appName {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "app not found"})
	}
}
