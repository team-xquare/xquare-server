package domain

// Project represents the full projects/{name}.yaml
type Project struct {
	Owners       []string      `yaml:"owners,omitempty" json:"owners,omitempty"`
	Applications []Application `yaml:"applications" json:"applications"`
	Addons       []Addon       `yaml:"addons,omitempty" json:"addons,omitempty"`
}

// HasAccess returns true if the given GitHub username is an owner of this project.
func (p *Project) HasAccess(username string) bool {
	for _, o := range p.Owners {
		if o == username {
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
