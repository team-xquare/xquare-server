package handler

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/domain"
	"github.com/team-xquare/xquare-server/internal/github"
	"github.com/team-xquare/xquare-server/internal/gitops"
	"github.com/team-xquare/xquare-server/internal/k8s"
	"github.com/team-xquare/xquare-server/internal/vault"
)

// redeployLimiter enforces a per-app cooldown on CI triggers to prevent Workflow flooding.
type redeployLimiter struct {
	mu       sync.Mutex
	lastRun  map[string]time.Time
	cooldown time.Duration
}

func newRedeployLimiter() *redeployLimiter {
	return &redeployLimiter{
		lastRun:  make(map[string]time.Time),
		cooldown: 30 * time.Second,
	}
}

func (r *redeployLimiter) allow(key string) (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.lastRun[key]; ok {
		if wait := r.cooldown - time.Since(t); wait > 0 {
			return false, wait
		}
	}
	r.lastRun[key] = time.Now()
	// Evict entries that are well past the cooldown window to bound map size.
	for k, t := range r.lastRun {
		if time.Since(t) > 2*r.cooldown {
			delete(r.lastRun, k)
		}
	}
	return true, 0
}

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
		return "an unexpected error occurred — please try again or contact an administrator"
	}
}

type AppHandler struct {
	gitops   *gitops.Client
	k8s      *k8s.Client
	vault    *vault.Client
	wf       *k8s.WorkflowClient
	github   *github.Client
	adminIDs map[int64]bool
	limiter  *redeployLimiter
}

func NewAppHandler(g *gitops.Client, k *k8s.Client, v *vault.Client, wf *k8s.WorkflowClient, gh *github.Client, adminIDs []int64) *AppHandler {
	m := make(map[int64]bool, len(adminIDs))
	for _, id := range adminIDs {
		m[id] = true
	}
	return &AppHandler{gitops: g, k8s: k, vault: v, wf: wf, github: gh, adminIDs: m, limiter: newRedeployLimiter()}
}

func (h *AppHandler) isAdmin(githubID int64) bool {
	return h.adminIDs[githubID]
}

// GET /projects/:project/apps
func (h *AppHandler) List(c *gin.Context) {
	proj, ok := projectFromCtx(c)
	if !ok {
		return
	}
	// Augment each application with a top-level buildType field for easy consumption
	// by CLI and MCP tools without requiring clients to inspect the nested build object.
	type appSummary struct {
		Name                 string `json:"name"`
		BuildType            string `json:"buildType,omitempty"`
		DisableNetworkPolicy bool   `json:"disableNetworkPolicy,omitempty"`
		GitHub               any    `json:"github"`
		Build                any    `json:"build"`
		Endpoints            any    `json:"endpoints,omitempty"`
	}
	summaries := make([]appSummary, 0, len(proj.Applications))
	for _, a := range proj.Applications {
		summaries = append(summaries, appSummary{
			Name:                 a.Name,
			BuildType:            a.Build.BuildType(),
			DisableNetworkPolicy: a.DisableNetworkPolicy,
			GitHub:               a.GitHub,
			Build:                a.Build,
			Endpoints:            a.Endpoints,
		})
	}
	c.JSON(http.StatusOK, gin.H{"applications": summaries})
}

// GET /projects/:project/apps/:app
func (h *AppHandler) Get(c *gin.Context) {
	app := c.Param("app")
	proj, ok := projectFromCtx(c)
	if !ok {
		return
	}
	for _, a := range proj.Applications {
		if a.Name == app {
			// Enrich with top-level buildType for consistency with the List endpoint.
			type appDetail struct {
				Name                 string `json:"name"`
				BuildType            string `json:"buildType,omitempty"`
				DisableNetworkPolicy bool   `json:"disableNetworkPolicy,omitempty"`
				GitHub               any    `json:"github"`
				Build                any    `json:"build"`
				Endpoints            any    `json:"endpoints,omitempty"`
			}
			c.JSON(http.StatusOK, appDetail{
				Name:                 a.Name,
				BuildType:            a.Build.BuildType(),
				DisableNetworkPolicy: a.DisableNetworkPolicy,
				GitHub:               a.GitHub,
				Build:                a.Build,
				Endpoints:            a.Endpoints,
			})
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

	// Compute deployPhase for UI clarity.
	// When the K8s deployment exists, reflect the actual runtime status directly
	// so clients can distinguish running/failed/pending/stopped without a second
	// status field. Only fall back to build-phase values when not yet deployed.
	deployPhase := "not_deployed"
	switch status.Status {
	case "running", "failed", "pending", "stopped":
		deployPhase = status.Status
	}
	if deployPhase == "not_deployed" && lastBuild != nil {
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

	// disableNetworkPolicy is admin-only — regular users cannot bypass network isolation
	if app.DisableNetworkPolicy && !h.isAdmin(c.GetInt64("githubId")) {
		c.JSON(http.StatusForbidden, gin.H{"error": "disableNetworkPolicy requires admin privileges"})
		return
	}

	if err := validateBuildSpec(app.Build); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := domain.ValidTriggerPaths(app.GitHub.TriggerPaths); err != nil {
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

	// Verify the authenticated user belongs to the repo's owner (org or personal account).
	// Prevents a user from claiming a CI target on a repository they don't control.
	if !h.isAdmin(c.GetInt64("githubId")) {
		if err := h.github.VerifyRepoAccess(c.Request.Context(), installID, app.GitHub.Owner, app.GitHub.Repo, c.GetString("username")); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
	}

	app.GitHub.InstallationID = installID

	// Validate endpoints: block reserved infrastructure domains
	if err := validateEndpoints(app.Endpoints); err != nil {
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

	if err := h.gitops.AddApplication(project, app, c.GetString("username")); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := h.vault.InitEnv(project, app.Name); err != nil {
		// Rollback gitops to avoid leaving app in inconsistent state (exists in gitops but no vault secret)
		if rbErr := h.gitops.DeleteApplication(project, app.Name, c.GetString("username")); rbErr != nil {
			log.Printf("warn: rollback gitops after vault failure: %v", rbErr)
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initialize app secrets"})
		return
	}
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

	// disableNetworkPolicy is admin-only
	if updated.DisableNetworkPolicy && !h.isAdmin(c.GetInt64("githubId")) {
		c.JSON(http.StatusForbidden, gin.H{"error": "disableNetworkPolicy requires admin privileges"})
		return
	}

	if err := validateBuildSpec(updated.Build); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := domain.ValidTriggerPaths(updated.GitHub.TriggerPaths); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate endpoints: block reserved infrastructure domains
	if err := validateEndpoints(updated.Endpoints); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check domain conflicts
	var domains []string
	for _, ep := range updated.Endpoints {
		domains = append(domains, ep.Routes...)
	}
	if len(domains) > 0 {
		if err := h.gitops.CheckDomainConflict(project, updated.Name, domains); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
	}

	// Always re-resolve installationId from GitHub (user-supplied value is ignored).
	// This ensures owner/repo changes don't inherit stale credentials.
	installID, err := h.github.GetRepoInstallationID(c.Request.Context(), updated.GitHub.Owner, updated.GitHub.Repo)
	if err != nil {
		var notInstalled *github.ErrAppNotInstalled
		if errors.As(err, &notInstalled) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":       fmt.Sprintf("GitHub App not installed on %s/%s", updated.GitHub.Owner, updated.GitHub.Repo),
				"install_url": notInstalled.InstallURL,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Same ownership check as Create: prevent pointing an app at a repo the user doesn't control.
	if !h.isAdmin(c.GetInt64("githubId")) {
		if err := h.github.VerifyRepoAccess(c.Request.Context(), installID, updated.GitHub.Owner, updated.GitHub.Repo, c.GetString("username")); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
	}

	updated.GitHub.InstallationID = installID

	// Preserve hash (server-managed, set by CI)
	proj, ok := projectFromCtx(c)
	if !ok {
		return
	}
	for _, existing := range proj.Applications {
		if existing.Name == appName {
			updated.GitHub.Hash = existing.GitHub.Hash
			break
		}
	}

	if err := h.gitops.UpdateApplication(project, updated, c.GetString("username")); err != nil {
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
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
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Clean up Vault secrets — log failures but do not fail the request;
	// the GitOps record is already removed so the app is effectively deleted.
	if err := h.vault.DeleteEnv(project, app); err != nil {
		log.Printf("warn: vault.DeleteEnv %s/%s: %v", project, app, err)
	}

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

	proj, ok := projectFromCtx(c)
	if !ok {
		return
	}
	var ports []int
	found := false
	for _, a := range proj.Applications {
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

// validateEndpoints returns an error if any endpoint has an invalid port or reserved hostname.
func validateEndpoints(endpoints []domain.Endpoint) error {
	if err := domain.ValidEndpoints(endpoints); err != nil {
		return err
	}
	for _, ep := range endpoints {
		for _, route := range ep.Routes {
			host := strings.SplitN(route, "/", 2)[0]
			if err := domain.ValidRouteHost(host); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateBuildSpec checks all build command fields for shell injection patterns.
func validateBuildSpec(b domain.Build) error {
	check := func(label, val string) error {
		if val == "" {
			return nil
		}
		return domain.ValidBuildCommand(val)
	}
	checkPath := func(label, val string) error {
		if val == "" {
			return nil
		}
		return domain.ValidFilePath(val)
	}
	if b.Gradle != nil {
		if err := check("buildCommand", b.Gradle.BuildCommand); err != nil {
			return err
		}
		if err := checkPath("jarOutputPath", b.Gradle.JarOutputPath); err != nil {
			return err
		}
	}
	if b.NodeJS != nil {
		if err := check("buildCommand", b.NodeJS.BuildCommand); err != nil {
			return err
		}
		if err := check("startCommand", b.NodeJS.StartCommand); err != nil {
			return err
		}
	}
	if b.React != nil {
		if err := check("buildCommand", b.React.BuildCommand); err != nil {
			return err
		}
		if err := checkPath("distPath", b.React.DistPath); err != nil {
			return err
		}
	}
	if b.Vite != nil {
		if err := check("buildCommand", b.Vite.BuildCommand); err != nil {
			return err
		}
		if err := checkPath("distPath", b.Vite.DistPath); err != nil {
			return err
		}
	}
	if b.Vue != nil {
		if err := check("buildCommand", b.Vue.BuildCommand); err != nil {
			return err
		}
		if err := checkPath("distPath", b.Vue.DistPath); err != nil {
			return err
		}
	}
	if b.NextJS != nil {
		if err := check("buildCommand", b.NextJS.BuildCommand); err != nil {
			return err
		}
		if err := check("startCommand", b.NextJS.StartCommand); err != nil {
			return err
		}
	}
	if b.NextJSExport != nil {
		if err := check("buildCommand", b.NextJSExport.BuildCommand); err != nil {
			return err
		}
		if err := checkPath("distPath", b.NextJSExport.DistPath); err != nil {
			return err
		}
	}
	if b.Go != nil {
		if err := check("buildCommand", b.Go.BuildCommand); err != nil {
			return err
		}
	}
	if b.Rust != nil {
		if err := check("buildCommand", b.Rust.BuildCommand); err != nil {
			return err
		}
	}
	if b.Maven != nil {
		if err := check("buildCommand", b.Maven.BuildCommand); err != nil {
			return err
		}
		if err := checkPath("jarOutputPath", b.Maven.JarOutputPath); err != nil {
			return err
		}
	}
	if b.Django != nil {
		if err := check("buildCommand", b.Django.BuildCommand); err != nil {
			return err
		}
		if err := check("startCommand", b.Django.StartCommand); err != nil {
			return err
		}
	}
	if b.Flask != nil {
		if err := check("buildCommand", b.Flask.BuildCommand); err != nil {
			return err
		}
		if err := check("startCommand", b.Flask.StartCommand); err != nil {
			return err
		}
	}
	if b.Docker != nil {
		if err := checkPath("dockerfilePath", b.Docker.DockerfilePath); err != nil {
			return err
		}
		if err := checkPath("contextPath", b.Docker.ContextPath); err != nil {
			return err
		}
	}
	return nil
}

// POST /projects/:project/apps/:app/trigger
// Triggers CI by creating a new Argo Workflow for the app.
func (h *AppHandler) Trigger(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

	if h.wf == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CI trigger unavailable"})
		return
	}

	// Rate limit: 1 redeploy per app per 60 seconds to prevent Workflow flooding
	limiterKey := project + "/" + app
	if ok, wait := h.limiter.allow(limiterKey); !ok {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":   fmt.Sprintf("redeploy rate limit exceeded — wait %ds before retrying", int(wait.Seconds())),
			"retryIn": int(wait.Seconds()),
		})
		return
	}

	// Resolve current SHA from GitHub so the workflow template takes the fast path
	// (github-event-type=push) and skips the sh process-substitution JWT code.
	proj, ok := projectFromCtx(c)
	if !ok {
		return
	}
	sha := ""
	for _, a := range proj.Applications {
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
