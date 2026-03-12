package k8s

import (
	"context"
	"fmt"
	"io"
	"strings"

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
		return &rest.Config{Host: cfg.Host, BearerToken: cfg.Token, TLSClientConfig: rest.TLSClientConfig{Insecure: true}}, nil
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
}

// GetAppStatus returns the K8s deployment status for an app
func (c *Client) GetAppStatus(ctx context.Context, project, app string) (*AppStatus, error) {
	ns := domain.Namespace(project)

	dep, err := c.cs.AppsV1().Deployments(ns).Get(ctx, app, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return &AppStatus{Name: app, Status: "not_deployed"}, nil
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
	if dep.Status.ReadyReplicas == dep.Status.Replicas && dep.Status.Replicas > 0 {
		status = "running"
	} else if dep.Status.Replicas == 0 {
		status = "stopped"
	}
	for _, cond := range dep.Status.Conditions {
		if cond.Type == "Available" && cond.Status == "False" && dep.Status.ReadyReplicas == 0 {
			status = "failed"
		}
	}

	// Get instances (pods)
	pods, _ := c.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", app),
	})
	var instances []InstanceInfo
	for _, pod := range pods.Items {
		ready := false
		var restarts int32
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name == app {
				ready = cs.Ready
				restarts = cs.RestartCount
			}
		}
		inst := InstanceInfo{
			Status:   strings.ToLower(string(pod.Status.Phase)),
			Ready:    ready,
			Restarts: restarts,
		}
		if pod.Status.StartTime != nil {
			inst.Since = pod.Status.StartTime.UTC().Format("2006-01-02T15:04:05Z")
		}
		instances = append(instances, inst)
	}

	return &AppStatus{
		Name:      app,
		Status:    status,
		Scale:     Scale{Desired: dep.Status.Replicas, Running: dep.Status.ReadyReplicas},
		Version:   hash,
		Instances: instances,
	}, nil
}

// StreamPodLogs streams logs from the latest running pod for an app
func (c *Client) StreamPodLogs(ctx context.Context, project, app string, tailLines int64, follow bool) (io.ReadCloser, error) {
	ns := domain.Namespace(project)
	pods, err := c.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", app),
	})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	// Find best running pod
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
		return nil, fmt.Errorf("no pods found for app %s", app)
	}

	req := c.cs.CoreV1().Pods(ns).GetLogs(target.Name, &corev1.PodLogOptions{
		Container: app,
		TailLines: &tailLines,
		Follow:    follow,
	})
	return req.Stream(ctx)
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
	return string(data["password"]), nil
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
