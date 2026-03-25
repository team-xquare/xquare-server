package handler

import (
	"net/http"
	"strings"

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
	ids, err := h.gitops.ListAllowedUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": resolveUsernames(c, h.github, ids)})
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
	if err := h.gitops.AddAllowedUser(c.GetString("username"), ghUser.ID); err != nil {
		if strings.Contains(err.Error(), "already") {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": ghUser.ID, "username": ghUser.Login})
}

type adminUser struct {
	ID          int64    `json:"id"`
	Username    string   `json:"username"`
	InAllowlist bool     `json:"inAllowlist"`
	Projects    []string `json:"projects"`
}

// GET /admin/users
func (h *AllowlistHandler) ListUsers(c *gin.Context) {
	if !h.isAdmin(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
		return
	}
	allowedIDs, err := h.gitops.ListAllowedUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	projectOwners, err := h.gitops.ListAllProjectOwners()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Build union of all user IDs
	allowedSet := make(map[int64]struct{}, len(allowedIDs))
	for _, id := range allowedIDs {
		allowedSet[id] = struct{}{}
	}
	userProjects := make(map[int64][]string)
	for project, owners := range projectOwners {
		for _, id := range owners {
			userProjects[id] = append(userProjects[id], project)
		}
	}
	allIDs := make(map[int64]struct{}, len(allowedIDs))
	for _, id := range allowedIDs {
		allIDs[id] = struct{}{}
	}
	for id := range userProjects {
		allIDs[id] = struct{}{}
	}

	ids := make([]int64, 0, len(allIDs))
	for id := range allIDs {
		ids = append(ids, id)
	}
	resolved := resolveUsernames(c, h.github, ids)

	users := make([]adminUser, 0, len(resolved))
	for _, r := range resolved {
		_, inAllowlist := allowedSet[r.ID]
		projects := userProjects[r.ID]
		if projects == nil {
			projects = []string{}
		}
		users = append(users, adminUser{
			ID:          r.ID,
			Username:    r.Username,
			InAllowlist: inAllowlist,
			Projects:    projects,
		})
	}
	c.JSON(http.StatusOK, gin.H{"users": users})
}

// GET /admin/users/:username
func (h *AllowlistHandler) GetUser(c *gin.Context) {
	if !h.isAdmin(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
		return
	}
	username := c.Param("username")
	ghUser, err := h.github.GetUserByUsername(c.Request.Context(), username)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "GitHub user not found"})
		return
	}
	allowedIDs, err := h.gitops.ListAllowedUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	projectOwners, err := h.gitops.ListAllProjectOwners()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	inAllowlist := false
	for _, id := range allowedIDs {
		if id == ghUser.ID {
			inAllowlist = true
			break
		}
	}
	var projects []string
	for project, owners := range projectOwners {
		for _, id := range owners {
			if id == ghUser.ID {
				projects = append(projects, project)
				break
			}
		}
	}
	if projects == nil {
		projects = []string{}
	}
	c.JSON(http.StatusOK, adminUser{
		ID:          ghUser.ID,
		Username:    ghUser.Login,
		InAllowlist: inAllowlist,
		Projects:    projects,
	})
}

// DELETE /admin/allowlist/:username
// Resolves username → GitHub ID to avoid removing the wrong user if an account is renamed.
func (h *AllowlistHandler) Remove(c *gin.Context) {
	if !h.isAdmin(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
		return
	}
	username := c.Param("username")
	ghUser, err := h.github.GetUserByUsername(c.Request.Context(), username)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not resolve GitHub username"})
		return
	}
	if err := h.gitops.RemoveAllowedUser(c.GetString("username"), ghUser.ID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.Status(http.StatusNoContent)
}
