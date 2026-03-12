package github

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/team-xquare/xquare-server/internal/config"
)

const apiBase = "https://api.github.com"

type Client struct {
	appID      string
	privateKey *rsa.PrivateKey // GitHub App private key for installation tokens
	httpClient *http.Client
}

func NewClient(cfg *config.GitHubConfig) *Client {
	c := &Client{
		appID:      cfg.AppID,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
	if cfg.PrivateKey != "" {
		if key, err := parseRSAKey(cfg.PrivateKey); err == nil {
			c.privateKey = key
		}
	}
	return c
}

func parseRSAKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("invalid PEM block")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// try PKCS8
		k, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return nil, err
		}
		rsaKey, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("not an RSA key")
		}
		return rsaKey, nil
	}
	return key, nil
}

// appJWT generates a short-lived JWT for authenticating as the GitHub App.
func (c *Client) appJWT() (string, error) {
	if c.privateKey == nil {
		return "", fmt.Errorf("GitHub App private key not configured")
	}
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Issuer:    c.appID,
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)), // 60s clock drift buffer
		ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(c.privateKey)
}

// GetInstallationToken returns a short-lived installation access token.
func (c *Client) GetInstallationToken(ctx context.Context, installationID string) (string, error) {
	appToken, err := c.appJWT()
	if err != nil {
		return "", fmt.Errorf("app jwt: %w", err)
	}

	url := fmt.Sprintf("%s/app/installations/%s/access_tokens", apiBase, installationID)
	req, _ := http.NewRequestWithContext(ctx, "POST", url, nil)
	req.Header.Set("Authorization", "Bearer "+appToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Token string `json:"token"`
		Error string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Error != "" {
		return "", fmt.Errorf("github app token: %s", result.Error)
	}
	return result.Token, nil
}

type User struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	Email     string `json:"email"`
}

// ExchangeCode exchanges a GitHub OAuth code for an access token
func (c *Client) ExchangeCode(ctx context.Context, clientID, clientSecret, code string) (string, error) {
	body := fmt.Sprintf(`{"client_id":"%s","client_secret":"%s","code":"%s"}`, clientID, clientSecret, code)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Error != "" {
		return "", fmt.Errorf("github oauth error: %s", result.Error)
	}
	return result.AccessToken, nil
}

// GetUser fetches GitHub user info with an access token
func (c *Client) GetUser(ctx context.Context, accessToken string) (*User, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", apiBase+"/user", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}
	return &user, nil
}

// GetLatestCommitSHA fetches the latest commit SHA for a branch
func (c *Client) GetLatestCommitSHA(ctx context.Context, token, owner, repo, branch string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/commits/%s", apiBase, owner, repo, branch)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.SHA == "" {
		return "", fmt.Errorf("could not get SHA for %s/%s@%s", owner, repo, branch)
	}
	return result.SHA, nil
}
