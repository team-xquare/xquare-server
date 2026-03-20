package handler

import (
	"fmt"
	"hash/adler32"
	"log"
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

// Project names: lowercase letters and numbers only, no hyphens (2-63 chars)
var projectNameRe = regexp.MustCompile(`^[a-z0-9]{2,63}$`)

func namespaceChecksum(project string) uint32 {
	return adler32.Checksum([]byte(domain.Namespace(project)))
}

type ProjectHandler struct {
	gitops   *gitops.Client
	vault    *vault.Client
	github   *github.Client
	k8s      *k8s.Client
	adminIDs map[int64]bool
}

func NewProjectHandler(g *gitops.Client, v *vault.Client, gh *github.Client, k *k8s.Client, adminIDs []int64) *ProjectHandler {
	m := make(map[int64]bool, len(adminIDs))
	for _, id := range adminIDs {
		m[id] = true
	}
	return &ProjectHandler{gitops: g, vault: v, github: gh, k8s: k, adminIDs: m}
}

func (h *ProjectHandler) isAdmin(githubID int64) bool {
	return h.adminIDs[githubID]
}

// GET /projects — only shows projects the user owns (admins see all)
func (h *ProjectHandler) List(c *gin.Context) {
	githubID := c.GetInt64("githubId")
	isAdmin := h.isAdmin(githubID)
	// ListProjectsWithAccess reads all project files in a single mutex lock,
	// avoiding the N+1 mutex contention of the old ListProjects + N*GetProject pattern.
	projects, err := h.gitops.ListProjectsWithAccess(githubID, isAdmin)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if projects == nil {
		projects = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"projects": projects})
}

// GET /projects/:project
func (h *ProjectHandler) Get(c *gin.Context) {
	project := c.Param("project")
	p, ok := projectFromCtx(c)
	if !ok {
		return
	}
	// Enrich applications with top-level buildType, consistent with GET /apps and GET /apps/:app.
	type appSummary struct {
		Name                 string `json:"name"`
		BuildType            string `json:"buildType,omitempty"`
		DisableNetworkPolicy bool   `json:"disableNetworkPolicy,omitempty"`
		GitHub               any    `json:"github"`
		Build                any    `json:"build"`
		Endpoints            any    `json:"endpoints,omitempty"`
	}
	summaries := make([]appSummary, 0, len(p.Applications))
	for _, a := range p.Applications {
		summaries = append(summaries, appSummary{
			Name:                 a.Name,
			BuildType:            a.Build.BuildType(),
			DisableNetworkPolicy: a.DisableNetworkPolicy,
			GitHub:               a.GitHub,
			Build:                a.Build,
			Endpoints:            a.Endpoints,
		})
	}
	// Enrich addons with live ready status, consistent with GET /projects/:project/addons.
	type addonSummary struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Storage string `json:"storage"`
		Ready   bool   `json:"ready"`
	}
	addonItems := make([]addonSummary, 0, len(p.Addons))
	for _, a := range p.Addons {
		ready := false
		if h.k8s != nil {
			ready = h.k8s.AddonReady(c.Request.Context(), project, a.Name, a.Type)
		}
		addonItems = append(addonItems, addonSummary{
			Name:    a.Name,
			Type:    a.Type,
			Storage: a.Storage,
			Ready:   ready,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"owners":       resolveUsernames(c, h.github, p.Owners),
		"applications": summaries,
		"addons":       addonItems,
	})
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project name: must be lowercase letters and numbers only, no hyphens (2-63 chars)"})
		return
	}

	// Check adler32sum(namespace) uniqueness — used as VictoriaMetrics tenant-id
	existing, err := h.gitops.ListProjects()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	newChecksum := namespaceChecksum(req.Name)
	for _, p := range existing {
		if namespaceChecksum(p) == newChecksum {
			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("project name %q conflicts with existing project %q (tenant-id collision)", req.Name, p)})
			return
		}
	}

	if err := h.gitops.CreateProject(req.Name, c.GetInt64("githubId"), c.GetString("username")); err != nil {
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
func (h *ProjectHandler) Delete(c *gin.Context) {
	project := c.Param("project")

	proj, ok := projectFromCtx(c)
	if !ok {
		return
	}

	if err := h.gitops.DeleteProject(project, c.GetString("username")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	for _, app := range proj.Applications {
		if err := h.vault.DeleteEnv(project, app.Name); err != nil {
			log.Printf("warn: vault.DeleteEnv %s/%s: %v", project, app.Name, err)
		}
	}

	// Delete K8s namespace (cascades Deployments, Services, PVCs, etc.)
	if h.k8s != nil {
		if err := h.k8s.DeleteNamespace(c.Request.Context(), project); err != nil {
			log.Printf("warn: k8s.DeleteNamespace %s: %v", project, err)
		}
	}

	c.Status(http.StatusNoContent)
}

// GET /projects/:project/members
func (h *ProjectHandler) ListMembers(c *gin.Context) {
	proj, ok := projectFromCtx(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"owners": resolveUsernames(c, h.github, proj.Owners)})
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

	if err := h.gitops.AddProjectMember(project, user.ID, c.GetString("username")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"added": user.ID, "username": user.Login})
}

// DELETE /projects/:project/members/:username
func (h *ProjectHandler) RemoveMember(c *gin.Context) {
	project := c.Param("project")
	targetUsername := c.Param("username")

	// Prevent removing the last owner — project would become permanently inaccessible
	proj, ok := projectFromCtx(c)
	if !ok {
		return
	}
	if len(proj.Owners) <= 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot remove the last owner of a project"})
		return
	}

	// Resolve username → GitHub ID
	user, err := h.github.GetUserByUsername(c.Request.Context(), targetUsername)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.gitops.RemoveProjectMember(project, user.ID, c.GetString("username")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// GET /projects/:project/dashboard
func (h *ProjectHandler) Dashboard(c *gin.Context) {
	project := c.Param("project")
	ns := domain.Namespace(project)
	dashURL := fmt.Sprintf("https://%s-observability-dashboard.dsmhs.kr", project)

	data, err := h.k8s.GetSecret(c.Request.Context(), ns, "grafana-admin-password")
	if err != nil {
		// Secret not ready yet (project just created) — return URL only
		c.JSON(http.StatusOK, gin.H{
			"url":      dashURL,
			"username": "admin",
			"password": nil,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"url":      dashURL,
		"username": "admin",
		"password": string(data["password"]),
	})
}
