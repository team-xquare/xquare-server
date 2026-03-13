package handler

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/team-xquare/xquare-server/internal/api/middleware"
	"github.com/team-xquare/xquare-server/internal/config"
	"github.com/team-xquare/xquare-server/internal/github"
)

type allowlistChecker interface {
	AllowedUserIDs() (map[int64]struct{}, error)
}

type AuthHandler struct {
	gh        *github.Client
	cfg       *config.Config
	allowlist allowlistChecker
}

func NewAuthHandler(gh *github.Client, cfg *config.Config, al allowlistChecker) *AuthHandler {
	return &AuthHandler{gh: gh, cfg: cfg, allowlist: al}
}

// POST /auth/github/callback
func (h *AuthHandler) GitHubCallback(c *gin.Context) {
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	accessToken, err := h.gh.ExchangeCode(c.Request.Context(),
		h.cfg.GitHub.ClientID, h.cfg.GitHub.ClientSecret, req.Code)
	if err != nil {
		log.Printf("github oauth exchange failed: %v", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication failed"})
		return
	}

	user, err := h.gh.GetUser(c.Request.Context(), accessToken)
	if err != nil {
		log.Printf("github get user failed: %v", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication failed"})
		return
	}

	// Check allowlist before issuing JWT — deny non-registered users at login time.
	if h.allowlist != nil {
		allowed, err := h.allowlist.AllowedUserIDs()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify access"})
			return
		}
		if allowed != nil {
			if _, ok := allowed[user.ID]; !ok {
				c.JSON(http.StatusForbidden, gin.H{
					"error": "your GitHub account is not authorized to access this platform",
					"code":  "not_authorized",
				})
				return
			}
		}
	}

	token, err := h.issueJWT(user.ID, user.Login)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "jwt issue failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token":      token,
		"github_id":  user.ID,
		"username":   user.Login,
		"avatar_url": user.AvatarURL,
	})
}

// GET /auth/me
func (h *AuthHandler) Me(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"github_id": c.GetInt64("githubId"),
		"username":  c.GetString("username"),
	})
}

func (h *AuthHandler) issueJWT(githubID int64, username string) (string, error) {
	claims := middleware.Claims{
		GithubID: githubID,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(h.cfg.JWT.AccessExp) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(h.cfg.JWT.Secret))
}
