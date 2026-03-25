package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
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
	authH := handler.NewAuthHandler(githubClient, cfg, gitopsClient)
	projectH := handler.NewProjectHandler(gitopsClient, vaultClient, githubClient, k8sClient, cfg.JWT.AdminIDs)
	appH := handler.NewAppHandler(gitopsClient, k8sClient, vaultClient, wfClient, githubClient, cfg.JWT.AdminIDs)
	envH := handler.NewEnvHandler(vaultClient)
	addonH := handler.NewAddonHandler(gitopsClient, k8sClient)
	logsH := handler.NewLogsHandler(k8sClient)
	buildsH := handler.NewBuildsHandler(wfClient)
	allowlistH := handler.NewAllowlistHandler(gitopsClient, githubClient, cfg.JWT.AdminIDs)

	r := gin.Default()

	// Restrict trusted proxies to prevent X-Forwarded-For spoofing (rate limit bypass).
	// Set TRUSTED_PROXIES env var to a comma-separated list of trusted proxy CIDRs/IPs
	// (e.g., "10.0.0.0/8,172.16.0.0/12"). Empty = trust nothing (direct connections only).
	{
		trustedProxies := os.Getenv("TRUSTED_PROXIES")
		var proxies []string
		for _, p := range strings.Split(trustedProxies, ",") {
			if p = strings.TrimSpace(p); p != "" {
				proxies = append(proxies, p)
			}
		}
		if err := r.SetTrustedProxies(proxies); err != nil {
			log.Fatalf("trusted proxies: %v", err)
		}
	}

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

	// Rate limiters (shared instances — one bucket map per limiter)
	authRL := middleware.RateLimitByIP(5, time.Minute)         // login: 5/min per IP
	createRL := middleware.RateLimitByAccount(10, time.Hour)   // resource creation: 10/hr per account
	triggerRL := middleware.RateLimitByAccount(3, time.Minute) // CI trigger: 3/min per account

	// Auth (public) — rate-limited by IP (no account available yet)
	auth := r.Group("/auth")
	{
		auth.POST("/github/callback", authRL, authH.GitHubCallback)
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
			admin.GET("/users", allowlistH.ListUsers)
			admin.GET("/users/:username", allowlistH.GetUser)
		}

		// Projects
		api.GET("/projects", projectH.List)
		api.POST("/projects", createRL, projectH.Create)

		// All project-specific routes require project ownership
		proj := api.Group("/projects/:project", middleware.ProjectAccess(gitopsClient, cfg.JWT.AdminIDs))
		{
			proj.GET("", projectH.Get)
			proj.DELETE("", projectH.Delete)
			proj.GET("/dashboard", projectH.Dashboard)

			// Members
			proj.GET("/members", projectH.ListMembers)
			proj.POST("/members", projectH.AddMember)
			proj.DELETE("/members/:username", projectH.RemoveMember)

			// Apps
			apps := proj.Group("/apps")
			{
				apps.GET("", appH.List)
				apps.POST("", createRL, appH.Create)

				// App-specific routes: verify :app belongs to this project before dispatching
				app := apps.Group("/:app", middleware.AppAccess())
				{
					app.GET("", appH.Get)
					app.PUT("", appH.Update)
					app.DELETE("", appH.Delete)
					app.GET("/status", appH.Status)
					app.POST("/trigger", triggerRL, appH.Trigger)
					app.PATCH("/scale", appH.Scale)
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
				addons.POST("", createRL, addonH.Create)
				addons.PUT("/:addon", addonH.Update)
				addons.DELETE("/:addon", addonH.Delete)
				addons.GET("/:addon/connection", addonH.Connection)
			}
		}
	}

	srv := &http.Server{
		Addr:    ":" + cfg.Server.Port,
		Handler: r,
	}

	go func() {
		log.Printf("starting xquare-server on :%s", cfg.Server.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Printf("shutting down server...")

	// Allow 30s for in-flight requests (log streams, gitops writes) to complete.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("server forced shutdown: %v", err)
	}
	log.Printf("server stopped")
}
