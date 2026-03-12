package config

import (
	"fmt"
	"os"
	"strconv"
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
	AccessExp  int     // hours
	RefreshExp int     // days
	AdminIDs   []int64 // GitHub user IDs with full access (comma-separated ADMIN_GITHUB_IDS)
}

type GitHubConfig struct {
	ClientID     string
	ClientSecret string
	AppID        string
	AppSlug      string // used for install URL: https://github.com/apps/{slug}/installations/new
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
	Token      string // bearer token; if set, overrides ConfigPath/in-cluster
	Host       string // API server host when Token is used; defaults to in-cluster
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
			AdminIDs:   parseIDList(os.Getenv("ADMIN_GITHUB_IDS")),
		},
		GitHub: GitHubConfig{
			ClientID:     requireEnv("GITHUB_CLIENT_ID"),
			ClientSecret: requireEnv("GITHUB_CLIENT_SECRET"),
			AppID:        getEnv("GITHUB_APP_ID", "1172114"),
			AppSlug:      getEnv("GITHUB_APP_SLUG", "xquare-infrastructure"),
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
			Token:      os.Getenv("K8S_TOKEN"),
			Host:       getEnv("K8S_HOST", "https://kubernetes.default.svc.cluster.local"),
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

// parseIDList parses a comma-separated list of GitHub user IDs (int64).
func parseIDList(s string) []int64 {
	var out []int64
	for _, v := range strings.Split(s, ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: invalid admin github ID %q: %v\n", v, err)
			continue
		}
		out = append(out, id)
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
