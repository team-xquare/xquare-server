package k8s

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/team-xquare/xquare-server/internal/config"
	"github.com/team-xquare/xquare-server/internal/domain"
)

var workflowGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "workflows",
}

var workflowTemplateGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "workflowtemplates",
}

// ErrCINotReady is returned when the CI pipeline template is not yet deployed.
type ErrCINotReady struct{ App string }

func (e *ErrCINotReady) Error() string {
	return fmt.Sprintf("CI pipeline for %q is not ready yet — ArgoCD is still deploying the build infrastructure. Try again in a moment.", e.App)
}

// WorkflowClient wraps dynamic client for Argo Workflows
type WorkflowClient struct {
	dyn dynamic.Interface
	cs  *Client
}

// WorkflowInfo represents a CI build
type WorkflowInfo struct {
	ID        string `json:"id"`
	Status    string `json:"status"` // pending, running, success, failed
	StartedAt string `json:"startedAt,omitempty"`
	Message   string `json:"message,omitempty"`
}

func NewWorkflowClient(cfg *config.K8sConfig, k8sClient *Client) (*WorkflowClient, error) {
	restCfg, err := buildRestConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s config: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	return &WorkflowClient{dyn: dyn, cs: k8sClient}, nil
}

// WorkflowTemplateExists checks if the CI pipeline template for an app has been deployed by ArgoCD.
func (wc *WorkflowClient) WorkflowTemplateExists(ctx context.Context, project, app string) bool {
	ns := domain.Namespace(project)
	templateName := app + "-ci-pipeline-template"
	_, err := wc.dyn.Resource(workflowTemplateGVR).Namespace(ns).Get(ctx, templateName, metav1.GetOptions{})
	return err == nil
}

// TriggerCI creates a new Argo Workflow to build and deploy the app.
// sha is the git commit SHA to build; if non-empty it is passed as a push event
// so the workflow template skips its own SHA resolution logic.
func (wc *WorkflowClient) TriggerCI(ctx context.Context, project, app, sha string) (string, error) {
	ns := domain.Namespace(project)
	templateName := app + "-ci-pipeline-template"

	eventType := "manual"
	if sha != "" {
		eventType = "push"
	}

	workflow := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Workflow",
			"metadata": map[string]any{
				"generateName": app + "-ci-",
				"namespace":    ns,
				"labels": map[string]any{
					"app.kubernetes.io/name":      app,
					"app.kubernetes.io/component": "ci-workflow",
				},
			},
			"spec": map[string]any{
				"workflowTemplateRef": map[string]any{
					"name": templateName,
				},
				"arguments": map[string]any{
					"parameters": []any{
						map[string]any{"name": "github-event-type", "value": eventType},
						map[string]any{"name": "github-sha", "value": sha},
					},
				},
				"podGC": map[string]any{
					"strategy":            "OnPodCompletion",
					"deleteDelayDuration": "72h",
				},
				"serviceAccountName": "ci-workflow-sa",
			},
		},
	}

	// Pre-check: ensure the WorkflowTemplate was deployed by ArgoCD
	if !wc.WorkflowTemplateExists(ctx, project, app) {
		return "", &ErrCINotReady{App: app}
	}

	result, err := wc.dyn.Resource(workflowGVR).Namespace(ns).Create(ctx, workflow, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("create workflow: %w", err)
	}
	return result.GetName(), nil
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
		id := item.GetName()
		status := "unknown"
		startedAt := ""
		message := ""

		if wfStatus, ok := item.Object["status"].(map[string]any); ok {
			if p, ok := wfStatus["phase"].(string); ok {
				switch p {
				case "Succeeded":
					status = "success"
				case "Running":
					status = "running"
				case "Failed", "Error":
					status = "failed"
				case "Pending":
					status = "pending"
				default:
					status = strings.ToLower(p)
				}
			}
			if s, ok := wfStatus["startedAt"].(string); ok {
				startedAt = s
			}
			if m, ok := wfStatus["message"].(string); ok {
				message = m
			}
		}

		workflows = append(workflows, WorkflowInfo{
			ID:        id,
			Status:    status,
			StartedAt: startedAt,
			Message:   message,
		})
	}

	// sort newest first
	sort.Slice(workflows, func(i, j int) bool {
		return workflows[i].StartedAt > workflows[j].StartedAt
	})

	// cap to 50 most recent to prevent unbounded response size
	const maxBuilds = 50
	if len(workflows) > maxBuilds {
		workflows = workflows[:maxBuilds]
	}

	return workflows, nil
}

// StreamWorkflowLogs streams logs from the pods of a workflow (all steps, in order)
func (wc *WorkflowClient) StreamWorkflowLogs(ctx context.Context, project, workflowName string, follow bool) (io.ReadCloser, error) {
	ns := domain.Namespace(project)

	// Find pods labeled with this workflow
	pods, err := wc.cs.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "workflows.argoproj.io/workflow=" + workflowName,
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

	// Check if pod is still initializing before requesting logs
	for _, cs := range target.Status.ContainerStatuses {
		if cs.Name == container && cs.State.Waiting != nil {
			reason := cs.State.Waiting.Reason
			return nil, fmt.Errorf("build initializing (%s) — wait 15-30s and retry", reason)
		}
	}
	if target.Status.Phase == corev1.PodPending {
		return nil, fmt.Errorf("build initializing (pod pending) — wait 15-30s and retry")
	}

	tailLines := int64(500)
	req := wc.cs.cs.CoreV1().Pods(ns).GetLogs(target.Name, &corev1.PodLogOptions{
		Container: container,
		TailLines: &tailLines,
		Follow:    follow,
	})
	return req.Stream(ctx)
}
