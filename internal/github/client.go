package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/team-xquare/xquare-server/internal/config"
)

const apiBase = "https://api.github.com"

type Client struct {
	token      string
	httpClient *http.Client
}

func NewClient(cfg *config.GitHubConfig) *Client {
	return &Client{
		token:      cfg.ClientSecret, // use as PAT fallback; real flow uses App JWT
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
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
