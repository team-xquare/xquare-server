package handler

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/domain"
	"github.com/team-xquare/xquare-server/internal/gitops"
	"github.com/team-xquare/xquare-server/internal/github"
	"github.com/team-xquare/xquare-server/internal/k8s"
	"github.com/team-xquare/xquare-server/internal/vault"
)

var resourceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`)

func validateName(name string) error {
	if !resourceNameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: must be lowercase alphanumeric and hyphens (2-63 chars)", name)
	}
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "%") {
		return fmt.Errorf("invalid name %q: path separators not allowed", name)
	}
	return nil
}

type AppHandler struct {
	gitops *gitops.Client
	k8s    *k8s.Client
	vault  *vault.Client
	gh     *github.Client
}

func NewAppHandler(g *gitops.Client, k *k8s.Client, v *vault.Client, gh *github.Client) *AppHandler {
	return &AppHandler{gitops: g, k8s: k, vault: v, gh: gh}
}

// GET /projects/:project/apps
func (h *AppHandler) List(c *gin.Context) {
	project := c.Param("project")
	p, err := h.gitops.GetProject(project)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"applications": p.Applications})
}

// GET /projects/:project/apps/:app
func (h *AppHandler) Get(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

	p, err := h.gitops.GetProject(project)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	for _, a := range p.Applications {
		if a.Name == app {
			c.JSON(http.StatusOK, a)
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "app not found"})
}

// GET /projects/:project/apps/:app/status
func (h *AppHandler) Status(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

	status, err := h.k8s.GetAppStatus(c.Request.Context(), project, app)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, status)
}

// POST /projects/:project/apps
func (h *AppHandler) Create(c *gin.Context) {
	project := c.Param("project")

	var app domain.Application
	if err := c.ShouldBindJSON(&app); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := validateName(app.Name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check domain conflicts
	var domains []string
	for _, ep := range app.Endpoints {
		domains = append(domains, ep.Routes...)
	}
	if len(domains) > 0 {
		if err := h.gitops.CheckDomainConflict(project, app.Name, domains); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
	}

	if err := h.gitops.AddApplication(project, app); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Initialize empty Vault entry so VSO can sync it
	_ = h.vault.InitEnv(project, app.Name)

	c.JSON(http.StatusCreated, app)
}

// PUT /projects/:project/apps/:app
func (h *AppHandler) Update(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

	var updated domain.Application
	if err := c.ShouldBindJSON(&updated); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated.Name = app // enforce from path

	if err := h.gitops.UpdateApplication(project, updated); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, updated)
}

// DELETE /projects/:project/apps/:app
func (h *AppHandler) Delete(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

	if err := h.gitops.DeleteApplication(project, app); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Clean up Vault secrets
	_ = h.vault.DeleteEnv(project, app)

	c.Status(http.StatusNoContent)
}

// POST /projects/:project/apps/:app/redeploy
// Triggers a re-deploy by fetching latest commit SHA and writing it to gitops
func (h *AppHandler) Redeploy(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

	// GitHub access token passed in X-GitHub-Token header
	token := c.GetHeader("X-GitHub-Token")

	p, err := h.gitops.GetProject(project)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	var target *domain.Application
	for i := range p.Applications {
		if p.Applications[i].Name == app {
			target = &p.Applications[i]
			break
		}
	}
	if target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "app not found"})
		return
	}

	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "X-GitHub-Token header required for redeploy"})
		return
	}

	sha, err := h.gh.GetLatestCommitSHA(c.Request.Context(), token,
		target.GitHub.Owner, target.GitHub.Repo, target.GitHub.Branch)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get latest commit: " + err.Error()})
		return
	}

	updated := *target
	updated.GitHub.Hash = sha

	if err := h.gitops.UpdateApplication(project, updated); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"sha": sha})
}
