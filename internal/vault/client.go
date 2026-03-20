package vault

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/team-xquare/xquare-server/internal/config"
	"github.com/team-xquare/xquare-server/internal/domain"
)

// ErrEnvTooLarge is returned when total env var size exceeds MaxEnvTotalBytes.
var ErrEnvTooLarge = errors.New("env vars exceed 1 MiB total size limit")

// MaxEnvTotalBytes is the maximum combined size (keys + values) for one app's env vars.
const MaxEnvTotalBytes = 1 * 1024 * 1024 // 1 MiB

func totalEnvSize(envs map[string]string) int {
	n := 0
	for k, v := range envs {
		n += len(k) + len(v)
	}
	return n
}

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
	if totalEnvSize(vars) > MaxEnvTotalBytes {
		return ErrEnvTooLarge
	}
	return c.setEnvLocked(project, app, vars)
}

// PatchEnv merges vars into existing Vault KV v1.
// The size check is performed INSIDE the mutex to prevent TOCTOU: two concurrent
// PATCH requests could both pass a handler-level size check (both reading stale
// state), then write combined values that exceed the limit.
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
	if totalEnvSize(existing) > MaxEnvTotalBytes {
		return ErrEnvTooLarge
	}
	return c.setEnvLocked(project, app, existing)
}

// DeleteEnvKey removes a single key from Vault KV v1.
// Returns ErrEnvKeyNotFound if the key does not exist.
// Uses a mutex to prevent TOCTOU race conditions on concurrent DELETE requests.
func (c *Client) DeleteEnvKey(project, app, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	existing, err := c.GetEnv(project, app)
	if err != nil {
		return err
	}
	if _, ok := existing[key]; !ok {
		return ErrEnvKeyNotFound
	}
	delete(existing, key)
	return c.setEnvLocked(project, app, existing)
}

// ErrEnvKeyNotFound is returned when the requested env key does not exist.
var ErrEnvKeyNotFound = errors.New("env key not found")

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
