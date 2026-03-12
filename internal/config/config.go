package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Server ServerConfig
	JWT    JWTConfig
	GitHub GitHubConfig
	GitOps GitOpsConfig
	Vault  VaultConfig
	K8s    K8sConfig
}

type ServerConfig struct {
	Port string
}

type JWTConfig struct {
	Secret     string
	AccessExp  int      // hours
	RefreshExp int      // days
	AdminUsers []string // GitHub usernames with full access (comma-separated ADMIN_GITHUB_USERS)
}

type GitHubConfig struct {
	ClientID     string
	ClientSecret string
	AppID        string
	PrivateKey   string // PEM content
}

type GitOpsConfig struct {
	Token     string
	RepoOwner string
	RepoName  string
	Branch    string
}

type VaultConfig struct {
	Address string
	Token   string
	Mount   string // "xquare-kv"
}

type K8sConfig struct {
	ConfigPath string // path to kubeconfig, empty = in-cluster
}

func Load() (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{
			Port: getEnv("PORT", "8080"),
		},
		JWT: JWTConfig{
			Secret:     requireEnv("JWT_SECRET"),
			AccessExp:  24,
			RefreshExp: 30,
			AdminUsers: parseList(os.Getenv("ADMIN_GITHUB_USERS")),
		},
		GitHub: GitHubConfig{
			ClientID:     requireEnv("GITHUB_CLIENT_ID"),
			ClientSecret: requireEnv("GITHUB_CLIENT_SECRET"),
			AppID:        getEnv("GITHUB_APP_ID", "1172114"),
			PrivateKey:   os.Getenv("GITHUB_APP_PRIVATE_KEY"),
		},
		GitOps: GitOpsConfig{
			Token:     requireEnv("GITOPS_TOKEN"),
			RepoOwner: getEnv("GITOPS_REPO_OWNER", "team-xquare"),
			RepoName:  getEnv("GITOPS_REPO_NAME", "xquare-onpremise-project-gitops-repo"),
			Branch:    getEnv("GITOPS_BRANCH", "main"),
		},
		Vault: VaultConfig{
			Address: requireEnv("VAULT_ADDR"),
			Token:   requireEnv("VAULT_TOKEN"),
			Mount:   getEnv("VAULT_MOUNT", "xquare-kv"),
		},
		K8s: K8sConfig{
			ConfigPath: os.Getenv("KUBECONFIG"),
		},
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "required env var %s is not set\n", key)
		os.Exit(1)
	}
	return v
}
