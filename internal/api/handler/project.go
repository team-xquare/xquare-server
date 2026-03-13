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
	all, err := h.gitops.ListProjects()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if h.isAdmin(githubID) {
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
	p, ok := projectFromCtx(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"owners":       resolveUsernames(c, h.github, p.Owners),
		"applications": p.Applications,
		"addons":       p.Addons,
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
	c.JSON(http.StatusOK, gin.H{"added": user.ID})
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
