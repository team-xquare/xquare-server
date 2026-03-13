package handler

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/domain"
	"github.com/team-xquare/xquare-server/internal/github"
)

// resolvedUser is a user with ID and resolved username for API responses.
type resolvedUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// resolveUsernames resolves a slice of IDs to {id, username} in parallel via GitHub API.
// IDs that fail to resolve get an empty username string instead of failing the whole request.
func resolveUsernames(c *gin.Context, gh *github.Client, ids []int64) []resolvedUser {
	out := make([]resolvedUser, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id int64) {
			defer wg.Done()
			out[i] = resolvedUser{ID: id}
			if user, err := gh.GetUserByID(c.Request.Context(), id); err == nil {
				out[i].Username = user.Login
			}
		}(i, id)
	}
	wg.Wait()
	return out
}

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
