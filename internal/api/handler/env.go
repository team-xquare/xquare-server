package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/domain"
	"github.com/team-xquare/xquare-server/internal/vault"
)

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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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

	if err := h.vault.SetEnv(project, app, envs); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, envs)
}

// PATCH /projects/:project/apps/:app/env
// Partial update — merges with existing keys
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

	if err := h.vault.PatchEnv(project, app, patch); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
