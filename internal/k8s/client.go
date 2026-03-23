package k8s

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/team-xquare/xquare-server/internal/config"
	"github.com/team-xquare/xquare-server/internal/domain"
)

type Client struct {
	cs *kubernetes.Clientset
}

func buildRestConfig(cfg *config.K8sConfig) (*rest.Config, error) {
	if cfg.Token != "" {
		tlsCfg := rest.TLSClientConfig{}
		if cfg.CAData != "" {
			tlsCfg.CAData = []byte(cfg.CAData)
		} else {
			// No CA cert provided: TLS verification is disabled.
			// Set K8S_CA_CERT env var (PEM) to enable proper TLS verification.
			log.Println("WARN: K8S_CA_CERT not set — K8s API TLS verification disabled (MITM risk)")
			tlsCfg.Insecure = true
		}
		return &rest.Config{Host: cfg.Host, BearerToken: cfg.Token, TLSClientConfig: tlsCfg}, nil
	}
	if cfg.ConfigPath != "" {
		return clientcmd.BuildConfigFromFlags("", cfg.ConfigPath)
	}
	return rest.InClusterConfig()
}

func NewClient(cfg *config.K8sConfig) (*Client, error) {
	restCfg, err := buildRestConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("k8s client: %w", err)
	}
	return &Client{cs: cs}, nil
}

// AppStatus represents the deployment status of an application
type AppStatus struct {
	Name      string         `json:"name"`
	Status    string         `json:"status"` // running, pending, failed, stopped, not_deployed
	Message   string         `json:"message,omitempty"` // human-readable reason for current status (e.g. deployment condition message)
	Scale     Scale          `json:"scale"`
	Version   string         `json:"version"`
	Instances []InstanceInfo `json:"instances"`
}

type Scale struct {
	Desired int32 `json:"desired"`
	Running int32 `json:"running"`
}

type InstanceInfo struct {
	Status   string `json:"status"`
	Ready    bool   `json:"ready"`
	Restarts int32  `json:"restarts"`
	Since    string `json:"since,omitempty"`
	// Reason is the container waiting/terminated reason (e.g. CrashLoopBackOff, OOMKilled, Error).
	// Empty when the container is running normally.
	Reason string `json:"reason,omitempty"`
}

// GetAppStatus returns the K8s deployment status for an app
func (c *Client) GetAppStatus(ctx context.Context, project, app string) (*AppStatus, error) {
	ns := domain.Namespace(project)

	dep, err := c.cs.AppsV1().Deployments(ns).Get(ctx, app, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return &AppStatus{Name: app, Status: "not_deployed", Instances: []InstanceInfo{}}, nil
		}
		return nil, fmt.Errorf("get deployment: %w", err)
	}

	// Extract hash from image tag
	hash := ""
	if len(dep.Spec.Template.Spec.Containers) > 0 {
		img := dep.Spec.Template.Spec.Containers[0].Image
		parts := strings.SplitN(img, ":", 2)
		if len(parts) == 2 {
			hash = parts[1]
		}
	}

	status := "pending"
	statusMessage := ""
	desiredReplicas := int32(1)
	if dep.Spec.Replicas != nil {
		desiredReplicas = *dep.Spec.Replicas
	}
	if dep.Status.ReadyReplicas >= desiredReplicas && desiredReplicas > 0 {
		status = "running"
	} else if desiredReplicas == 0 {
		// Intentionally scaled to zero (user requested 0 replicas)
		status = "stopped"
	}
	// Check if a rolling update is in progress — Progressing=True means K8s is
	// actively replacing pods. During this window, Available=False with
	// ReadyReplicas==0 is expected and should stay "pending", not "failed".
	progressing := false
	for _, cond := range dep.Status.Conditions {
		if cond.Type == "Progressing" && cond.Status == "True" {
			progressing = true
		}
	}
	for _, cond := range dep.Status.Conditions {
		if cond.Type == "Available" && cond.Status == "False" && dep.Status.ReadyReplicas == 0 && !progressing {
			status = "failed"
			if cond.Message != "" {
				statusMessage = cond.Message
			}
		}
	}

	// Get instances (pods). Discard the error — a transient API failure should
	// not prevent returning deployment status; instances will just be empty.
	// Guard against nil to avoid a panic on pods.Items when List fails.
	pods, _ := c.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", app),
	})
	var podItems []corev1.Pod
	if pods != nil {
		podItems = pods.Items
	}
	instances := make([]InstanceInfo, 0, len(podItems))
	for _, pod := range podItems {
		ready := false
		var restarts int32
		var reason string
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name == app {
				ready = cs.Ready
				restarts = cs.RestartCount
				// Capture the most actionable failure reason:
				// prefer Waiting reason (CrashLoopBackOff) over Terminated reason (OOMKilled, Error).
				if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
					reason = cs.State.Waiting.Reason
				} else if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
					reason = cs.State.Terminated.Reason
				} else if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.Reason != "" {
					reason = cs.LastTerminationState.Terminated.Reason
				}
			}
		}
		inst := InstanceInfo{
			Status:   strings.ToLower(string(pod.Status.Phase)),
			Ready:    ready,
			Restarts: restarts,
			Reason:   reason,
		}
		if pod.Status.StartTime != nil {
			inst.Since = pod.Status.StartTime.UTC().Format("2006-01-02T15:04:05Z")
		}
		instances = append(instances, inst)
	}

	return &AppStatus{
		Name:      app,
		Status:    status,
		Message:   statusMessage,
		Scale:     Scale{Desired: desiredReplicas, Running: dep.Status.ReadyReplicas},
		Version:   hash,
		Instances: instances,
	}, nil
}

// StreamPodLogs streams logs from the latest pod for an app.
// If the pod exists but is not yet running, it polls until the container is ready
// (up to 3 minutes) before opening the log stream — so callers never see a raw
// Kubernetes "container is waiting" error.
// since is an optional duration string (e.g. "1h", "30m") that limits log output
// to lines produced within that window; an empty string means "all available".
func (c *Client) StreamPodLogs(ctx context.Context, project, app string, tailLines int64, follow bool, since string) (io.ReadCloser, error) {
	ns := domain.Namespace(project)

	const pollInterval = 2 * time.Second
	const pollTimeout = 3 * time.Minute
	deadline := time.Now().Add(pollTimeout)

	for {
		pods, err := c.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", app),
		})
		if err != nil {
			return nil, fmt.Errorf("list pods: %w", err)
		}

		// Prefer the newest Running pod; fall back to the newest pod of any phase.
		var target *corev1.Pod
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.Status.Phase == corev1.PodRunning {
				if target == nil || p.CreationTimestamp.After(target.CreationTimestamp.Time) {
					target = p
				}
			}
		}
		if target == nil && len(pods.Items) > 0 {
			target = &pods.Items[len(pods.Items)-1]
		}
		if target == nil {
			return nil, &ErrAppNotDeployed{App: app}
		}

		// Check whether the container is actually ready to serve logs.
		ready := target.Status.Phase == corev1.PodRunning || target.Status.Phase == corev1.PodSucceeded
		if ready {
			// Even if the pod is Running, the container might still be Waiting
			// (e.g. image pull in a multi-container pod).
			for _, cs := range target.Status.ContainerStatuses {
				if cs.Name == app && cs.State.Waiting != nil {
					ready = false
					break
				}
			}
		}

		if !ready {
			if time.Now().After(deadline) {
				return nil, &ErrPodStartTimeout{App: app}
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(pollInterval):
				continue
			}
		}

		// Resolve container name: prefer app name, fall back to first container.
		containerName := app
		if len(target.Spec.Containers) > 0 {
			found := false
			for _, cont := range target.Spec.Containers {
				if cont.Name == app {
					found = true
					break
				}
			}
			if !found {
				containerName = target.Spec.Containers[0].Name
			}
		}

		logOpts := &corev1.PodLogOptions{
			Container: containerName,
			TailLines: &tailLines,
			Follow:    follow,
		}
		if since != "" {
			if d, err := time.ParseDuration(since); err == nil && d > 0 {
				secs := int64(d.Seconds())
				logOpts.SinceSeconds = &secs
				// K8s supports SinceSeconds and TailLines simultaneously:
				// returns the last TailLines lines from within the time window.
				// Keep TailLines set so --since X --tail N behaves as expected.
			}
		}
		req := c.cs.CoreV1().Pods(ns).GetLogs(target.Name, logOpts)
		rc, err := req.Stream(ctx)
		if err != nil {
			// The container may have just transitioned to Waiting between our
			// readiness check and the stream call — retry once more if time allows.
			msg := err.Error()
			if (strings.Contains(msg, "waiting") || strings.Contains(msg, "ContainerCreating") ||
				strings.Contains(msg, "PodInitializing") || strings.Contains(msg, "not running")) &&
				time.Now().Before(deadline) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(pollInterval):
					continue
				}
			}
			return nil, fmt.Errorf("stream logs: %w", err)
		}
		return rc, nil
	}
}

// GetSecret reads a K8s secret
func (c *Client) GetSecret(ctx context.Context, namespace, name string) (map[string][]byte, error) {
	secret, err := c.cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return secret.Data, nil
}

// GetAccessServerPassword returns the access-server password for a project
func (c *Client) GetAccessServerPassword(ctx context.Context, project string) (string, error) {
	ns := domain.Namespace(project)
	data, err := c.GetSecret(ctx, ns, "access-server-password")
	if err != nil {
		return "", fmt.Errorf("get access-server-password: %w", err)
	}
	pw, ok := data["password"]
	if !ok {
		return "", fmt.Errorf("access-server-password secret missing 'password' key")
	}
	return string(pw), nil
}

// AddonReady returns true if the addon's StatefulSet exists and has at least one ready replica.
// addonType is used to determine the correct StatefulSet name (e.g. seaweedfs uses -filer suffix).
func (c *Client) AddonReady(ctx context.Context, project, addonName, addonType string) bool {
	ns := domain.Namespace(project)
	stsName := addonName
	if addonType == "seaweedfs" {
		stsName = addonName + "-filer"
	}
	sts, err := c.cs.AppsV1().StatefulSets(ns).Get(ctx, stsName, metav1.GetOptions{})
	if err != nil {
		return false
	}
	return sts.Status.ReadyReplicas > 0
}

// ErrAppNotDeployed is returned when streaming logs for an app that has no pods.
type ErrAppNotDeployed struct{ App string }

func (e *ErrAppNotDeployed) Error() string {
	return fmt.Sprintf("app %q has not been deployed yet — run: xquare trigger %s", e.App, e.App)
}

// ErrPodStartTimeout is returned when a pod fails to become ready within the wait window.
type ErrPodStartTimeout struct{ App string }

func (e *ErrPodStartTimeout) Error() string {
	return fmt.Sprintf("app %q did not start within 3 minutes", e.App)
}

// ScaleApp sets the replica count for an app's Deployment.
// replicas=0 stops the app; replicas>0 starts it.
func (c *Client) ScaleApp(ctx context.Context, project, app string, replicas int32) error {
	ns := domain.Namespace(project)
	scale, err := c.cs.AppsV1().Deployments(ns).GetScale(ctx, app, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("app %q has not been deployed yet — push to GitHub to trigger a build first", app)
		}
		return fmt.Errorf("get scale: %w", err)
	}
	scale.Spec.Replicas = replicas
	if _, err := c.cs.AppsV1().Deployments(ns).UpdateScale(ctx, app, scale, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update scale: %w", err)
	}
	return nil
}

// DeleteNamespace deletes the K8s namespace for a project (cascades all resources).
func (c *Client) DeleteNamespace(ctx context.Context, project string) error {
	ns := domain.Namespace(project)
	err := c.cs.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete namespace %s: %w", ns, err)
	}
	return nil
}

// NamespaceExists checks if a project namespace exists
func (c *Client) NamespaceExists(ctx context.Context, project string) (bool, error) {
	ns := domain.Namespace(project)
	_, err := c.cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
