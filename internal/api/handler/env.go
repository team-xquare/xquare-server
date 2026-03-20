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

// GET /projects/:project/apps/:app/env
func (h *EnvHandler) Get(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

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
// The 1 MiB total size cap is enforced INSIDE vault.PatchEnv (under its mutex)
// to prevent a TOCTOU race where two concurrent PATCHes both pass a pre-check
// but their combined writes exceed the limit.
func (h *EnvHandler) Patch(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

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

	// Read current state and check total size after merge before writing
	current, err := h.vault.GetEnv(project, app)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	merged := make(map[string]string, len(current)+len(patch))
	for k, v := range current {
		merged[k] = v
	}
	for k, v := range patch {
		merged[k] = v
	}
	if totalEnvSize(merged) > maxEnvTotalBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "env vars exceed 1 MiB total size limit after merge"})
		return
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
