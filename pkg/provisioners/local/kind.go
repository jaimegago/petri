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

// defaultNodeImage is the pinned kind node image. v1.31.4 is a well-tested
// release; v1.35.0 has a known kind incompatibility where bootstrap fails
// because it tries to remove a taint that no longer exists.
const defaultNodeImage = "kindest/node:v1.31.4"

// kindClusterConfig generates kind cluster YAML for the given total node count.
// The first node is always the control-plane; the rest are workers.
// extraPortMappings expose NodePort 30080 (HTTP) and 30443 (HTTPS) on the host
// so that ingress-nginx is reachable at http://localhost:30080.
// kind removes the control-plane NoSchedule taint automatically for single-node
// clusters, making workloads schedulable without a kubeadmConfigPatch.
func kindClusterConfig(nodeCount int) string {
	var b strings.Builder
	b.WriteString("kind: Cluster\n")
	b.WriteString("apiVersion: kind.x-k8s.io/v1alpha4\n")
	b.WriteString("nodes:\n")
	b.WriteString("- role: control-plane\n")
	b.WriteString("  image: " + defaultNodeImage + "\n")
	b.WriteString("  extraPortMappings:\n")
	b.WriteString("  - containerPort: 30080\n")
	b.WriteString("    hostPort: 30080\n")
	b.WriteString("    protocol: TCP\n")
	b.WriteString("  - containerPort: 30443\n")
	b.WriteString("    hostPort: 30443\n")
	b.WriteString("    protocol: TCP\n")
	for i := 1; i < nodeCount; i++ {
		b.WriteString("- role: worker\n")
		b.WriteString("  image: " + defaultNodeImage + "\n")
	}
	return b.String()
}
