package gitops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/team-xquare/xquare-server/internal/config"
	"github.com/team-xquare/xquare-server/internal/domain"
	"gopkg.in/yaml.v3"
)

const pullCacheTTL = 5 * time.Second

type Client struct {
	cfg      *config.GitOpsConfig
	repoURL  string
	repoDir  string
	mu       sync.Mutex
	lastPull time.Time
}

func NewClient(cfg *config.GitOpsConfig) *Client {
	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", cfg.RepoOwner, cfg.RepoName)
	repoDir := filepath.Join(os.TempDir(), "xquare-gitops")
	return &Client{cfg: cfg, repoURL: repoURL, repoDir: repoDir}
}

func (c *Client) auth() *http.BasicAuth {
	return &http.BasicAuth{Username: "x-access-token", Password: c.cfg.Token}
}

// ensureRepo clones or pulls the repo (must be called with lock held).
// pull is skipped if within pullCacheTTL (read performance optimization).
func (c *Client) ensureRepo() (*git.Repository, error) {
	return c.ensureRepoFresh(false)
}

// ensureRepoFresh ignores the cache when forcePull=true (used before writes).
func (c *Client) ensureRepoFresh(forcePull bool) (*git.Repository, error) {
	repo, err := git.PlainOpen(c.repoDir)
	if err == git.ErrRepositoryNotExists {
		repo, err = git.PlainClone(c.repoDir, false, &git.CloneOptions{
			URL:  c.repoURL,
			Auth: c.auth(),
		})
		if err != nil {
			return nil, fmt.Errorf("clone: %w", err)
		}
		c.lastPull = time.Now()
		return repo, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}
	// skip pull if within TTL and not forced
	if !forcePull && time.Since(c.lastPull) < pullCacheTTL {
		return repo, nil
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, err
	}
	if err := wt.Pull(&git.PullOptions{Auth: c.auth(), Force: true}); err != nil && err != git.NoErrAlreadyUpToDate {
		return nil, fmt.Errorf("pull: %w", err)
	}
	c.lastPull = time.Now()
	return repo, nil
}

type allowedUsersFile struct {
	Users []int64 `yaml:"users"`
}

const maxAllowedUsersFileBytes = 1 * 1024 * 1024 // 1 MiB

func (c *Client) readAllowedUsers() (*allowedUsersFile, error) {
	path := filepath.Join(c.repoDir, "allowed-users.yaml")
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat allowed-users.yaml: %w", err)
	}
	if fi.Size() > maxAllowedUsersFileBytes {
		return nil, fmt.Errorf("allowed-users.yaml exceeds maximum size of %d bytes", maxAllowedUsersFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read allowed-users.yaml: %w", err)
	}
	var f allowedUsersFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse allowed-users.yaml: %w", err)
	}
	return &f, nil
}

func (c *Client) writeAllowedUsers(actor string, f *allowedUsersFile) error {
	repo, err := c.ensureRepoFresh(true)
	if err != nil {
		return err
	}
	path := filepath.Join(c.repoDir, "allowed-users.yaml")
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}
	wt, _ := repo.Worktree()
	if _, err := wt.Add("allowed-users.yaml"); err != nil {
		return err
	}
	return c.commit(repo, fmt.Sprintf("feat: update allowlist [actor: %s]", sanitizeCommitToken(actor)))
}

// AllowedUserIDs reads allowed-users.yaml and returns the set of allowed GitHub user IDs.
// Returns nil if the file does not exist; the middleware treats nil as DENY-ALL.
func (c *Client) AllowedUserIDs() (map[int64]struct{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.ensureRepo(); err != nil {
		return nil, err
	}
	f, err := c.readAllowedUsers()
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, nil // no file → middleware treats as DENY-ALL
	}
	set := make(map[int64]struct{}, len(f.Users))
	for _, id := range f.Users {
		set[id] = struct{}{}
	}
	return set, nil
}

// ListAllowedUsers returns the full list of allowed GitHub user IDs.
func (c *Client) ListAllowedUsers() ([]int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.ensureRepo(); err != nil {
		return nil, err
	}
	f, err := c.readAllowedUsers()
	if err != nil {
		return nil, err
	}
	if f == nil {
		return []int64{}, nil
	}
	return f.Users, nil
}

// AddAllowedUser adds a GitHub user ID to the allowlist. Errors if already present.
func (c *Client) AddAllowedUser(actor string, id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.ensureRepo(); err != nil {
		return err
	}
	f, err := c.readAllowedUsers()
	if err != nil {
		return err
	}
	if f == nil {
		f = &allowedUsersFile{}
	}
	for _, existing := range f.Users {
		if existing == id {
			return fmt.Errorf("user id=%d is already in the allowlist", id)
		}
	}
	f.Users = append(f.Users, id)
	return c.writeAllowedUsers(actor, f)
}

// RemoveAllowedUser removes a user from the allowlist by GitHub ID.
// Matching by immutable ID prevents confusion if a user renames their GitHub account.
func (c *Client) RemoveAllowedUser(actor string, githubID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.ensureRepo(); err != nil {
		return err
	}
	f, err := c.readAllowedUsers()
	if err != nil {
		return err
	}
	if f == nil {
		return fmt.Errorf("user (id=%d) not found in allowlist", githubID)
	}
	filtered := f.Users[:0]
	found := false
	for _, id := range f.Users {
		if id == githubID {
			found = true
			continue
		}
		filtered = append(filtered, id)
	}
	if !found {
		return fmt.Errorf("user (id=%d) not found in allowlist", githubID)
	}
	f.Users = filtered
	return c.writeAllowedUsers(actor, f)
}

// ListProjects returns all project names from projects/*.yaml
func (c *Client) ListProjects() ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.ensureRepo(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(c.repoDir, "projects"))
	if err != nil {
		return nil, err
	}
	var projects []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			projects = append(projects, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	return projects, nil
}

// GetProject reads and parses projects/{name}.yaml
func (c *Client) GetProject(name string) (*domain.Project, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.ensureRepo(); err != nil {
		return nil, err
	}
	return c.readProject(name)
}

const maxProjectFileBytes = 1 * 1024 * 1024 // 1 MiB

func (c *Client) readProject(name string) (*domain.Project, error) {
	path := filepath.Join(c.repoDir, "projects", name+".yaml")
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("project %q not found", name)
		}
		return nil, err
	}
	if fi.Size() > maxProjectFileBytes {
		return nil, fmt.Errorf("project file exceeds maximum size of %d bytes", maxProjectFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p domain.Project
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	return &p, nil
}

// CreateProject creates a new empty projects/{name}.yaml with the creator as first owner.
func (c *Client) CreateProject(name string, ownerID int64, actor string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	repo, err := c.ensureRepoFresh(true)
	if err != nil {
		return err
	}
	path := filepath.Join(c.repoDir, "projects", name+".yaml")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("project %q already exists", name)
	}
	p := domain.Project{
		Owners:       []int64{ownerID},
		Applications: []domain.Application{},
		Addons:       []domain.Addon{},
	}
	return c.writeAndPush(repo, name, &p, fmt.Sprintf("feat: create project %s [actor: %s]", name, sanitizeCommitToken(actor)))
}

// AddProjectMember adds a GitHub user ID as a project owner.
func (c *Client) AddProjectMember(project string, ownerID int64, actor string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.retryUpdate(project, func(p *domain.Project) error {
		for _, id := range p.Owners {
			if id == ownerID {
				return nil // already a member
			}
		}
		p.Owners = append(p.Owners, ownerID)
		return nil
	}, fmt.Sprintf("feat: add member id=%d to project %s [actor: %s]", ownerID, project, sanitizeCommitToken(actor)))
}

// RemoveProjectMember removes a project owner by GitHub ID.
func (c *Client) RemoveProjectMember(project string, githubID int64, actor string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.retryUpdate(project, func(p *domain.Project) error {
		owners := p.Owners[:0]
		for _, id := range p.Owners {
			if id != githubID {
				owners = append(owners, id)
			}
		}
		p.Owners = owners
		return nil
	}, fmt.Sprintf("feat: remove member id=%d from project %s [actor: %s]", githubID, project, sanitizeCommitToken(actor)))
}

// DeleteProject removes projects/{name}.yaml and pushes
func (c *Client) DeleteProject(name, actor string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	repo, err := c.ensureRepoFresh(true)
	if err != nil {
		return err
	}
	wt, _ := repo.Worktree()
	relPath := filepath.Join("projects", name+".yaml")
	if _, err := wt.Remove(relPath); err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	return c.commit(repo, fmt.Sprintf("feat: delete project %s [actor: %s]", name, sanitizeCommitToken(actor)))
}

// AddApplication adds an application to projects/{project}.yaml and pushes
func (c *Client) AddApplication(project string, app domain.Application, actor string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.retryUpdate(project, func(p *domain.Project) error {
		for _, a := range p.Applications {
			if a.Name == app.Name {
				return fmt.Errorf("application %q already exists in project %q", app.Name, project)
			}
		}
		p.Applications = append(p.Applications, app)
		return nil
	}, fmt.Sprintf("feat: add application %s to %s [actor: %s]", app.Name, project, sanitizeCommitToken(actor)))
}

// UpdateApplication updates an existing application
func (c *Client) UpdateApplication(project string, app domain.Application, actor string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.retryUpdate(project, func(p *domain.Project) error {
		for i, a := range p.Applications {
			if a.Name == app.Name {
				// preserve hash
				app.GitHub.Hash = a.GitHub.Hash
				p.Applications[i] = app
				return nil
			}
		}
		return fmt.Errorf("application %q not found in project %q", app.Name, project)
	}, fmt.Sprintf("feat: update application %s in %s [actor: %s]", app.Name, project, sanitizeCommitToken(actor)))
}

// DeleteApplication removes an application from the project
func (c *Client) DeleteApplication(project, appName, actor string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.retryUpdate(project, func(p *domain.Project) error {
		for i, a := range p.Applications {
			if a.Name == appName {
				p.Applications = append(p.Applications[:i], p.Applications[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("application %q not found", appName)
	}, fmt.Sprintf("feat: delete application %s from %s [actor: %s]", appName, project, sanitizeCommitToken(actor)))
}

// AddAddon adds an addon to the project
func (c *Client) AddAddon(project string, addon domain.Addon, actor string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.retryUpdate(project, func(p *domain.Project) error {
		for _, a := range p.Addons {
			if a.Name == addon.Name {
				return fmt.Errorf("addon %q already exists", addon.Name)
			}
		}
		p.Addons = append(p.Addons, addon)
		return nil
	}, fmt.Sprintf("feat: add addon %s to %s [actor: %s]", addon.Name, project, sanitizeCommitToken(actor)))
}

// UpdateAddon updates mutable fields (buckets) of an existing addon
func (c *Client) UpdateAddon(project, addonName string, buckets []domain.AddonBucket, actor string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.retryUpdate(project, func(p *domain.Project) error {
		for i, a := range p.Addons {
			if a.Name == addonName {
				p.Addons[i].Buckets = buckets
				return nil
			}
		}
		return fmt.Errorf("addon %q not found", addonName)
	}, fmt.Sprintf("feat: update addon %s in %s [actor: %s]", addonName, project, sanitizeCommitToken(actor)))
}

// DeleteAddon removes an addon from the project
func (c *Client) DeleteAddon(project, addonName, actor string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.retryUpdate(project, func(p *domain.Project) error {
		for i, a := range p.Addons {
			if a.Name == addonName {
				p.Addons = append(p.Addons[:i], p.Addons[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("addon %q not found", addonName)
	}, fmt.Sprintf("feat: delete addon %s from %s [actor: %s]", addonName, project, sanitizeCommitToken(actor)))
}

// CheckDomainConflict checks if a domain is already used across all projects
func (c *Client) CheckDomainConflict(excludeProject, excludeApp string, domains []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.ensureRepo(); err != nil {
		return err
	}
	domainSet := make(map[string]bool)
	for _, d := range domains {
		// strip path prefix
		host := strings.SplitN(d, "/", 2)[0]
		domainSet[host] = true
	}
	entries, _ := os.ReadDir(filepath.Join(c.repoDir, "projects"))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		projName := strings.TrimSuffix(e.Name(), ".yaml")
		p, err := c.readProject(projName)
		if err != nil {
			continue
		}
		for _, app := range p.Applications {
			if projName == excludeProject && app.Name == excludeApp {
				continue
			}
			for _, ep := range app.Endpoints {
				for _, route := range ep.Routes {
					host := strings.SplitN(route, "/", 2)[0]
					if domainSet[host] {
						return fmt.Errorf("domain %q is already used by %s/%s", host, projName, app.Name)
					}
				}
			}
		}
	}
	return nil
}

// retryUpdate reads, mutates, writes, and pushes with retry on conflict
func (c *Client) retryUpdate(project string, mutate func(*domain.Project) error, commitMsg string) error {
	repo, err := c.ensureRepoFresh(true)
	if err != nil {
		return err
	}
	for i := 0; i < 3; i++ {
		p, err := c.readProject(project)
		if err != nil {
			return err
		}
		if err := mutate(p); err != nil {
			return err
		}
		if err := c.writeAndPush(repo, project, p, commitMsg); err != nil {
			if strings.Contains(err.Error(), "non-fast-forward") || strings.Contains(err.Error(), "conflict") {
				wt, _ := repo.Worktree()
				_ = wt.Pull(&git.PullOptions{Auth: c.auth(), Force: true})
				time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("failed after 3 retries due to git conflicts")
}

func (c *Client) writeAndPush(repo *git.Repository, project string, p *domain.Project, commitMsg string) error {
	data, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	path := filepath.Join(c.repoDir, "projects", project+".yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}
	wt, _ := repo.Worktree()
	if _, err := wt.Add(filepath.Join("projects", project+".yaml")); err != nil {
		return err
	}
	return c.commit(repo, commitMsg)
}

// sanitizeCommitToken strips newlines and control characters from user-supplied
// strings embedded in commit messages to prevent log injection.
func sanitizeCommitToken(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r < 0x20 {
			return '_'
		}
		return r
	}, s)
}

func (c *Client) commit(repo *git.Repository, msg string) error {
	wt, _ := repo.Worktree()

	// Save HEAD so we can reset if push fails, leaving a local-only commit that
	// would prevent the next git pull from succeeding (diverged history).
	var preCommitHead plumbing.Hash
	if ref, err := repo.Head(); err == nil {
		preCommitHead = ref.Hash()
	}

	_, err := wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "xquare-server",
			Email: "xquare@dsmhs.kr",
			When:  time.Now(),
		},
	})
	if err != nil {
		if strings.Contains(err.Error(), "clean working tree") || strings.Contains(err.Error(), "empty commit") {
			return nil // already up to date, no-op
		}
		return fmt.Errorf("commit: %w", err)
	}
	if err := repo.Push(&git.PushOptions{Auth: c.auth()}); err != nil && err != git.NoErrAlreadyUpToDate {
		// Roll back the local commit so the next pull starts from a clean state.
		// Without this, a diverged local commit would cause the retry pull to fail.
		if !preCommitHead.IsZero() {
			_ = wt.Reset(&git.ResetOptions{Commit: preCommitHead, Mode: git.HardReset})
		}
		return fmt.Errorf("push: %w", err)
	}
	return nil
}
