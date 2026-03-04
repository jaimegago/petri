package local

import (
	"context"
	"strings"
)

// cliKind implements kindOps by shelling out to the kind CLI.
type cliKind struct{}

func (k *cliKind) createCluster(ctx context.Context, name, configPath, kubeconfigPath string) error {
	return runCmd(ctx, "kind", "create", "cluster",
		"--name", name,
		"--config", configPath,
		"--kubeconfig", kubeconfigPath,
	)
}

func (k *cliKind) deleteCluster(ctx context.Context, name string) error {
	return runCmd(ctx, "kind", "delete", "cluster", "--name", name)
}

func (k *cliKind) listClusters(ctx context.Context) ([]string, error) {
	out, err := runOutput(ctx, "kind", "get", "clusters")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var clusters []string
	for _, line := range strings.Split(out, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			clusters = append(clusters, t)
		}
	}
	return clusters, nil
}

// kindClusterConfig generates kind cluster YAML for the given total node count.
// The first node is always the control-plane; the rest are workers.
func kindClusterConfig(nodeCount int) string {
	var b strings.Builder
	b.WriteString("kind: Cluster\n")
	b.WriteString("apiVersion: kind.x-k8s.io/v1alpha4\n")
	b.WriteString("nodes:\n")
	b.WriteString("- role: control-plane\n")
	for i := 1; i < nodeCount; i++ {
		b.WriteString("- role: worker\n")
	}
	return b.String()
}
