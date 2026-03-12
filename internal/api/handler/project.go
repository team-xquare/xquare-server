package handler

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/gitops"
	"github.com/team-xquare/xquare-server/internal/vault"
)

var projectNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`)

type ProjectHandler struct {
	gitops *gitops.Client
	vault  *vault.Client
}

func NewProjectHandler(g *gitops.Client, v *vault.Client) *ProjectHandler {
	return &ProjectHandler{gitops: g, vault: v}
}

// GET /projects
func (h *ProjectHandler) List(c *gin.Context) {
	projects, err := h.gitops.ListProjects()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"projects": projects})
}

// GET /projects/:project
func (h *ProjectHandler) Get(c *gin.Context) {
	project := c.Param("project")
	p, err := h.gitops.GetProject(project)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
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

	if err := h.gitops.CreateProject(req.Name); err != nil {
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

	// Read project first to get app list for Vault cleanup
	p, err := h.gitops.GetProject(project)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	if err := h.gitops.DeleteProject(project); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Clean up Vault secrets for all apps
	for _, app := range p.Applications {
		_ = h.vault.DeleteEnv(project, app.Name)
	}

	c.Status(http.StatusNoContent)
}
