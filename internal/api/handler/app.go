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

// friendlyK8sError translates internal K8s/Argo error messages into user-facing ones.
func friendlyK8sError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "workflowtemplates") && strings.Contains(msg, "not found"):
		return "CI pipeline not ready yet — ArgoCD is deploying the build infrastructure. Try again in a moment."
	case strings.Contains(msg, "workflows.argoproj.io") && strings.Contains(msg, "forbidden"):
		return "Build trigger is not authorized. Please contact an administrator."
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "no route to host"):
		return "Cannot connect to the cluster. Please try again."
	default:
		return msg
	}
}

type AppHandler struct {
	gitops *gitops.Client
	k8s    *k8s.Client
	vault  *vault.Client
	wf     *k8s.WorkflowClient
	github *github.Client
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
// Returns deployment status enriched with CI readiness and latest build info.
func (h *AppHandler) Status(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

	status, err := h.k8s.GetAppStatus(c.Request.Context(), project, app)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": friendlyK8sError(err)})
		return
	}

	// Enrich: CI pipeline readiness
	ciReady := false
	if h.wf != nil {
		ciReady = h.wf.WorkflowTemplateExists(c.Request.Context(), project, app)
	}

	// Enrich: latest build status so clients can show deployPhase
	var lastBuild *k8s.WorkflowInfo
	if h.wf != nil {
		if wfs, err := h.wf.ListWorkflows(c.Request.Context(), project, app); err == nil && len(wfs) > 0 {
			lastBuild = &wfs[0]
		}
	}

	// Compute deployPhase for UI clarity
	deployPhase := "not_deployed"
	if status.Status == "running" || status.Status == "failed" || status.Status == "pending" || status.Status == "stopped" {
		deployPhase = "deployed"
	} else if lastBuild != nil {
		switch lastBuild.Status {
		case "running", "pending":
			deployPhase = "building"
		case "success":
			deployPhase = "syncing" // build done, waiting for ArgoCD
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"name":        status.Name,
		"status":      status.Status,
		"deployPhase": deployPhase,
		"ciReady":     ciReady,
		"scale":       status.Scale,
		"version":     status.Version,
		"instances":   status.Instances,
		"lastBuild":   lastBuild,
	})
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

	if err := h.gitops.AddApplication(project, app, c.GetString("username")); err != nil {
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
// Preserves server-managed fields (installationId, hash) that are not in the request body.
func (h *AppHandler) Update(c *gin.Context) {
	project := c.Param("project")
	appName := c.Param("app")

	var updated domain.Application
	if err := c.ShouldBindJSON(&updated); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated.Name = appName // enforce from path

	// Preserve server-managed fields from existing app
	p, _ := c.Get("project")
	for _, existing := range p.(*domain.Project).Applications {
		if existing.Name == appName {
			if updated.GitHub.InstallationID == "" {
				updated.GitHub.InstallationID = existing.GitHub.InstallationID
			}
			if updated.GitHub.Hash == "" {
				updated.GitHub.Hash = existing.GitHub.Hash
			}
			break
		}
	}

	if err := h.gitops.UpdateApplication(project, updated, c.GetString("username")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, updated)
}

// DELETE /projects/:project/apps/:app
func (h *AppHandler) Delete(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

	if err := h.gitops.DeleteApplication(project, app, c.GetString("username")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Clean up Vault secrets
	_ = h.vault.DeleteEnv(project, app)

	c.Status(http.StatusNoContent)
}

// GET /projects/:project/apps/:app/tunnel
// Returns tunnel connection info (access server host/password, app endpoint ports)
func (h *AppHandler) Tunnel(c *gin.Context) {
	project := c.Param("project")
	appName := c.Param("app")

	password, err := h.k8s.GetAccessServerPassword(c.Request.Context(), project)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not get access credentials"})
		return
	}

	p, _ := c.Get("project")
	var ports []int
	found := false
	for _, a := range p.(*domain.Project).Applications {
		if a.Name == appName {
			found = true
			for _, ep := range a.Endpoints {
				ports = append(ports, ep.Port)
			}
			break
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "app not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"host":     "xquare-remote-access-" + project + ".dsmhs.kr",
		"password": password,
		"ports":    ports,
	})
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
		var ciNotReady *k8s.ErrCINotReady
		if errors.As(err, &ciNotReady) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":   ciNotReady.Error(),
				"code":    "ci_not_ready",
				"retryIn": 30,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": friendlyK8sError(err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"build": name})
}
