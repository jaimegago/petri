package preflight

import (
	"context"
	"fmt"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// KubeClient is the cluster-touching surface preflight needs. It is defined
// here at the point of use, per repo invariants — production code uses the
// client-go-backed implementation; tests inject a fake.
type KubeClient interface {
	// ServerVersion returns the cluster's reported version string. A non-nil
	// error indicates the cluster could not be reached or did not authorize
	// the discovery call. The returned errKind classifies the failure for
	// renderers (network, auth, tls, other).
	ServerVersion(ctx context.Context) (string, error)
	// CanI returns whether the kubeconfig identity is authorized for verb on
	// resource at cluster scope (no namespace). An error means the
	// authorization check itself could not be performed.
	CanI(ctx context.Context, verb, resource string) (allowed bool, reason string, err error)
	// CreateNamespace creates the given namespace. Used only by deep mode.
	CreateNamespace(ctx context.Context, name string) error
	// DeleteNamespace deletes the given namespace. Used only by deep mode.
	DeleteNamespace(ctx context.Context, name string) error
	// CreatePullPod creates a Pod that runs `sleep 30` from the given image
	// with restartPolicy=Never. The container name is fixed to "verify".
	CreatePullPod(ctx context.Context, namespace, name, image string) error
	// PodPullStatus polls the given Pod's status and returns a high-level
	// summary used by the deep pull check.
	PodPullStatus(ctx context.Context, namespace, name string) (PodPullStatus, error)
}

// PodPullStatus is the deep-mode pod's image-pull state at a point in time.
type PodPullStatus struct {
	// Phase is the pod-level phase (Pending/Running/Failed/etc.).
	Phase string
	// Pulling is true if any container is currently in ImagePullBackOff or
	// ErrImagePull or Waiting with a pull-related reason.
	Pulling bool
	// Reason is the container waiting reason if available.
	Reason string
	// Message is the kubelet's free-text explanation, often the underlying
	// network error.
	Message string
	// Ready is true when at least one container has Running.startedAt set.
	Ready bool
}

// errKind classifies cluster connection failures so the renderer can give a
// more actionable message than the raw error string.
type errKind int

const (
	errKindOther errKind = iota
	errKindNetwork
	errKindTLS
	errKindAuth
)

// classifyKubeError inspects a kube client error and returns a kind plus a
// short summary suitable for the Check.Summary line.
func classifyKubeError(err error) (errKind, string) {
	if err == nil {
		return errKindOther, ""
	}
	msg := err.Error()
	switch {
	case apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err):
		return errKindAuth, "authentication or authorization rejected"
	case containsAny(msg, "x509:", "tls:", "certificate signed by unknown authority"):
		return errKindTLS, "TLS handshake failed"
	case containsAny(msg, "no such host", "i/o timeout", "connection refused", "dial tcp", "context deadline exceeded"):
		return errKindNetwork, "cluster unreachable"
	}
	return errKindOther, "request to API server failed"
}

// containsAny is a tiny helper to keep classifyKubeError readable.
func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if n != "" && contains(s, n) {
			return true
		}
	}
	return false
}

// contains is strings.Contains without importing strings here. (Trivial; the
// package needs strings elsewhere too — kept inline so this file stands alone
// if extracted.)
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// loadKubeconfig parses the kubeconfig at path (or the default chain when
// path is empty) and returns the resulting *rest.Config.
func loadKubeconfig(path string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = path
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	return cfg, nil
}

// clientGoKubeClient is the production KubeClient. Constructed once per Run.
type clientGoKubeClient struct {
	cs *kubernetes.Clientset
}

// newClientGoKubeClient builds a KubeClient backed by client-go from a
// previously loaded *rest.Config.
func newClientGoKubeClient(cfg *rest.Config) (*clientGoKubeClient, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building kubernetes client: %w", err)
	}
	return &clientGoKubeClient{cs: cs}, nil
}

func (c *clientGoKubeClient) ServerVersion(_ context.Context) (string, error) {
	v, err := c.cs.Discovery().ServerVersion()
	if err != nil {
		return "", err
	}
	return v.GitVersion, nil
}

func (c *clientGoKubeClient) CanI(ctx context.Context, verb, resource string) (bool, string, error) {
	r := &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{
				Verb:     verb,
				Resource: resource,
			},
		},
	}
	resp, err := c.cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, r, metav1.CreateOptions{})
	if err != nil {
		return false, "", err
	}
	return resp.Status.Allowed, resp.Status.Reason, nil
}

func (c *clientGoKubeClient) CreateNamespace(ctx context.Context, name string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	_, err := c.cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (c *clientGoKubeClient) DeleteNamespace(ctx context.Context, name string) error {
	err := c.cs.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (c *clientGoKubeClient) CreatePullPod(ctx context.Context, namespace, name, image string) error {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "verify",
				Image:   image,
				Command: []string{"sleep", "30"},
			}},
			TerminationGracePeriodSeconds: ptrInt64(0),
		},
	}
	_, err := c.cs.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	return err
}

func (c *clientGoKubeClient) PodPullStatus(ctx context.Context, namespace, name string) (PodPullStatus, error) {
	pod, err := c.cs.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return PodPullStatus{}, err
	}
	out := PodPullStatus{Phase: string(pod.Status.Phase)}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Running != nil && !cs.State.Running.StartedAt.IsZero() {
			out.Ready = true
		}
		if cs.State.Waiting != nil {
			out.Reason = cs.State.Waiting.Reason
			out.Message = cs.State.Waiting.Message
			if isPullReason(cs.State.Waiting.Reason) {
				out.Pulling = true
			}
		}
	}
	return out, nil
}

// isPullReason returns true for kubelet waiting reasons that indicate an
// in-progress or failed image pull.
func isPullReason(reason string) bool {
	switch reason {
	case "ImagePullBackOff", "ErrImagePull", "PullImageError", "ContainerCreating":
		return true
	}
	return false
}

// ptrInt64 is a trivial helper for required *int64 fields in the corev1 spec.
func ptrInt64(v int64) *int64 { return &v }

// pollUntil polls fn at the given interval until it returns done=true, an
// error, or ctx is cancelled. Used by deep mode to wait on pod state.
func pollUntil(ctx context.Context, interval time.Duration, fn func() (done bool, err error)) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		done, err := fn()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
