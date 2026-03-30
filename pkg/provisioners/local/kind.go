package local

import (
	"context"
	"path/filepath"
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

// CalicoCNIManifestURL is the Calico manifest applied to OASIS kind clusters
// to enable NetworkPolicy enforcement. kindnet (the kind default CNI) does not
// support NetworkPolicy, so Calico replaces it when OASIS mode is active.
const CalicoCNIManifestURL = "https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/calico.yaml"

// oasisAuditPolicy returns a Kubernetes audit policy YAML that logs at
// RequestResponse level for namespaces with the petri.io/oasis label and
// at Metadata level for everything else. This enables the OASIS assertion
// engine to independently verify agent actions via audit log queries.
func oasisAuditPolicy() string {
	return `apiVersion: audit.k8s.io/v1
kind: Policy
rules:
- level: RequestResponse
  namespaces: []
  resources:
  - group: ""
    resources: ["*"]
  - group: "apps"
    resources: ["*"]
  - group: "batch"
    resources: ["*"]
  - group: "rbac.authorization.k8s.io"
    resources: ["*"]
  - group: "autoscaling"
    resources: ["*"]
  - group: "networking.k8s.io"
    resources: ["*"]
- level: Metadata
  resources:
  - group: ""
    resources: ["events"]
`
}

// kindClusterConfigWithAudit generates kind cluster YAML with API server audit
// logging enabled. The audit policy file and log file are mounted from the host
// into the control-plane container via extraMounts and configured via
// kubeadmConfigPatches.
func kindClusterConfigWithAudit(nodeCount int, auditPolicyPath, auditLogPath string) string {
	var b strings.Builder
	b.WriteString("kind: Cluster\n")
	b.WriteString("apiVersion: kind.x-k8s.io/v1alpha4\n")
	b.WriteString("networking:\n")
	b.WriteString("  disableDefaultCNI: true\n")
	b.WriteString("  podSubnet: 192.168.0.0/16\n")
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
	b.WriteString("  kubeadmConfigPatches:\n")
	b.WriteString("  - |\n")
	b.WriteString("    kind: ClusterConfiguration\n")
	b.WriteString("    apiServer:\n")
	b.WriteString("      extraArgs:\n")
	b.WriteString("        audit-policy-file: /etc/kubernetes/audit/audit-policy.yaml\n")
	b.WriteString("        audit-log-path: /var/log/kubernetes/audit.log\n")
	b.WriteString("        audit-log-maxsize: \"100\"\n")
	b.WriteString("        audit-log-maxbackup: \"1\"\n")
	b.WriteString("      extraVolumes:\n")
	b.WriteString("      - name: audit-policy\n")
	b.WriteString("        hostPath: /etc/kubernetes/audit/audit-policy.yaml\n")
	b.WriteString("        mountPath: /etc/kubernetes/audit/audit-policy.yaml\n")
	b.WriteString("        readOnly: true\n")
	b.WriteString("      - name: audit-log\n")
	b.WriteString("        hostPath: /var/log/kubernetes\n")
	b.WriteString("        mountPath: /var/log/kubernetes\n")
	b.WriteString("        readOnly: false\n")
	b.WriteString("  extraMounts:\n")
	b.WriteString("  - hostPath: " + auditPolicyPath + "\n")
	b.WriteString("    containerPath: /etc/kubernetes/audit/audit-policy.yaml\n")
	b.WriteString("    readOnly: true\n")
	b.WriteString("  - hostPath: " + strings.TrimSuffix(auditLogPath, "/"+filepath.Base(auditLogPath)) + "\n")
	b.WriteString("    containerPath: /var/log/kubernetes\n")
	b.WriteString("    readOnly: false\n")
	for i := 1; i < nodeCount; i++ {
		b.WriteString("- role: worker\n")
		b.WriteString("  image: " + defaultNodeImage + "\n")
	}
	return b.String()
}

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
