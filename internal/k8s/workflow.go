package k8s

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/team-xquare/xquare-server/internal/config"
	"github.com/team-xquare/xquare-server/internal/domain"
)

var workflowGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "workflows",
}

// WorkflowClient wraps dynamic client for Argo Workflows
type WorkflowClient struct {
	dyn dynamic.Interface
	cs  *Client
}

// WorkflowInfo represents a CI build workflow
type WorkflowInfo struct {
	Name      string `json:"name"`
	Phase     string `json:"phase"`    // Pending, Running, Succeeded, Failed
	StartedAt string `json:"startedAt,omitempty"`
	Message   string `json:"message,omitempty"`
}

func NewWorkflowClient(cfg *config.K8sConfig, k8sClient *Client) (*WorkflowClient, error) {
	var restCfg *rest.Config
	var err error
	if cfg.ConfigPath != "" {
		restCfg, err = clientcmd.BuildConfigFromFlags("", cfg.ConfigPath)
	} else {
		restCfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("k8s config: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	return &WorkflowClient{dyn: dyn, cs: k8sClient}, nil
}

// ListWorkflows returns CI workflows for an app, sorted by start time (newest first)
func (wc *WorkflowClient) ListWorkflows(ctx context.Context, project, app string) ([]WorkflowInfo, error) {
	ns := domain.Namespace(project)

	list, err := wc.dyn.Resource(workflowGVR).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", app),
	})
	if err != nil {
		return nil, fmt.Errorf("list workflows: %w", err)
	}

	var workflows []WorkflowInfo
	for _, item := range list.Items {
		name := item.GetName()
		phase := "Unknown"
		startedAt := ""
		message := ""

		if status, ok := item.Object["status"].(map[string]any); ok {
			if p, ok := status["phase"].(string); ok {
				phase = p
			}
			if s, ok := status["startedAt"].(string); ok {
				startedAt = s
			}
			if m, ok := status["message"].(string); ok {
				message = m
			}
		}

		workflows = append(workflows, WorkflowInfo{
			Name:      name,
			Phase:     phase,
			StartedAt: startedAt,
			Message:   message,
		})
	}

	// sort newest first
	sort.Slice(workflows, func(i, j int) bool {
		return workflows[i].StartedAt > workflows[j].StartedAt
	})

	return workflows, nil
}

// StreamWorkflowLogs streams logs from the pods of a workflow (all steps, in order)
func (wc *WorkflowClient) StreamWorkflowLogs(ctx context.Context, project, workflowName string, follow bool) (io.ReadCloser, error) {
	ns := domain.Namespace(project)

	// Find pods labeled with this workflow
	pods, err := wc.cs.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("workflows.argoproj.io/workflow=%s", workflowName),
	})
	if err != nil {
		return nil, fmt.Errorf("list workflow pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no pods found for workflow %s", workflowName)
	}

	// Sort pods by creation time
	sort.Slice(pods.Items, func(i, j int) bool {
		return pods.Items[i].CreationTimestamp.Before(&pods.Items[j].CreationTimestamp)
	})

	// Find the main/primary pod (usually the one doing the actual build)
	// In Argo Workflows, look for pods not named with -wait suffix
	var target *corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		if !strings.HasSuffix(p.Name, "-wait") {
			target = p
			break
		}
	}
	if target == nil {
		target = &pods.Items[0]
	}

	// Determine container to stream — prefer "main", fallback to first container
	container := ""
	for _, c := range target.Spec.Containers {
		if c.Name == "main" {
			container = "main"
			break
		}
	}
	if container == "" && len(target.Spec.Containers) > 0 {
		container = target.Spec.Containers[0].Name
	}

	tailLines := int64(500)
	req := wc.cs.cs.CoreV1().Pods(ns).GetLogs(target.Name, &corev1.PodLogOptions{
		Container: container,
		TailLines: &tailLines,
		Follow:    follow,
	})
	return req.Stream(ctx)
}
