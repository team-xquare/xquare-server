package handler

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/domain"
	"github.com/team-xquare/xquare-server/internal/github"
	"github.com/team-xquare/xquare-server/internal/gitops"
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
	gitops  *gitops.Client
	k8s     *k8s.Client
	vault   *vault.Client
	wf      *k8s.WorkflowClient
	github  *github.Client
}

func NewAppHandler(g *gitops.Client, k *k8s.Client, v *vault.Client, wf *k8s.WorkflowClient, gh *github.Client) *AppHandler {
	return &AppHandler{gitops: g, k8s: k, vault: v, wf: wf, github: gh}
}

// GET /projects/:project/apps
func (h *AppHandler) List(c *gin.Context) {
	p, _ := c.Get("project")
	c.JSON(http.StatusOK, gin.H{"applications": p.(*domain.Project).Applications})
}

// GET /projects/:project/apps/:app
func (h *AppHandler) Get(c *gin.Context) {
	app := c.Param("app")
	p, _ := c.Get("project")
	for _, a := range p.(*domain.Project).Applications {
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

	// Auto-resolve GitHub App installation ID
	installID, err := h.github.GetRepoInstallationID(c.Request.Context(), app.GitHub.Owner, app.GitHub.Repo)
	if err != nil {
		var notInstalled *github.ErrAppNotInstalled
		if errors.As(err, &notInstalled) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":       fmt.Sprintf("GitHub App not installed on %s/%s", app.GitHub.Owner, app.GitHub.Repo),
				"install_url": notInstalled.InstallURL,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	app.GitHub.InstallationID = installID

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
// Triggers CI by creating a new Argo Workflow for the app.
func (h *AppHandler) Redeploy(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

	if h.wf == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CI trigger unavailable"})
		return
	}

	// Resolve current SHA from GitHub so the workflow template takes the fast path
	// (github-event-type=push) and skips the sh process-substitution JWT code.
	p, _ := c.Get("project")
	sha := ""
	for _, a := range p.(*domain.Project).Applications {
		if a.Name == app {
			sha, _ = h.github.GetBranchSHA(c.Request.Context(), a.GitHub.Owner, a.GitHub.Repo, a.GitHub.Branch)
			break
		}
	}

	name, err := h.wf.TriggerCI(c.Request.Context(), project, app, sha)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"build": name})
}
