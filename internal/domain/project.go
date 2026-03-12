package domain

import (
	"fmt"
	"strings"
)

// Owner represents a project owner identified by their immutable GitHub ID.
// Username is cached for display only and may be stale if the user renames.
type Owner struct {
	ID       int64  `yaml:"id" json:"id"`
	Username string `yaml:"username" json:"username"`
}

// Project represents the full projects/{name}.yaml
type Project struct {
	Owners       []Owner       `yaml:"owners,omitempty" json:"owners,omitempty"`
	Applications []Application `yaml:"applications" json:"applications"`
	Addons       []Addon       `yaml:"addons,omitempty" json:"addons,omitempty"`
}

// HasAccess returns true if the given GitHub ID is an owner of this project.
func (p *Project) HasAccess(githubID int64) bool {
	for _, o := range p.Owners {
		if o.ID == githubID {
			return true
		}
	}
	return false
}

// Application represents one entry in applications[]
type Application struct {
	Name                 string     `yaml:"name" json:"name"`
	DisableNetworkPolicy bool       `yaml:"disableNetworkPolicy,omitempty" json:"disableNetworkPolicy,omitempty"`
	GitHub               GitHub     `yaml:"github" json:"github"`
	Build                Build      `yaml:"build" json:"build"`
	Endpoints            []Endpoint `yaml:"endpoints,omitempty" json:"endpoints,omitempty"`
}

type GitHub struct {
	Owner          string   `yaml:"owner" json:"owner"`
	Repo           string   `yaml:"repo" json:"repo"`
	Branch         string   `yaml:"branch" json:"branch"`
	InstallationID string   `yaml:"installationId" json:"installationId"`
	Hash           string   `yaml:"hash,omitempty" json:"hash,omitempty"`
	TriggerPaths   []string `yaml:"triggerPaths,omitempty" json:"triggerPaths,omitempty"`
}

type Build struct {
	Gradle       *GradleBuild       `yaml:"gradle,omitempty" json:"gradle,omitempty"`
	NodeJS       *NodeJSBuild       `yaml:"nodejs,omitempty" json:"nodejs,omitempty"`
	React        *ReactBuild        `yaml:"react,omitempty" json:"react,omitempty"`
	Vite         *ViteBuild         `yaml:"vite,omitempty" json:"vite,omitempty"`
	Vue          *VueBuild          `yaml:"vue,omitempty" json:"vue,omitempty"`
	NextJS       *NextJSBuild       `yaml:"nextjs,omitempty" json:"nextjs,omitempty"`
	NextJSExport *NextJSExportBuild `yaml:"nextjs-export,omitempty" json:"nextjs-export,omitempty"`
	Go           *GoBuild           `yaml:"go,omitempty" json:"go,omitempty"`
	Rust         *RustBuild         `yaml:"rust,omitempty" json:"rust,omitempty"`
	Maven        *MavenBuild        `yaml:"maven,omitempty" json:"maven,omitempty"`
	Django       *DjangoBuild       `yaml:"django,omitempty" json:"django,omitempty"`
	Flask        *FlaskBuild        `yaml:"flask,omitempty" json:"flask,omitempty"`
	Docker       *DockerBuild       `yaml:"docker,omitempty" json:"docker,omitempty"`
}

type GradleBuild struct {
	JavaVersion   string `yaml:"javaVersion" json:"javaVersion"`
	JarOutputPath string `yaml:"jarOutputPath" json:"jarOutputPath"`
	BuildCommand  string `yaml:"buildCommand" json:"buildCommand"`
}

type NodeJSBuild struct {
	NodeVersion  string `yaml:"nodeVersion" json:"nodeVersion"`
	BuildCommand string `yaml:"buildCommand" json:"buildCommand"`
	StartCommand string `yaml:"startCommand" json:"startCommand"`
}

type ReactBuild struct {
	NodeVersion  string `yaml:"nodeVersion" json:"nodeVersion"`
	BuildCommand string `yaml:"buildCommand" json:"buildCommand"`
	DistPath     string `yaml:"distPath" json:"distPath"`
}

type ViteBuild struct {
	NodeVersion  string `yaml:"nodeVersion" json:"nodeVersion"`
	BuildCommand string `yaml:"buildCommand" json:"buildCommand"`
	DistPath     string `yaml:"distPath" json:"distPath"`
}

type VueBuild struct {
	NodeVersion  string `yaml:"nodeVersion" json:"nodeVersion"`
	BuildCommand string `yaml:"buildCommand" json:"buildCommand"`
	DistPath     string `yaml:"distPath" json:"distPath"`
}

type NextJSBuild struct {
	NodeVersion  string `yaml:"nodeVersion" json:"nodeVersion"`
	BuildCommand string `yaml:"buildCommand" json:"buildCommand"`
	StartCommand string `yaml:"startCommand" json:"startCommand"`
}

type NextJSExportBuild struct {
	NodeVersion  string `yaml:"nodeVersion" json:"nodeVersion"`
	BuildCommand string `yaml:"buildCommand" json:"buildCommand"`
	DistPath     string `yaml:"distPath" json:"distPath"`
}

type GoBuild struct {
	GoVersion    string `yaml:"goVersion" json:"goVersion"`
	BuildCommand string `yaml:"buildCommand" json:"buildCommand"`
	BinaryName   string `yaml:"binaryName" json:"binaryName"`
}

type RustBuild struct {
	RustVersion  string `yaml:"rustVersion" json:"rustVersion"`
	BuildCommand string `yaml:"buildCommand" json:"buildCommand"`
	BinaryName   string `yaml:"binaryName" json:"binaryName"`
}

type MavenBuild struct {
	JavaVersion   string `yaml:"javaVersion" json:"javaVersion"`
	BuildCommand  string `yaml:"buildCommand" json:"buildCommand"`
	JarOutputPath string `yaml:"jarOutputPath" json:"jarOutputPath"`
}

type DjangoBuild struct {
	PythonVersion string `yaml:"pythonVersion" json:"pythonVersion"`
	BuildCommand  string `yaml:"buildCommand" json:"buildCommand"`
	StartCommand  string `yaml:"startCommand" json:"startCommand"`
}

type FlaskBuild struct {
	PythonVersion string `yaml:"pythonVersion" json:"pythonVersion"`
	BuildCommand  string `yaml:"buildCommand" json:"buildCommand"`
	StartCommand  string `yaml:"startCommand" json:"startCommand"`
}

type DockerBuild struct {
	DockerfilePath string `yaml:"dockerfilePath" json:"dockerfilePath"`
	ContextPath    string `yaml:"contextPath" json:"contextPath"`
}

type Endpoint struct {
	Port   int      `yaml:"port" json:"port"`
	Routes []string `yaml:"routes,omitempty" json:"routes,omitempty"`
}

// Addon represents one entry in addons[]
type Addon struct {
	Name      string `yaml:"name" json:"name"`
	Type      string `yaml:"type" json:"type"`
	Storage   string `yaml:"storage" json:"storage"`
	Bootstrap string `yaml:"bootstrap,omitempty" json:"bootstrap,omitempty"`
}

// AddonPort returns the default port for an addon type
func AddonPort(addonType string) int {
	ports := map[string]int{
		"mysql":         3306,
		"postgresql":    5432,
		"redis":         6379,
		"mongodb":       27017,
		"kafka":         9092,
		"rabbitmq":      5672,
		"opensearch":    9200,
		"elasticsearch": 9200,
		"qdrant":        6333,
	}
	if p, ok := ports[addonType]; ok {
		return p
	}
	return 0
}

// Namespace returns the K8s namespace for a project
func Namespace(project string) string {
	return project + "-dsm-project"
}

// ImageName returns the Harbor image name for an app
func ImageName(project, app string) string {
	return "harbor-xquare-infra.dsmhs.kr/xquare/" + project + "-" + app
}

// VaultPath returns the Vault KV v1 path for an app's secrets
func VaultPath(project, app string) string {
	return project + "-" + app
}

// K8sSecretName returns the K8s secret name for an app
func K8sSecretName(project, app string) string {
	return project + "-" + app
}

// ValidBuildCommand returns an error if the command contains shell injection patterns.
// Build commands run inside CI containers, but we still block obvious exfiltration
// attempts (command substitution, null bytes).
func ValidBuildCommand(cmd string) error {
	if strings.ContainsRune(cmd, 0) {
		return fmt.Errorf("build command must not contain null bytes")
	}
	if strings.Contains(cmd, "`") {
		return fmt.Errorf("build command must not contain backticks (command substitution)")
	}
	if strings.Contains(cmd, "$(") {
		return fmt.Errorf("build command must not contain $() (command substitution)")
	}
	return nil
}

// ValidFilePath returns an error if the path is absolute or contains path traversal.
func ValidFilePath(path string) error {
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("path must be relative, not absolute")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("path must not contain path traversal (..)")
	}
	if strings.ContainsRune(path, 0) {
		return fmt.Errorf("path must not contain null bytes")
	}
	return nil
}

// ValidAddonType returns an error if the addon type is not in the allowlist.
var validAddonTypes = map[string]bool{
	"mysql":         true,
	"postgresql":    true,
	"redis":         true,
	"mongodb":       true,
	"kafka":         true,
	"rabbitmq":      true,
	"opensearch":    true,
	"elasticsearch": true,
	"qdrant":        true,
}

func ValidAddonType(t string) error {
	if !validAddonTypes[t] {
		return fmt.Errorf("unsupported addon type %q: must be one of mysql, postgresql, redis, mongodb, kafka, rabbitmq, opensearch, elasticsearch, qdrant", t)
	}
	return nil
}

// ValidEnvKey returns an error if the key is not safe for use as a Vault KV key.
// Blocks empty keys, keys starting with underscore (Vault internal), and non-printable chars.
func ValidEnvKey(key string) error {
	if key == "" {
		return fmt.Errorf("env key must not be empty")
	}
	if strings.HasPrefix(key, "_") {
		return fmt.Errorf("env key %q must not start with underscore (reserved for internal use)", key)
	}
	for _, r := range key {
		if r < 0x20 || r > 0x7e {
			return fmt.Errorf("env key %q contains non-printable or non-ASCII character", key)
		}
	}
	return nil
}

// ValidTriggerPaths validates the triggerPaths array on a GitHub app config.
// Each path must be a valid relative file path (no null bytes, no traversal).
const maxTriggerPaths = 20
const maxTriggerPathLen = 200

func ValidTriggerPaths(paths []string) error {
	if len(paths) > maxTriggerPaths {
		return fmt.Errorf("triggerPaths must not exceed %d entries", maxTriggerPaths)
	}
	for _, p := range paths {
		if len(p) > maxTriggerPathLen {
			return fmt.Errorf("triggerPath entry exceeds max length of %d", maxTriggerPathLen)
		}
		if err := ValidFilePath(p); err != nil {
			return fmt.Errorf("invalid triggerPath %q: %w", p, err)
		}
	}
	return nil
}

// ValidEndpoints returns an error if any endpoint has an invalid port number.
func ValidEndpoints(endpoints []Endpoint) error {
	for _, ep := range endpoints {
		if ep.Port < 1 || ep.Port > 65535 {
			return fmt.Errorf("endpoint port %d is out of range (must be 1-65535)", ep.Port)
		}
	}
	return nil
}

// ValidRouteHost returns an error if the hostname is a reserved infrastructure domain.
// Blocked patterns:
//   - *-xquare-infra.dsmhs.kr  (harbor, argocd, argocdwebhook, argo-events, argo-workflows, vault, longhorn, goldilocks)
//   - xquare-remote-access-*.dsmhs.kr  (per-project DB tunnel access servers)
//   - *-observability-dashboard.dsmhs.kr  (per-project Grafana dashboards)
//   - xquare-server.dsmhs.kr  (the API server itself)
func ValidRouteHost(host string) error {
	h := strings.ToLower(strings.TrimSpace(host))
	if strings.HasSuffix(h, "-xquare-infra.dsmhs.kr") {
		return fmt.Errorf("route host %q is a reserved infrastructure domain", host)
	}
	if strings.HasPrefix(h, "xquare-remote-access-") && strings.HasSuffix(h, ".dsmhs.kr") {
		return fmt.Errorf("route host %q is a reserved infrastructure domain", host)
	}
	if strings.HasSuffix(h, "-observability-dashboard.dsmhs.kr") {
		return fmt.Errorf("route host %q is a reserved infrastructure domain", host)
	}
	if h == "xquare-server.dsmhs.kr" {
		return fmt.Errorf("route host %q is a reserved infrastructure domain", host)
	}
	return nil
}
