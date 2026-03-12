package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/gitops"
)

// ProjectAccess checks that the authenticated user owns the requested project.
// Admins (listed in adminUsers) bypass the check.
// On success, the loaded project is stored in context as "project".
func ProjectAccess(g *gitops.Client, adminUsers []string) gin.HandlerFunc {
	admins := make(map[string]bool, len(adminUsers))
	for _, u := range adminUsers {
		if u != "" {
			admins[u] = true
		}
	}

	return func(c *gin.Context) {
		projectName := c.Param("project")
		username := c.GetString("username")

		p, err := g.GetProject(projectName)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "project not found"})
			return
		}

		if !admins[username] && !p.HasAccess(username) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "access denied"})
			return
		}

		c.Set("project", p)
		c.Next()
	}
}
