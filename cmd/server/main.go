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

	// Init handlers
	authH := handler.NewAuthHandler(githubClient, cfg)
	projectH := handler.NewProjectHandler(gitopsClient, vaultClient)
	appH := handler.NewAppHandler(gitopsClient, k8sClient, vaultClient, githubClient)
	envH := handler.NewEnvHandler(vaultClient)
	addonH := handler.NewAddonHandler(gitopsClient, k8sClient)
	logsH := handler.NewLogsHandler(k8sClient)

	r := gin.Default()

	// CORS
	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-GitHub-Token")
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
	api := r.Group("/", middleware.Auth(cfg.JWT.Secret))
	{
		// Projects
		projects := api.Group("/projects")
		{
			projects.GET("", projectH.List)
			projects.POST("", projectH.Create)
			projects.GET("/:project", projectH.Get)
			projects.DELETE("/:project", projectH.Delete)

			// Apps
			apps := projects.Group("/:project/apps")
			{
				apps.GET("", appH.List)
				apps.POST("", appH.Create)
				apps.GET("/:app", appH.Get)
				apps.PUT("/:app", appH.Update)
				apps.DELETE("/:app", appH.Delete)
				apps.GET("/:app/status", appH.Status)
				apps.POST("/:app/redeploy", appH.Redeploy)
				apps.GET("/:app/logs", logsH.Stream)
			}

			// Env
			env := projects.Group("/:project/apps/:app/env")
			{
				env.GET("", envH.Get)
				env.PUT("", envH.Set)
				env.PATCH("", envH.Patch)
				env.DELETE("/:key", envH.DeleteKey)
			}

			// Addons
			addons := projects.Group("/:project/addons")
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
