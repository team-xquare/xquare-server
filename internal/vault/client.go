package vault

import (
	"fmt"
	"strings"
	"sync"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/team-xquare/xquare-server/internal/config"
	"github.com/team-xquare/xquare-server/internal/domain"
)

type Client struct {
	client *vaultapi.Client
	mount  string
	mu     sync.Mutex // guards read-modify-write operations
}

func NewClient(cfg *config.VaultConfig) (*Client, error) {
	vcfg := vaultapi.DefaultConfig()
	vcfg.Address = cfg.Address
	c, err := vaultapi.NewClient(vcfg)
	if err != nil {
		return nil, fmt.Errorf("vault client: %w", err)
	}
	c.SetToken(cfg.Token)
	return &Client{client: c, mount: cfg.Mount}, nil
}

// GetEnv reads env vars for an app from Vault KV v1
func (c *Client) GetEnv(project, app string) (map[string]string, error) {
	path := fmt.Sprintf("%s/%s", c.mount, domain.VaultPath(project, app))
	secret, err := c.client.Logical().Read(path)
	if err != nil {
		return nil, fmt.Errorf("vault read: %w", err)
	}
	if secret == nil || secret.Data == nil {
		return map[string]string{}, nil
	}
	result := make(map[string]string)
	for k, v := range secret.Data {
		if strings.HasPrefix(k, "_") {
			continue // skip _raw, _init internal keys
		}
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	return result, nil
}

// setEnvLocked writes env vars without acquiring the mutex (caller must hold c.mu).
func (c *Client) setEnvLocked(project, app string, vars map[string]string) error {
	path := fmt.Sprintf("%s/%s", c.mount, domain.VaultPath(project, app))
	data := make(map[string]interface{}, len(vars))
	for k, v := range vars {
		data[k] = v
	}
	_, err := c.client.Logical().Write(path, data)
	if err != nil {
		return fmt.Errorf("vault write: %w", err)
	}
	return nil
}

// SetEnv writes env vars (full replace) to Vault KV v1.
// Acquires the mutex so concurrent PUT /env requests don't overwrite each other.
func (c *Client) SetEnv(project, app string, vars map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setEnvLocked(project, app, vars)
}

// PatchEnv merges vars into existing Vault KV v1.
// Uses a mutex to prevent TOCTOU race conditions on concurrent PATCH requests.
func (c *Client) PatchEnv(project, app string, vars map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	existing, err := c.GetEnv(project, app)
	if err != nil {
		return err
	}
	for k, v := range vars {
		existing[k] = v
	}
	return c.setEnvLocked(project, app, existing)
}

// DeleteEnvKey removes a single key from Vault KV v1.
// Uses a mutex to prevent TOCTOU race conditions on concurrent DELETE requests.
func (c *Client) DeleteEnvKey(project, app, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	existing, err := c.GetEnv(project, app)
	if err != nil {
		return err
	}
	delete(existing, key)
	return c.setEnvLocked(project, app, existing)
}

// InitEnv creates an empty secret at the Vault path (needed before VaultStaticSecret syncs)
func (c *Client) InitEnv(project, app string) error {
	path := fmt.Sprintf("%s/%s", c.mount, domain.VaultPath(project, app))
	// Check if already exists
	secret, _ := c.client.Logical().Read(path)
	if secret != nil && secret.Data != nil {
		return nil // already initialized
	}
	_, err := c.client.Logical().Write(path, map[string]interface{}{
		"_init": "1",
	})
	return err
}

// DeleteEnv removes the entire secret path for an app
func (c *Client) DeleteEnv(project, app string) error {
	path := fmt.Sprintf("%s/%s", c.mount, domain.VaultPath(project, app))
	_, err := c.client.Logical().Delete(path)
	return err
}
