package handler

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/team-xquare/xquare-server/internal/k8s"
)

const maxTailLines = int64(5000)

var upgrader = websocket.Upgrader{
	// Only allow WebSocket upgrades from the same origin or from non-browser
	// clients (which send no Origin header). Prevents CSRF-like WS hijacking.
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // non-browser client (CLI, etc.)
		}
		host := r.Host
		return origin == "https://"+host || origin == "http://"+host
	},
}

type LogsHandler struct {
	k8s *k8s.Client
}

func NewLogsHandler(k *k8s.Client) *LogsHandler {
	return &LogsHandler{k8s: k}
}

// GET /projects/:project/apps/:app/logs
// Supports both HTTP streaming (SSE-style NDJSON) and WebSocket upgrade
func (h *LogsHandler) Stream(c *gin.Context) {
	project := c.Param("project")
	app := c.Param("app")

	tailLines := int64(100)
	if t := c.Query("tail"); t != "" {
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			tailLines = n
		}
	}
	if tailLines > maxTailLines {
		tailLines = maxTailLines
	}
	if tailLines < 1 {
		tailLines = 1
	}
	follow := c.Query("follow") != "false"

	if websocket.IsWebSocketUpgrade(c.Request) {
		h.streamWS(c, project, app, tailLines, follow)
		return
	}
	h.streamHTTP(c, project, app, tailLines, follow)
}

func (h *LogsHandler) streamHTTP(c *gin.Context, project, app string, tailLines int64, follow bool) {
	rc, err := h.k8s.StreamPodLogs(c.Request.Context(), project, app, tailLines, follow)
	if err != nil {
		var notDeployed *k8s.ErrAppNotDeployed
		if errors.As(err, &notDeployed) {
			c.JSON(http.StatusNotFound, gin.H{"error": notDeployed.Error(), "code": "not_deployed"})
			return
		}
		var notReady *k8s.ErrPodNotReady
		if errors.As(err, &notReady) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": notReady.Error(), "code": "pod_not_ready"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "로그를 가져올 수 없습니다 — 잠시 후 다시 시도하세요"})
		return
	}
	defer rc.Close()

	c.Header("Content-Type", "application/x-ndjson")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusOK)

	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 512*1024), 512*1024) // match CLI buffer; default 64KB drops long Docker build lines
	for scanner.Scan() {
		line := scanner.Text()
		c.Writer.WriteString(line + "\n")
		c.Writer.Flush()

		select {
		case <-c.Request.Context().Done():
			return
		default:
		}
	}
}

func (h *LogsHandler) streamWS(c *gin.Context, project, app string, tailLines int64, follow bool) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ctx := c.Request.Context()
	rc, err := h.k8s.StreamPodLogs(ctx, project, app, tailLines, follow)
	if err != nil {
		var notDeployed *k8s.ErrAppNotDeployed
		var notReady *k8s.ErrPodNotReady
		if errors.As(err, &notDeployed) {
			msg, _ := json.Marshal(map[string]string{"error": notDeployed.Error(), "code": "not_deployed"})
			_ = conn.WriteMessage(websocket.TextMessage, msg)
		} else if errors.As(err, &notReady) {
			msg, _ := json.Marshal(map[string]string{"error": notReady.Error(), "code": "pod_not_ready"})
			_ = conn.WriteMessage(websocket.TextMessage, msg)
		} else {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"로그를 가져올 수 없습니다 — 잠시 후 다시 시도하세요"}`))
		}
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
