package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/domain"
)

// projectFromCtx safely retrieves the *domain.Project stored by ProjectAccess middleware.
// Returns false and writes a 500 if the value is missing or the wrong type (should never
// happen in a correctly wired router, but prevents a nil-pointer panic if it does).
func projectFromCtx(c *gin.Context) (*domain.Project, bool) {
	v, exists := c.Get("project")
	if !exists {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "project context missing"})
		return nil, false
	}
	p, ok := v.(*domain.Project)
	if !ok || p == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "project context invalid"})
		return nil, false
	}
	return p, true
}
