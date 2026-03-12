package handler

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/domain"
	"github.com/team-xquare/xquare-server/internal/github"
	"github.com/team-xquare/xquare-server/internal/gitops"
	"github.com/team-xquare/xquare-server/internal/vault"
)

var projectNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`)

type ProjectHandler struct {
	gitops     *gitops.Client
	vault      *vault.Client
	github     *github.Client
	adminUsers map[string]bool
}

func NewProjectHandler(g *gitops.Client, v *vault.Client, gh *github.Client, admins []string) *ProjectHandler {
	m := make(map[string]bool, len(admins))
	for _, u := range admins {
		if u != "" {
			m[u] = true
		}
	}
	return &ProjectHandler{gitops: g, vault: v, github: gh, adminUsers: m}
}

func (h *ProjectHandler) isAdmin(username string) bool {
	return h.adminUsers[username]
}

// GET /projects — only shows projects the user owns (admins see all)
func (h *ProjectHandler) List(c *gin.Context) {
	githubID := c.GetInt64("githubId")
	username := c.GetString("username")
	all, err := h.gitops.ListProjects()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if h.isAdmin(username) {
		c.JSON(http.StatusOK, gin.H{"projects": all})
		return
	}
	var accessible []string
	for _, name := range all {
		p, err := h.gitops.GetProject(name)
		if err != nil {
			continue
		}
		if p.HasAccess(githubID) {
			accessible = append(accessible, name)
		}
	}
	if accessible == nil {
		accessible = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"projects": accessible})
}

// GET /projects/:project
func (h *ProjectHandler) Get(c *gin.Context) {
	p, _ := c.Get("project")
	c.JSON(http.StatusOK, p)
}

// POST /projects
func (h *ProjectHandler) Create(c *gin.Context) {
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if !projectNameRe.MatchString(req.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project name: must be lowercase alphanumeric and hyphens (2-63 chars)"})
		return
	}

	owner := domain.Owner{
		ID:       c.GetInt64("githubId"),
		Username: c.GetString("username"),
	}
	if err := h.gitops.CreateProject(req.Name, owner); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"name": req.Name})
}

// DELETE /projects/:project
// Also cleans up Vault secrets for all apps in the project
func (h *ProjectHandler) Delete(c *gin.Context) {
	project := c.Param("project")

	p, _ := c.Get("project")
	proj := p.(*domain.Project)

	if err := h.gitops.DeleteProject(project); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Clean up Vault secrets for all apps
	for _, app := range proj.Applications {
		_ = h.vault.DeleteEnv(project, app.Name)
	}

	c.Status(http.StatusNoContent)
}

// GET /projects/:project/members
func (h *ProjectHandler) ListMembers(c *gin.Context) {
	p, _ := c.Get("project")
	proj := p.(*domain.Project)
	c.JSON(http.StatusOK, gin.H{"owners": proj.Owners})
}

// POST /projects/:project/members  {"username": "github-login"}
func (h *ProjectHandler) AddMember(c *gin.Context) {
	project := c.Param("project")

	var req struct {
		Username string `json:"username" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Resolve username → GitHub ID (immutable)
	user, err := h.github.GetUserByUsername(c.Request.Context(), req.Username)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	owner := domain.Owner{ID: user.ID, Username: user.Login}
	if err := h.gitops.AddProjectMember(project, owner); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"added": owner})
}

// DELETE /projects/:project/members/:username
func (h *ProjectHandler) RemoveMember(c *gin.Context) {
	project := c.Param("project")
	targetUsername := c.Param("username")

	// Resolve username → GitHub ID
	user, err := h.github.GetUserByUsername(c.Request.Context(), targetUsername)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.gitops.RemoveProjectMember(project, user.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
