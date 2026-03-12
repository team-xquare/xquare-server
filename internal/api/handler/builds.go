package handler

import (
	"bufio"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/team-xquare/xquare-server/internal/k8s"
)

type BuildsHandler struct {
	wf *k8s.WorkflowClient
}

func NewBuildsHandler(wf *k8s.WorkflowClient) *BuildsHandler {
	return &BuildsHandler{wf: wf}
}

// GET /projects/:project/apps/:app/builds
func (h *BuildsHandler) List(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

	workflows, err := h.wf.ListWorkflows(c.Request.Context(), project, app)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"builds": workflows})
}

// GET /projects/:project/apps/:app/builds/:workflow/logs
// Also supports WebSocket upgrade
func (h *BuildsHandler) StreamLogs(c *gin.Context) {
	project := c.Param("project")
	workflowName := c.Param("workflow")
	follow := c.Query("follow") != "false"

	// resolve "latest" to actual workflow name
	if workflowName == "latest" {
		app := c.Param("app")
		wfs, err := h.wf.ListWorkflows(c.Request.Context(), project, app)
		if err != nil || len(wfs) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "no builds found"})
			return
		}
		workflowName = wfs[0].Name
	}

	if websocket.IsWebSocketUpgrade(c.Request) {
		h.streamWS(c, project, workflowName, follow)
		return
	}
	h.streamHTTP(c, project, workflowName, follow)
}

func (h *BuildsHandler) streamHTTP(c *gin.Context, project, workflowName string, follow bool) {
	rc, err := h.wf.StreamWorkflowLogs(c.Request.Context(), project, workflowName, follow)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rc.Close()

	c.Header("Content-Type", "application/x-ndjson")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusOK)

	scanner := bufio.NewScanner(rc)
	for scanner.Scan() {
		c.Writer.WriteString(scanner.Text() + "\n")
		c.Writer.Flush()
		select {
		case <-c.Request.Context().Done():
			return
		default:
		}
	}
}

func (h *BuildsHandler) streamWS(c *gin.Context, project, workflowName string, follow bool) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ctx := c.Request.Context()
	rc, err := h.wf.StreamWorkflowLogs(ctx, project, workflowName, follow)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"`+err.Error()+`"}`))
		return
	}
	defer rc.Close()

	scanner := bufio.NewScanner(rc)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := conn.WriteMessage(websocket.TextMessage, scanner.Bytes()); err != nil {
			return
		}
	}
}
