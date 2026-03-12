package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/gitops"
)

type ProjectHandler struct {
	gitops *gitops.Client
}

func NewProjectHandler(g *gitops.Client) *ProjectHandler {
	return &ProjectHandler{gitops: g}
}

// GET /projects
func (h *ProjectHandler) List(c *gin.Context) {
	projects, err := h.gitops.ListProjects()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"projects": projects})
}

// GET /projects/:project
func (h *ProjectHandler) Get(c *gin.Context) {
	project := c.Param("project")
	p, err := h.gitops.GetProject(project)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, p)
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

	if err := h.gitops.CreateProject(req.Name); err != nil {
		if err.Error() == "project already exists: "+req.Name {
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
	if err := h.gitops.DeleteProject(project); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
