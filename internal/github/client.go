package github

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/team-xquare/xquare-server/internal/config"
)

const apiBase = "https://api.github.com"

type Client struct {
	appID      string
	appSlug    string
	privateKey *rsa.PrivateKey
	httpClient *http.Client
}

func NewClient(cfg *config.GitHubConfig) *Client {
	c := &Client{
		appID:      cfg.AppID,
		appSlug:    cfg.AppSlug,
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

func (c *Client) appJWT() (string, error) {
	if c.privateKey == nil {
		return "", fmt.Errorf("GitHub App private key not configured")
	}
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Issuer:    c.appID,
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(c.privateKey)
}

type User struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

// ExchangeCode exchanges a GitHub OAuth code for an access token.
func (c *Client) ExchangeCode(ctx context.Context, clientID, clientSecret, code string) (string, error) {
	payload := struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Code         string `json:"code"`
	}{ClientID: clientID, ClientSecret: clientSecret, Code: code}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal oauth request: %w", err)
	}
	body := string(b)
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

// GetUser fetches GitHub user info with an access token.
func (c *Client) GetUser(ctx context.Context, accessToken string) (*User, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", apiBase+"/user", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github /user returned %d", resp.StatusCode)
	}
	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}
	if user.ID <= 0 {
		return nil, fmt.Errorf("github /user returned invalid user ID")
	}
	return &user, nil
}

// GetUserByID fetches public GitHub user info by numeric ID.
// GitHub resolves numeric path segments to user IDs (usernames are never purely numeric).
func (c *Client) GetUserByID(ctx context.Context, id int64) (*User, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/user/%d", apiBase, id), nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("github user id=%d not found", id)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github /users/%d returned %d", id, resp.StatusCode)
	}
	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByUsername fetches public GitHub user info by username (no auth required).
func (c *Client) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", apiBase+"/users/"+url.PathEscape(username), nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("github user %q not found", username)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github /users/%s returned %d", username, resp.StatusCode)
	}
	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}
	if user.ID <= 0 {
		return nil, fmt.Errorf("github returned invalid user ID for %q", username)
	}
	return &user, nil
}

// ErrAppNotInstalled is returned when the GitHub App is not installed on the target repo.
type ErrAppNotInstalled struct {
	Owner      string
	Repo       string
	InstallURL string
}

func (e *ErrAppNotInstalled) Error() string {
	return fmt.Sprintf("GitHub App not installed on %s/%s\n\nInstall it at: %s", e.Owner, e.Repo, e.InstallURL)
}

// GetBranchSHA returns the latest commit SHA for a branch.
func (c *Client) GetBranchSHA(ctx context.Context, owner, repo, branch string) (string, error) {
	apiURL := fmt.Sprintf("%s/repos/%s/%s/branches/%s", apiBase, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch))
	req, _ := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github branches/%s returned %d", branch, resp.StatusCode)
	}
	var result struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Commit.SHA, nil
}

// getInstallationToken exchanges a GitHub App JWT for an installation access token.
func (c *Client) getInstallationToken(ctx context.Context, installationID string) (string, error) {
	appToken, err := c.appJWT()
	if err != nil {
		return "", fmt.Errorf("app jwt: %w", err)
	}
	apiURL := fmt.Sprintf("%s/app/installations/%s/access_tokens", apiBase, url.PathEscape(installationID))
	req, _ := http.NewRequestWithContext(ctx, "POST", apiURL, nil)
	req.Header.Set("Authorization", "Bearer "+appToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("installation token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("installation token: unexpected status %d", resp.StatusCode)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("installation token decode: %w", err)
	}
	return result.Token, nil
}

// VerifyRepoAccess checks that the given GitHub user is a member of the repo's owner
// (org or personal account). For personal repos, the owner must match the username.
// For org repos, the user must be a member of the org.
// This prevents a user from registering another team's repo as their app target.
func (c *Client) VerifyRepoAccess(ctx context.Context, installationID, owner, repo, username string) error {
	// Personal repo: owner is the user themselves
	if strings.EqualFold(owner, username) {
		return nil
	}
	// Org repo: verify org membership using the installation token
	token, err := c.getInstallationToken(ctx, installationID)
	if err != nil {
		return fmt.Errorf("cannot verify org membership for %s: %w", owner, err)
	}
	apiURL := fmt.Sprintf("%s/orgs/%s/members/%s", apiBase, url.PathEscape(owner), url.PathEscape(username))
	req, _ := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("verify org membership: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil // user is an org member
	}
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("user %q is not a member of GitHub org %q — cannot use this repository", username, owner)
	}
	return fmt.Errorf("org membership check returned unexpected status %d for %s/%s", resp.StatusCode, owner, repo)
}

// GetRepoInstallationID returns the GitHub App installation ID for a repo.
// Returns ErrAppNotInstalled if the app is not installed.
func (c *Client) GetRepoInstallationID(ctx context.Context, owner, repo string) (string, error) {
	appToken, err := c.appJWT()
	if err != nil {
		return "", fmt.Errorf("app jwt: %w", err)
	}

	apiURL := fmt.Sprintf("%s/repos/%s/%s/installation", apiBase, url.PathEscape(owner), url.PathEscape(repo))
	req, _ := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	req.Header.Set("Authorization", "Bearer "+appToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		// Three possible cases:
		// 1. Repo is public and doesn't exist → "repo not found"
		// 2. Repo is private, App not installed on owner → install URL
		// 3. Repo is public/private, App installed on owner but not on this specific repo → "repo not found"
		//
		// Strategy:
		// - If unauthenticated check shows repo exists publicly → App installed but not on this repo? shouldn't happen
		//   (if repo exists publicly and App is installed somewhere, /installation would return 200)
		//   Actually this means App is NOT installed on owner at all.
		// - Check App installation on owner with App JWT:
		//   - If owner has App installed → repo truly doesn't exist (or was deleted)
		//   - If owner doesn't have App → could be private repo → return install URL
		if c.repoExists(ctx, owner, repo) || !c.ownerHasApp(ctx, appToken, owner) {
			// Public repo exists but app not installed on it, OR owner doesn't have app at all
			// If owner doesn't have app → private repo possible → install URL
			if !c.ownerHasApp(ctx, appToken, owner) {
				installURL := c.buildInstallURL(ctx, owner)
				return "", &ErrAppNotInstalled{Owner: owner, Repo: repo, InstallURL: installURL}
			}
		}
		return "", fmt.Errorf("repository %s/%s not found", owner, repo)
	}

	var result struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", result.ID), nil
}

// repoExists checks whether a public repository exists on GitHub (unauthenticated).
// Returns false only on definitive 404; private repos also return 404 here.
func (c *Client) repoExists(ctx context.Context, owner, repo string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/%s", apiBase, url.PathEscape(owner), url.PathEscape(repo)), nil)
	if err != nil {
		return true
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return true
	}
	resp.Body.Close()
	return resp.StatusCode != 404
}

// ownerHasApp checks whether the GitHub App is installed on the given owner (user or org)
// using the App JWT. Returns true if installed, false if not installed or on error.
func (c *Client) ownerHasApp(ctx context.Context, appToken, owner string) bool {
	// Try org endpoint first, fall back to user endpoint
	for _, path := range []string{"/orgs/" + url.PathEscape(owner) + "/installation", "/users/" + url.PathEscape(owner) + "/installation"} {
		req, err := http.NewRequestWithContext(ctx, "GET", apiBase+path, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+appToken)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			return true
		}
	}
	return false
}

// ownerExists checks whether a GitHub user or org exists (unauthenticated).
func (c *Client) ownerExists(ctx context.Context, owner string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/users/%s", apiBase, url.PathEscape(owner)), nil)
	if err != nil {
		return true
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return true
	}
	resp.Body.Close()
	return resp.StatusCode != 404
}

// buildInstallURL returns a targeted GitHub App installation URL with the owner's
// target_id pre-filled so the user lands directly on the correct org/user page.
// Falls back to the generic installation URL if the owner lookup fails.
func (c *Client) buildInstallURL(ctx context.Context, owner string) string {
	req, err := http.NewRequestWithContext(ctx, "GET", apiBase+"/users/"+url.PathEscape(owner), nil)
	if err != nil {
		return fmt.Sprintf("https://github.com/apps/%s/installations/new", c.appSlug)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.httpClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return fmt.Sprintf("https://github.com/apps/%s/installations/new", c.appSlug)
	}
	defer resp.Body.Close()

	var user struct {
		ID   int64  `json:"id"`
		Type string `json:"type"` // "User" or "Organization"
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil || user.ID == 0 {
		return fmt.Sprintf("https://github.com/apps/%s/installations/new", c.appSlug)
	}

	return fmt.Sprintf("https://github.com/apps/%s/installations/new/permissions?target_id=%d",
		c.appSlug, user.ID)
}

