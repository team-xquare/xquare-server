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
const maxBootstrapBytes = 32 * 1024            // 32 KiB

var storageRe = regexp.MustCompile(`^(\d+)(Ki|Mi|Gi|Ti|Pi|E|P|T|G|M|K)$`)

func parseStorageBytes(s string) (int64, error) {
	m := storageRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid storage %q: must be a number followed by a unit (e.g. 1Gi, 500Mi)", s)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid storage value %q: %w", m[1], err)
	}
	units := map[string]int64{
		"Ki": 1024, "Mi": 1024 * 1024, "Gi": 1024 * 1024 * 1024,
		"Ti": 1024 * 1024 * 1024 * 1024, "Pi": 1024 * 1024 * 1024 * 1024 * 1024,
		"K": 1000, "M": 1000 * 1000, "G": 1000 * 1000 * 1000,
		"T": 1000 * 1000 * 1000 * 1000, "P": 1000 * 1000 * 1000 * 1000 * 1000,
		"E": 1000 * 1000 * 1000 * 1000 * 1000 * 1000,
	}
	unit := units[m[2]]
	// Guard against integer overflow: if n > MaxInt64/unit the multiplication wraps.
	if n > 0 && unit > 0 && n > (1<<63-1)/unit {
		return 0, fmt.Errorf("storage value %q overflows int64", s)
	}
	return n * unit, nil
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
	proj, ok := projectFromCtx(c)
	if !ok {
		return
	}
	addons := proj.Addons

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
			Ready:   h.k8s.AddonReady(c.Request.Context(), project, a.Name, a.Type),
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

	if err := validateName(addon.Name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid addon name: " + err.Error()})
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

	if len(addon.Bootstrap) > maxBootstrapBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("bootstrap must be less than %d bytes", maxBootstrapBytes)})
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

// PUT /projects/:project/addons/:addon
func (h *AddonHandler) Update(c *gin.Context) {
	project := c.Param("project")
	addonName := c.Param("addon")

	proj, ok := projectFromCtx(c)
	if !ok {
		return
	}
	var addon *domain.Addon
	for i := range proj.Addons {
		if proj.Addons[i].Name == addonName {
			addon = &proj.Addons[i]
			break
		}
	}
	if addon == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "addon not found"})
		return
	}

	var req struct {
		Buckets []domain.AddonBucket `json:"buckets"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if addon.Type != "seaweedfs" && len(req.Buckets) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "buckets are only supported for seaweedfs addons"})
		return
	}

	if err := h.gitops.UpdateAddon(project, addonName, req.Buckets, c.GetString("username")); err != nil {
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"name": addonName, "buckets": req.Buckets})
}

// DELETE /projects/:project/addons/:addon
func (h *AddonHandler) Delete(c *gin.Context) {
	project := c.Param("project")
	addonName := c.Param("addon")

	if err := h.gitops.DeleteAddon(project, addonName, c.GetString("username")); err != nil {
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
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

	proj, ok := projectFromCtx(c)
	if !ok {
		return
	}
	var addon *domain.Addon
	for i := range proj.Addons {
		if proj.Addons[i].Name == addonName {
			addon = &proj.Addons[i]
			break
		}
	}
	if addon == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "addon not found"})
		return
	}

	port := domain.AddonPort(addon.Type)
	ready := h.k8s.AddonReady(c.Request.Context(), project, addonName, addon.Type)

	password, err := h.k8s.GetAccessServerPassword(c.Request.Context(), project)
	if err != nil {
		password = ""
	}

	resp := gin.H{
		"name":     addon.Name,
		"type":     addon.Type,
		"host":     "xquare-remote-access-" + project + ".dsmhs.kr",
		"port":     port,
		"password": password,
		"ready":    ready,
	}

	if addon.Type == "seaweedfs" {
		ns := domain.Namespace(project)
		type bucketCreds struct {
			Name      string `json:"name"`
			AccessKey string `json:"accessKey"`
			SecretKey string `json:"secretKey"`
		}
		var creds []bucketCreds
		for _, b := range addon.Buckets {
			creds = append(creds, bucketCreds{
				Name:      b.Name,
				AccessKey: ns + "-" + addonName + "-" + b.Name,
				SecretKey: ns + "-" + addonName + "-" + b.Name + "-secret",
			})
		}
		resp["buckets"] = creds
	}

	c.JSON(http.StatusOK, resp)
}
