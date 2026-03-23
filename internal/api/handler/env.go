package handler

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/domain"
	"github.com/team-xquare/xquare-server/internal/vault"
)

// maxEnvTotalBytes is the maximum combined size (keys + values) of all env vars
// for a single app. Prevents unbounded Vault secret growth via repeated PATCHes.
const maxEnvTotalBytes = 1 * 1024 * 1024 // 1 MiB

func totalEnvSize(envs map[string]string) int {
	n := 0
	for k, v := range envs {
		n += len(k) + len(v)
	}
	return n
}

type EnvHandler struct {
	vault *vault.Client
}

func NewEnvHandler(v *vault.Client) *EnvHandler {
	return &EnvHandler{vault: v}
}

// appExistsInCtx checks that the app exists in the project loaded by middleware.
// Returns false and writes 404 if not found.
func appExistsInCtx(c *gin.Context, project, app string) bool {
	proj, ok := projectFromCtx(c)
	if !ok {
		return false
	}
	for _, a := range proj.Applications {
		if a.Name == app {
			return true
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("app %q not found in project %q", app, project)})
	return false
}

// GET /projects/:project/apps/:app/env
func (h *EnvHandler) Get(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

	if !appExistsInCtx(c, project, app) {
		return
	}

	envs, err := h.vault.GetEnv(project, app)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve environment variables"})
		return
	}
	c.JSON(http.StatusOK, envs)
}

// PUT /projects/:project/apps/:app/env
// Full replace — sets exactly these keys (removes keys not in request)
func (h *EnvHandler) Set(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

	if !appExistsInCtx(c, project, app) {
		return
	}

	var envs map[string]string
	if err := c.ShouldBindJSON(&envs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	for k := range envs {
		if err := domain.ValidEnvKey(k); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if totalEnvSize(envs) > maxEnvTotalBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "env vars exceed 1 MiB total size limit"})
		return
	}

	if err := h.vault.SetEnv(project, app, envs); err != nil {
		if errors.Is(err, vault.ErrEnvTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set environment variables"})
		return
	}
	c.JSON(http.StatusOK, envs)
}

// PATCH /projects/:project/apps/:app/env
// Partial update — merges with existing keys.
// The 1 MiB total size cap is enforced atomically inside vault.PatchEnv (under its mutex)
// to prevent TOCTOU races. No pre-check is done here to avoid a redundant read.
func (h *EnvHandler) Patch(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

	if !appExistsInCtx(c, project, app) {
		return
	}

	var patch map[string]string
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	for k := range patch {
		if err := domain.ValidEnvKey(k); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	if err := h.vault.PatchEnv(project, app, patch); err != nil {
		if errors.Is(err, vault.ErrEnvTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update environment variables"})
		return
	}

	// Return the merged result
	envs, err := h.vault.GetEnv(project, app)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"updated": len(patch)})
		return
	}
	c.JSON(http.StatusOK, envs)
}

// DELETE /projects/:project/apps/:app/env/:key
func (h *EnvHandler) DeleteKey(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")
	key := c.Param("key")

	if !appExistsInCtx(c, project, app) {
		return
	}

	if err := domain.ValidEnvKey(key); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.vault.DeleteEnvKey(project, app, key); err != nil {
		if errors.Is(err, vault.ErrEnvKeyNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("env key %q not found", key)})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete environment variable"})
		return
	}
	c.Status(http.StatusNoContent)
}
