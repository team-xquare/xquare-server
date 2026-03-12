package domain

// Project represents the full projects/{name}.yaml
type Project struct {
	Applications []Application `yaml:"applications"`
	Addons       []Addon       `yaml:"addons,omitempty"`
}

// Application represents one entry in applications[]
type Application struct {
	Name                 string    `yaml:"name"`
	DisableNetworkPolicy bool      `yaml:"disableNetworkPolicy,omitempty"`
	GitHub               GitHub    `yaml:"github"`
	Build                Build     `yaml:"build"`
	Endpoints            []Endpoint `yaml:"endpoints,omitempty"`
}

type GitHub struct {
	Owner          string   `yaml:"owner"`
	Repo           string   `yaml:"repo"`
	Branch         string   `yaml:"branch"`
	InstallationID string   `yaml:"installationId"`
	Hash           string   `yaml:"hash,omitempty"`
	TriggerPaths   []string `yaml:"triggerPaths,omitempty"`
}

type Build struct {
	Gradle      *GradleBuild      `yaml:"gradle,omitempty"`
	NodeJS      *NodeJSBuild      `yaml:"nodejs,omitempty"`
	React       *ReactBuild       `yaml:"react,omitempty"`
	Vite        *ViteBuild        `yaml:"vite,omitempty"`
	Vue         *VueBuild         `yaml:"vue,omitempty"`
	NextJS      *NextJSBuild      `yaml:"nextjs,omitempty"`
	NextJSExport *NextJSExportBuild `yaml:"nextjs-export,omitempty"`
	Go          *GoBuild          `yaml:"go,omitempty"`
	Rust        *RustBuild        `yaml:"rust,omitempty"`
	Maven       *MavenBuild       `yaml:"maven,omitempty"`
	Django      *DjangoBuild      `yaml:"django,omitempty"`
	Flask       *FlaskBuild       `yaml:"flask,omitempty"`
	Docker      *DockerBuild      `yaml:"docker,omitempty"`
}

type GradleBuild struct {
	JavaVersion   string `yaml:"javaVersion"`
	JarOutputPath string `yaml:"jarOutputPath"`
	BuildCommand  string `yaml:"buildCommand"`
}

type NodeJSBuild struct {
	NodeVersion  string `yaml:"nodeVersion"`
	BuildCommand string `yaml:"buildCommand"`
	StartCommand string `yaml:"startCommand"`
}

type ReactBuild struct {
	NodeVersion  string `yaml:"nodeVersion"`
	BuildCommand string `yaml:"buildCommand"`
	DistPath     string `yaml:"distPath"`
}

type ViteBuild struct {
	NodeVersion  string `yaml:"nodeVersion"`
	BuildCommand string `yaml:"buildCommand"`
	DistPath     string `yaml:"distPath"`
}

type VueBuild struct {
	NodeVersion  string `yaml:"nodeVersion"`
	BuildCommand string `yaml:"buildCommand"`
	DistPath     string `yaml:"distPath"`
}

type NextJSBuild struct {
	NodeVersion  string `yaml:"nodeVersion"`
	BuildCommand string `yaml:"buildCommand"`
	StartCommand string `yaml:"startCommand"`
}

type NextJSExportBuild struct {
	NodeVersion  string `yaml:"nodeVersion"`
	BuildCommand string `yaml:"buildCommand"`
	DistPath     string `yaml:"distPath"`
}

type GoBuild struct {
	GoVersion    string `yaml:"goVersion"`
	BuildCommand string `yaml:"buildCommand"`
	BinaryName   string `yaml:"binaryName"`
}

type RustBuild struct {
	RustVersion  string `yaml:"rustVersion"`
	BuildCommand string `yaml:"buildCommand"`
	BinaryName   string `yaml:"binaryName"`
}

type MavenBuild struct {
	JavaVersion   string `yaml:"javaVersion"`
	BuildCommand  string `yaml:"buildCommand"`
	JarOutputPath string `yaml:"jarOutputPath"`
}

type DjangoBuild struct {
	PythonVersion string `yaml:"pythonVersion"`
	BuildCommand  string `yaml:"buildCommand"`
	StartCommand  string `yaml:"startCommand"`
}

type FlaskBuild struct {
	PythonVersion string `yaml:"pythonVersion"`
	BuildCommand  string `yaml:"buildCommand"`
	StartCommand  string `yaml:"startCommand"`
}

type DockerBuild struct {
	DockerfilePath string `yaml:"dockerfilePath"`
	ContextPath    string `yaml:"contextPath"`
}

type Endpoint struct {
	Port   int      `yaml:"port"`
	Routes []string `yaml:"routes,omitempty"`
}

// Addon represents one entry in addons[]
type Addon struct {
	Name      string `yaml:"name"`
	Type      string `yaml:"type"`
	Storage   string `yaml:"storage"`
	Bootstrap string `yaml:"bootstrap,omitempty"`
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
