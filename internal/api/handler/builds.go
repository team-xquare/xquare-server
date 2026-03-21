package handler

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/team-xquare/xquare-server/internal/k8s"
)

// workflowNameRe enforces K8s resource naming rules to prevent label selector injection.
var workflowNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9\-\.]*[a-z0-9])?$`)

type BuildsHandler struct {
	wf *k8s.WorkflowClient
}

func NewBuildsHandler(wf *k8s.WorkflowClient) *BuildsHandler {
	return &BuildsHandler{wf: wf}
}

// GET /projects/:project/apps/:app/builds
// Optional query params: ?limit=N (1-50, default 50), ?status=running|success|failed|pending
func (h *BuildsHandler) List(c *gin.Context) {
	if h.wf == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "build logs unavailable: workflow client not initialized"})
		return
	}
	project := c.Param("project")
	app := c.Param("app")

	// Verify the app exists in the project before querying Argo Workflows.
	// Without this check, a typo in the app name silently returns {"builds":[]}
	// instead of a 404, which is misleading for users and AI agents alike.
	proj, ok := projectFromCtx(c)
	if !ok {
		return
	}
	appExists := false
	for _, a := range proj.Applications {
		if a.Name == app {
			appExists = true
			break
		}
	}
	if !appExists {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("app %q not found in project %q", app, project)})
		return
	}

	workflows, err := h.wf.ListWorkflows(c.Request.Context(), project, app)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Guarantee a JSON array (never null) so clients can safely iterate.
	if workflows == nil {
		workflows = []k8s.WorkflowInfo{}
	}

	// Apply optional status filter — e.g. ?status=running shows only in-progress builds.
	// Valid values: running, success, failed, pending. Unknown values return an empty list
	// rather than an error to avoid breaking callers on future status additions.
	if statusFilter := c.Query("status"); statusFilter != "" {
		filtered := workflows[:0]
		for _, w := range workflows {
			if w.Status == statusFilter {
				filtered = append(filtered, w)
			}
		}
		workflows = filtered
	}

	// Apply optional server-side limit — callers can request fewer items to reduce response size.
	if limitStr := c.Query("limit"); limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n < len(workflows) {
			workflows = workflows[:n]
		}
	}

	c.JSON(http.StatusOK, gin.H{"builds": workflows})
}

// GET /projects/:project/apps/:app/builds/:workflow/logs
// Also supports WebSocket upgrade
func (h *BuildsHandler) StreamLogs(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")
	workflowName := c.Param("workflow")
	follow := c.Query("follow") != "false"

	// Verify the app exists before resolving "latest" or streaming.
	// Without this check, an unknown app name returns "no builds found" instead of 404.
	proj, ok := projectFromCtx(c)
	if !ok {
		return
	}
	appExists := false
	for _, a := range proj.Applications {
		if a.Name == app {
			appExists = true
			break
		}
	}
	if !appExists {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("app %q not found in project %q", app, project)})
		return
	}

	// Parse optional ?tail=N (default 500, max 2000)
	tailLines := int64(500)
	if tailStr := c.Query("tail"); tailStr != "" {
		if n, err := strconv.ParseInt(tailStr, 10, 64); err == nil && n > 0 {
			if n > 2000 {
				n = 2000
			}
			tailLines = n
		}
	}

	// Validate workflow name to prevent K8s label selector injection.
	// "latest" is the only special value allowed before resolution.
	if workflowName != "latest" && (len(workflowName) > 253 || !workflowNameRe.MatchString(workflowName)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid workflow name"})
		return
	}

	// resolve "latest" to actual workflow name
	if workflowName == "latest" {
		wfs, err := h.wf.ListWorkflows(c.Request.Context(), project, app)
		if err != nil || len(wfs) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "no builds found"})
			return
		}
		workflowName = wfs[0].ID
	}

	if websocket.IsWebSocketUpgrade(c.Request) {
		h.streamWS(c, project, workflowName, follow, tailLines)
		return
	}
	h.streamHTTP(c, project, workflowName, follow, tailLines)
}

func (h *BuildsHandler) streamHTTP(c *gin.Context, project, workflowName string, follow bool, tailLines int64) {
	rc, err := h.wf.StreamWorkflowLogs(c.Request.Context(), project, workflowName, follow, tailLines)
	if err != nil {
		if strings.Contains(err.Error(), "build initializing") {
			c.JSON(http.StatusAccepted, gin.H{"status": "initializing", "message": err.Error()})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	defer rc.Close()

	c.Header("Content-Type", "application/x-ndjson")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusOK)

	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 512*1024), 512*1024) // prevent silent truncation of long Docker build lines
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

func (h *BuildsHandler) streamWS(c *gin.Context, project, workflowName string, follow bool, tailLines int64) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ctx := c.Request.Context()
	rc, err := h.wf.StreamWorkflowLogs(ctx, project, workflowName, follow, tailLines)
	if err != nil {
		var payload map[string]string
		if strings.Contains(err.Error(), "build initializing") {
			payload = map[string]string{"error": err.Error(), "code": "initializing"}
		} else {
			payload = map[string]string{"error": err.Error()}
		}
		msg, _ := json.Marshal(payload)
		_ = conn.WriteMessage(websocket.TextMessage, msg)
		return
	}
	defer rc.Close()

	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 512*1024), 512*1024) // prevent silent truncation of long Docker build lines
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
