package orchestrator

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	tfprov "github.com/jaimegago/petri/pkg/provisioners/terraform"
)

// provisionedCluster is one Kubernetes cluster an IaC run created, with the
// kubeconfig petri uses to reach it.
type provisionedCluster struct {
	// Name is the cluster's name in the cloud (e.g. "acme-lab-prod").
	Name string
	// Endpoint is the API server URL, when known.
	Endpoint string
	// NodeCount is the node count the level spec asked for.
	NodeCount int
	// Kubeconfig is a single-context kubeconfig selecting this cluster. It is
	// credential material: never log it, and persist it only encrypted.
	Kubeconfig []byte
}

// eksExecAPIVersion is the client-authentication API version `aws eks
// get-token` emits.
const eksExecAPIVersion = "client.authentication.k8s.io/v1beta1"

// awsRegionOutput is the AWS template's region output.
const awsRegionOutput = "aws_region"

// eksOutputKeys returns the AWS template's output names for one cluster of
// the level spec. templates/terraform/aws/outputs.tf.tmpl declares them; a
// test renders the template and holds the two in step.
func eksOutputKeys(short string) (name, endpoint, certificate string) {
	return fmt.Sprintf("eks_%s_cluster_name", short),
		fmt.Sprintf("eks_%s_cluster_endpoint", short),
		fmt.Sprintf("eks_%s_cluster_certificate", short)
}

// eksClustersFromOutputs assembles a kubeconfig for each EKS cluster from the
// AWS template's per-cluster outputs (`eks_<name>_cluster_name`,
// `_cluster_endpoint`, `_cluster_certificate`) and its `aws_region` output.
//
// Authentication is the standard EKS exec plugin, `aws eks get-token` — the
// kubeconfig `aws eks update-kubeconfig` writes — so it carries no secret: the
// token is minted at use from the caller's own AWS credentials. The aws CLI is
// a documented prerequisite of cloud labs (README).
func eksClustersFromOutputs(outputs map[string]tfprov.OutputValue, clusterNames []string, nodeCounts []int) ([]provisionedCluster, error) {
	if len(clusterNames) == 0 {
		return nil, fmt.Errorf("level spec names no clusters")
	}
	region, err := stringOutput(outputs, awsRegionOutput)
	if err != nil {
		return nil, err
	}

	clusters := make([]provisionedCluster, 0, len(clusterNames))
	for i, short := range clusterNames {
		nameKey, endpointKey, caKey := eksOutputKeys(short)
		name, err := stringOutput(outputs, nameKey)
		if err != nil {
			return nil, err
		}
		endpoint, err := stringOutput(outputs, endpointKey)
		if err != nil {
			return nil, err
		}
		caB64, err := stringOutput(outputs, caKey)
		if err != nil {
			return nil, err
		}
		ca, err := base64.StdEncoding.DecodeString(caB64)
		if err != nil {
			return nil, fmt.Errorf("terraform output %q is not valid base64", caKey)
		}

		kc, err := eksKubeconfig(name, endpoint, region, ca)
		if err != nil {
			return nil, fmt.Errorf("building kubeconfig for cluster %s: %w", name, err)
		}
		nodes := 0
		if i < len(nodeCounts) {
			nodes = nodeCounts[i]
		}
		clusters = append(clusters, provisionedCluster{
			Name:       name,
			Endpoint:   endpoint,
			NodeCount:  nodes,
			Kubeconfig: kc,
		})
	}
	return clusters, nil
}

// eksKubeconfig renders a single-context kubeconfig for one EKS cluster.
func eksKubeconfig(name, endpoint, region string, ca []byte) ([]byte, error) {
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[name] = &clientcmdapi.Cluster{
		Server:                   endpoint,
		CertificateAuthorityData: ca,
	}
	cfg.AuthInfos[name] = &clientcmdapi.AuthInfo{
		Exec: &clientcmdapi.ExecConfig{
			APIVersion:      eksExecAPIVersion,
			Command:         "aws",
			Args:            []string{"eks", "get-token", "--cluster-name", name, "--region", region, "--output", "json"},
			InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
		},
	}
	cfg.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	cfg.CurrentContext = name
	return clientcmd.Write(*cfg)
}

// mergeKubeconfigs combines per-cluster kubeconfigs into one, current context
// on the first (primary) cluster. It is what the lab's encrypted `kubeconfig`
// credential holds.
func mergeKubeconfigs(clusters []provisionedCluster) ([]byte, error) {
	merged := clientcmdapi.NewConfig()
	for i, c := range clusters {
		cfg, err := clientcmd.Load(c.Kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("parsing kubeconfig for cluster %s: %w", c.Name, err)
		}
		for k, v := range cfg.Clusters {
			merged.Clusters[k] = v
		}
		for k, v := range cfg.AuthInfos {
			merged.AuthInfos[k] = v
		}
		for k, v := range cfg.Contexts {
			merged.Contexts[k] = v
		}
		if i == 0 {
			merged.CurrentContext = cfg.CurrentContext
		}
	}
	return clientcmd.Write(*merged)
}

// stringOutput decodes a string-typed IaC output. Errors name the output and
// never its value, which may be sensitive.
func stringOutput(outputs map[string]tfprov.OutputValue, key string) (string, error) {
	out, ok := outputs[key]
	if !ok {
		return "", fmt.Errorf("terraform output %q missing", key)
	}
	var s string
	if err := json.Unmarshal(out.Value, &s); err != nil {
		return "", fmt.Errorf("terraform output %q is not a string", key)
	}
	if s == "" {
		return "", fmt.Errorf("terraform output %q is empty", key)
	}
	return s, nil
}
