package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/team-xquare/xquare-server/internal/github"
	"github.com/team-xquare/xquare-server/internal/gitops"
)

type AllowlistHandler struct {
	gitops   *gitops.Client
	github   *github.Client
	adminIDs map[int64]bool
}

func NewAllowlistHandler(g *gitops.Client, gh *github.Client, adminIDs []int64) *AllowlistHandler {
	m := make(map[int64]bool, len(adminIDs))
	for _, id := range adminIDs {
		m[id] = true
	}
	return &AllowlistHandler{gitops: g, github: gh, adminIDs: m}
}

func (h *AllowlistHandler) isAdmin(c *gin.Context) bool {
	id, _ := c.Get("githubId")
	githubID, ok := id.(int64)
	return ok && h.adminIDs[githubID]
}

// GET /admin/allowlist
func (h *AllowlistHandler) List(c *gin.Context) {
	if !h.isAdmin(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
		return
	}
	users, err := h.gitops.ListAllowedUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": users})
}

// POST /admin/allowlist
// body: {"username": "alice"}
func (h *AllowlistHandler) Add(c *gin.Context) {
	if !h.isAdmin(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
		return
	}
	var req struct {
		Username string `json:"username" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ghUser, err := h.github.GetUserByUsername(c.Request.Context(), req.Username)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	actor, _ := c.Get("username")
	if err := h.gitops.AddAllowedUser(actor.(string), gitops.AllowedUser{
		ID:       ghUser.ID,
		Username: ghUser.Login,
	}); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": ghUser.ID, "username": ghUser.Login})
}

// DELETE /admin/allowlist/:username
func (h *AllowlistHandler) Remove(c *gin.Context) {
	if !h.isAdmin(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
		return
	}
	username := c.Param("username")
	actor, _ := c.Get("username")
	if err := h.gitops.RemoveAllowedUser(actor.(string), username); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
