package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/domain"
	"github.com/team-xquare/xquare-server/internal/gitops"
	"github.com/team-xquare/xquare-server/internal/k8s"
)

type AddonHandler struct {
	gitops *gitops.Client
	k8s    *k8s.Client
}

func NewAddonHandler(g *gitops.Client, k *k8s.Client) *AddonHandler {
	return &AddonHandler{gitops: g, k8s: k}
}

// GET /projects/:project/addons
func (h *AddonHandler) List(c *gin.Context) {
	project := c.Param("project")
	p, err := h.gitops.GetProject(project)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"addons": p.Addons})
}

// POST /projects/:project/addons
func (h *AddonHandler) Create(c *gin.Context) {
	project := c.Param("project")

	var addon domain.Addon
	if err := c.ShouldBindJSON(&addon); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.gitops.AddAddon(project, addon); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, addon)
}

// DELETE /projects/:project/addons/:addon
func (h *AddonHandler) Delete(c *gin.Context) {
	project := c.Param("project")
	addonName := c.Param("addon")

	if err := h.gitops.DeleteAddon(project, addonName); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// GET /projects/:project/addons/:addon/connection
// Returns connection info for DB addon (host, port, password for tunneling)
func (h *AddonHandler) Connection(c *gin.Context) {
	project := c.Param("project")
	addonName := c.Param("addon")

	p, err := h.gitops.GetProject(project)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	var addon *domain.Addon
	for i := range p.Addons {
		if p.Addons[i].Name == addonName {
			addon = &p.Addons[i]
			break
		}
	}
	if addon == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "addon not found"})
		return
	}

	password, err := h.k8s.GetAccessServerPassword(c.Request.Context(), project)
	if err != nil {
		password = ""
	}

	port := domain.AddonPort(addon.Type)

	c.JSON(http.StatusOK, gin.H{
		"name":        addon.Name,
		"type":        addon.Type,
		"host":        addon.Name + "." + domain.Namespace(project) + ".svc.cluster.local",
		"port":        port,
		"tunnel_host": "xquare-remote-access-" + project + ".dsmhs.kr",
		"tunnel_port": port,
		"password":    password,
	})
}
