package handler

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/domain"
	"github.com/team-xquare/xquare-server/internal/gitops"
	"github.com/team-xquare/xquare-server/internal/k8s"
)

const maxStorageBytes = 4 * 1024 * 1024 * 1024 // 4Gi

var storageRe = regexp.MustCompile(`^(\d+)(Ki|Mi|Gi|Ti|Pi|E|P|T|G|M|K)$`)

func parseStorageBytes(s string) (int64, error) {
	m := storageRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid storage %q: must be a number followed by a unit (e.g. 1Gi, 500Mi)", s)
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	units := map[string]int64{
		"Ki": 1024, "Mi": 1024 * 1024, "Gi": 1024 * 1024 * 1024,
		"Ti": 1024 * 1024 * 1024 * 1024, "Pi": 1024 * 1024 * 1024 * 1024 * 1024,
		"K": 1000, "M": 1000 * 1000, "G": 1000 * 1000 * 1000,
		"T": 1000 * 1000 * 1000 * 1000, "P": 1000 * 1000 * 1000 * 1000 * 1000,
		"E": 1000 * 1000 * 1000 * 1000 * 1000 * 1000,
	}
	return n * units[m[2]], nil
}

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
	p, _ := c.Get("project")
	addons := p.(*domain.Project).Addons

	type addonItem struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Storage string `json:"storage"`
		Ready   bool   `json:"ready"`
	}

	items := make([]addonItem, 0, len(addons))
	for _, a := range addons {
		items = append(items, addonItem{
			Name:    a.Name,
			Type:    a.Type,
			Storage: a.Storage,
			Ready:   h.k8s.AddonReady(c.Request.Context(), project, a.Name),
		})
	}
	c.JSON(http.StatusOK, gin.H{"addons": items})
}

// POST /projects/:project/addons
func (h *AddonHandler) Create(c *gin.Context) {
	project := c.Param("project")

	var addon domain.Addon
	if err := c.ShouldBindJSON(&addon); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := domain.ValidAddonType(addon.Type); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	storageBytes, err := parseStorageBytes(addon.Storage)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if storageBytes >= maxStorageBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "storage must be less than 4Gi"})
		return
	}

	if err := h.gitops.AddAddon(project, addon, c.GetString("username")); err != nil {
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

	if err := h.gitops.DeleteAddon(project, addonName, c.GetString("username")); err != nil {
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

	proj, _ := c.Get("project")
	var addon *domain.Addon
	for i := range proj.(*domain.Project).Addons {
		if proj.(*domain.Project).Addons[i].Name == addonName {
			addon = &proj.(*domain.Project).Addons[i]
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
	ready := h.k8s.AddonReady(c.Request.Context(), project, addonName)

	c.JSON(http.StatusOK, gin.H{
		"name":     addon.Name,
		"type":     addon.Type,
		"host":     "xquare-remote-access-" + project + ".dsmhs.kr",
		"port":     port,
		"password": password,
		"ready":    ready,
	})
}
