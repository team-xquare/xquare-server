package main

import (
	"log"
	"net/http"

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
	projectH := handler.NewProjectHandler(gitopsClient, vaultClient, githubClient, cfg.JWT.AdminIDs)
	appH := handler.NewAppHandler(gitopsClient, k8sClient, vaultClient, wfClient, githubClient)
	envH := handler.NewEnvHandler(vaultClient)
	addonH := handler.NewAddonHandler(gitopsClient, k8sClient)
	logsH := handler.NewLogsHandler(k8sClient)
	buildsH := handler.NewBuildsHandler(wfClient)
	allowlistH := handler.NewAllowlistHandler(gitopsClient, githubClient, cfg.JWT.AdminIDs)

	r := gin.Default()

	// CORS
	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
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

	// Auth (public)
	auth := r.Group("/auth")
	{
		auth.POST("/github/callback", authH.GitHubCallback)
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
				apps.GET("/:app", appH.Get)
				apps.PUT("/:app", appH.Update)
				apps.DELETE("/:app", appH.Delete)
				apps.GET("/:app/status", appH.Status)
				apps.POST("/:app/redeploy", appH.Redeploy)
				apps.GET("/:app/logs", logsH.Stream)
				apps.GET("/:app/builds", buildsH.List)
				apps.GET("/:app/builds/:workflow/logs", buildsH.StreamLogs)
				apps.GET("/:app/tunnel", appH.Tunnel)
			}

			// Env
			env := proj.Group("/apps/:app/env")
			{
				env.GET("", envH.Get)
				env.PUT("", envH.Set)
				env.PATCH("", envH.Patch)
				env.DELETE("/:key", envH.DeleteKey)
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
