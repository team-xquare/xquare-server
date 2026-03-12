package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/gitops"
)

// ProjectAccess checks that the authenticated user owns the requested project.
// Admins (listed in adminIDs by GitHub user ID) bypass the ownership check.
// On success, the loaded project is stored in context as "project".
func ProjectAccess(g *gitops.Client, adminIDs []int64) gin.HandlerFunc {
	admins := make(map[int64]bool, len(adminIDs))
	for _, id := range adminIDs {
		admins[id] = true
	}

	return func(c *gin.Context) {
		projectName := c.Param("project")
		githubID := c.GetInt64("githubId")

		p, err := g.GetProject(projectName)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "project not found"})
			return
		}

		if !admins[githubID] && !p.HasAccess(githubID) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "access denied"})
			return
		}

		c.Set("project", p)
		c.Next()
	}
}
