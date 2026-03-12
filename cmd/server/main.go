package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/team-xquare/xquare-server/internal/api/handler"
	"github.com/team-xquare/xquare-server/internal/api/middleware"
	"github.com/team-xquare/xquare-server/internal/config"
	"github.com/team-xquare/xquare-server/internal/github"
	"github.com/team-xquare/xquare-server/internal/gitops"
	"github.com/team-xquare/xquare-server/internal/k8s"
	"github.com/team-xquare/xquare-server/internal/vault"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Init clients
	gitopsClient := gitops.NewClient(&cfg.GitOps)

	k8sClient, err := k8s.NewClient(&cfg.K8s)
	if err != nil {
		log.Fatalf("k8s: %v", err)
	}

	vaultClient, err := vault.NewClient(&cfg.Vault)
	if err != nil {
		log.Fatalf("vault: %v", err)
	}
	githubClient := github.NewClient(&cfg.GitHub)

	wfClient, err := k8s.NewWorkflowClient(&cfg.K8s, k8sClient)
	if err != nil {
		log.Printf("warn: workflow client init failed (build logs unavailable): %v", err)
	}

	// Init handlers
	authH := handler.NewAuthHandler(githubClient, cfg)
	projectH := handler.NewProjectHandler(gitopsClient, vaultClient, githubClient, k8sClient, cfg.JWT.AdminIDs)
	appH := handler.NewAppHandler(gitopsClient, k8sClient, vaultClient, wfClient, githubClient, cfg.JWT.AdminIDs)
	envH := handler.NewEnvHandler(vaultClient)
	addonH := handler.NewAddonHandler(gitopsClient, k8sClient)
	logsH := handler.NewLogsHandler(k8sClient)
	buildsH := handler.NewBuildsHandler(wfClient)
	allowlistH := handler.NewAllowlistHandler(gitopsClient, githubClient, cfg.JWT.AdminIDs)

	r := gin.Default()

	// Limit request body to 1 MiB to prevent memory exhaustion via large payloads
	r.Use(func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		c.Next()
	})

	// Security headers (API-only server, no HTML — still good defense-in-depth)
	r.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Next()
	})

	// CORS — restrict to allowed origins from CORS_ORIGINS env var (comma-separated).
	// Defaults to *.dsmhs.kr. Set "*" in CORS_ORIGINS only for local dev.
	corsOrigins := os.Getenv("CORS_ORIGINS")
	if corsOrigins == "" {
		corsOrigins = "https://xquare-server.dsmhs.kr"
	}
	allowedOrigins := make(map[string]bool)
	for _, o := range strings.Split(corsOrigins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			allowedOrigins[o] = true
		}
	}
	r.Use(func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (allowedOrigins["*"] || allowedOrigins[origin]) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
		}
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	})

	// Health
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Auth (public) — callback is rate-limited to 20 req/min per IP
	auth := r.Group("/auth")
	{
		auth.POST("/github/callback", middleware.RateLimit(20, time.Minute), authH.GitHubCallback)
		auth.GET("/me", middleware.Auth(cfg.JWT.Secret), authH.Me)
	}

	// Protected routes
	api := r.Group("/", middleware.Auth(cfg.JWT.Secret), middleware.Allowlist(gitopsClient))
	{
		// Admin: allowlist management
		admin := api.Group("/admin")
		{
			admin.GET("/allowlist", allowlistH.List)
			admin.POST("/allowlist", allowlistH.Add)
			admin.DELETE("/allowlist/:username", allowlistH.Remove)
		}

		// Projects (list + create are not project-scoped)
		api.GET("/projects", projectH.List)
		api.POST("/projects", projectH.Create)

		// All project-specific routes require project ownership
		proj := api.Group("/projects/:project", middleware.ProjectAccess(gitopsClient, cfg.JWT.AdminIDs))
		{
			proj.GET("", projectH.Get)
			proj.DELETE("", projectH.Delete)

			// Members
			proj.GET("/members", projectH.ListMembers)
			proj.POST("/members", projectH.AddMember)
			proj.DELETE("/members/:username", projectH.RemoveMember)

			// Apps
			apps := proj.Group("/apps")
			{
				apps.GET("", appH.List)
				apps.POST("", appH.Create)

				// App-specific routes: verify :app belongs to this project before dispatching
				app := apps.Group("/:app", middleware.AppAccess())
				{
					app.GET("", appH.Get)
					app.PUT("", appH.Update)
					app.DELETE("", appH.Delete)
					app.GET("/status", appH.Status)
					app.POST("/redeploy", appH.Redeploy)
					app.GET("/logs", logsH.Stream)
					app.GET("/builds", buildsH.List)
					app.GET("/builds/:workflow/logs", buildsH.StreamLogs)
					app.GET("/tunnel", appH.Tunnel)
				}
			}

			// Env — also gated by AppAccess
			env := proj.Group("/apps/:app", middleware.AppAccess())
			{
				env.GET("/env", envH.Get)
				env.PUT("/env", envH.Set)
				env.PATCH("/env", envH.Patch)
				env.DELETE("/env/:key", envH.DeleteKey)
			}

			// Addons
			addons := proj.Group("/addons")
			{
				addons.GET("", addonH.List)
				addons.POST("", addonH.Create)
				addons.DELETE("/:addon", addonH.Delete)
				addons.GET("/:addon/connection", addonH.Connection)
			}
		}
	}

	log.Printf("starting xquare-server on :%s", cfg.Server.Port)
	if err := r.Run(":" + cfg.Server.Port); err != nil {
		log.Fatalf("server: %v", err)
	}
}
