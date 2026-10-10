package orchestrator

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	pulumiprov "github.com/jaimegago/petri/pkg/provisioners/pulumi"
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

// execAPIVersion is the client-authentication API version the exec plugins
// petri configures emit: `aws eks get-token`, `gke-gcloud-auth-plugin` and
// `kubelogin get-token`.
const execAPIVersion = "client.authentication.k8s.io/v1beta1"

// awsRegionOutput is the AWS template's region output.
const awsRegionOutput = "aws_region"

// gkeAuthPlugin is the exec plugin `gcloud container clusters get-credentials`
// configures, and gkeAuthInstallHint is the hint it writes beside it.
const (
	gkeAuthPlugin      = "gke-gcloud-auth-plugin"
	gkeAuthInstallHint = "Install gke-gcloud-auth-plugin for use with kubectl by following " +
		"https://cloud.google.com/kubernetes-engine/docs/how-to/cluster-access-for-kubectl#install_plugin"
)

// aksAADServerID is the application ID of the AKS-managed Microsoft Entra
// server application — the audience a token for any AAD-enabled AKS cluster
// is requested for (public cloud).
const aksAADServerID = "6dae42f8-4368-4678-94ff-3960e28e3630"

// clusterOutputKeys names one cluster's outputs: its cloud name, its API
// endpoint and its base64-encoded CA certificate.
type clusterOutputKeys struct {
	name, endpoint, certificate string
}

// eksOutputKeys returns the AWS template's output names for one cluster of
// the level spec. templates/terraform/aws/outputs.tf.tmpl declares them; a
// test renders the template and holds the two in step.
func eksOutputKeys(short string) (name, endpoint, certificate string) {
	return fmt.Sprintf("eks_%s_cluster_name", short),
		fmt.Sprintf("eks_%s_cluster_endpoint", short),
		fmt.Sprintf("eks_%s_cluster_certificate", short)
}

// gkeOutputKeys returns the GCP template's output names for one cluster.
// templates/terraform/gcp/outputs.tf.tmpl declares them; a test holds the two
// in step.
func gkeOutputKeys(short string) clusterOutputKeys {
	return clusterOutputKeys{
		name:        fmt.Sprintf("gke_%s_cluster_name", short),
		endpoint:    fmt.Sprintf("gke_%s_endpoint", short),
		certificate: fmt.Sprintf("gke_%s_cluster_ca_certificate", short),
	}
}

// aksOutputKeys returns the Azure Pulumi program's export names for one
// cluster. templates/pulumi/azure/index.ts.tmpl spells them with the template
// func `title`, which is strings.Title; a test holds the two in step.
func aksOutputKeys(short string) clusterOutputKeys {
	t := strings.Title(short) //nolint:staticcheck // must match the template's `title`
	return clusterOutputKeys{
		name:        "aks" + t + "Name",
		endpoint:    "aks" + t + "Endpoint",
		certificate: "aks" + t + "Certificate",
	}
}

// clusterAccess is how one provider's kubeconfig reaches its clusters.
type clusterAccess struct {
	keys func(short string) clusterOutputKeys
	// exec returns the exec-plugin credential for the named cluster. Every
	// provider authenticates through one, so no kubeconfig petri assembles
	// carries a secret: the token is minted at use from the caller's own
	// cloud credentials.
	exec func(clusterName string) *clientcmdapi.ExecConfig
}

// eksClustersFromOutputs assembles a kubeconfig for each EKS cluster from the
// AWS template's per-cluster outputs (`eks_<name>_cluster_name`,
// `_cluster_endpoint`, `_cluster_certificate`) and its `aws_region` output.
//
// Authentication is the standard EKS exec plugin, `aws eks get-token` — the
// kubeconfig `aws eks update-kubeconfig` writes. The aws CLI is a documented
// prerequisite of cloud labs (README).
func eksClustersFromOutputs(outputs map[string]tfprov.OutputValue, clusterNames []string, nodeCounts []int) ([]provisionedCluster, error) {
	out := terraformOutputSet(outputs)
	region, err := out.str(awsRegionOutput)
	if err != nil {
		return nil, err
	}
	return clustersFromOutputs(out, clusterAccess{
		keys: func(short string) clusterOutputKeys {
			n, e, c := eksOutputKeys(short)
			return clusterOutputKeys{name: n, endpoint: e, certificate: c}
		},
		exec: func(name string) *clientcmdapi.ExecConfig {
			return &clientcmdapi.ExecConfig{
				APIVersion:      execAPIVersion,
				Command:         "aws",
				Args:            []string{"eks", "get-token", "--cluster-name", name, "--region", region, "--output", "json"},
				InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
			}
		},
	}, clusterNames, nodeCounts)
}

// gkeClustersFromOutputs assembles a kubeconfig for each GKE cluster from the
// GCP template's per-cluster outputs (`gke_<name>_cluster_name`, `_endpoint`,
// `_cluster_ca_certificate`).
//
// Authentication is `gke-gcloud-auth-plugin`, the exec plugin `gcloud
// container clusters get-credentials` configures; it is a documented
// prerequisite of GCP labs (README). GKE reports its endpoint as a bare
// address, which gets its https scheme here.
func gkeClustersFromOutputs(outputs map[string]tfprov.OutputValue, clusterNames []string, nodeCounts []int) ([]provisionedCluster, error) {
	return clustersFromOutputs(terraformOutputSet(outputs), clusterAccess{
		keys: gkeOutputKeys,
		exec: func(string) *clientcmdapi.ExecConfig {
			return &clientcmdapi.ExecConfig{
				APIVersion:         execAPIVersion,
				Command:            gkeAuthPlugin,
				InstallHint:        gkeAuthInstallHint,
				ProvideClusterInfo: true,
				InteractiveMode:    clientcmdapi.NeverExecInteractiveMode,
			}
		},
	}, clusterNames, nodeCounts)
}

// aksClustersFromOutputs assembles a kubeconfig for each AKS cluster from the
// Azure program's per-cluster exports (`aks<Name>Name`, `Endpoint`,
// `Certificate`).
//
// The clusters are Microsoft Entra-enabled with Azure RBAC, so authentication
// is `kubelogin get-token --login azurecli`: a token minted from the caller's
// own `az login`, the AAD counterpart of `aws eks get-token`. No local-account
// credential is exported or stored. kubelogin is a documented prerequisite of
// Azure labs (README).
func aksClustersFromOutputs(outputs map[string]pulumiprov.OutputValue, clusterNames []string, nodeCounts []int) ([]provisionedCluster, error) {
	return clustersFromOutputs(pulumiOutputSet(outputs), clusterAccess{
		keys: aksOutputKeys,
		exec: func(string) *clientcmdapi.ExecConfig {
			return &clientcmdapi.ExecConfig{
				APIVersion:      execAPIVersion,
				Command:         "kubelogin",
				Args:            []string{"get-token", "--login", "azurecli", "--server-id", aksAADServerID},
				InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
			}
		},
	}, clusterNames, nodeCounts)
}

// clustersFromOutputs assembles one single-context kubeconfig per cluster of
// the level spec from that cluster's name, endpoint and CA outputs.
func clustersFromOutputs(out outputSet, access clusterAccess, clusterNames []string, nodeCounts []int) ([]provisionedCluster, error) {
	if len(clusterNames) == 0 {
		return nil, fmt.Errorf("level spec names no clusters")
	}
	clusters := make([]provisionedCluster, 0, len(clusterNames))
	for i, short := range clusterNames {
		keys := access.keys(short)
		name, err := out.str(keys.name)
		if err != nil {
			return nil, err
		}
		endpoint, err := out.str(keys.endpoint)
		if err != nil {
			return nil, err
		}
		caB64, err := out.str(keys.certificate)
		if err != nil {
			return nil, err
		}
		ca, err := base64.StdEncoding.DecodeString(caB64)
		if err != nil {
			return nil, fmt.Errorf("%s output %q is not valid base64", out.tool, keys.certificate)
		}

		server := withHTTPS(endpoint)
		kc, err := execKubeconfig(name, server, ca, access.exec(name))
		if err != nil {
			return nil, fmt.Errorf("building kubeconfig for cluster %s: %w", name, err)
		}
		nodes := 0
		if i < len(nodeCounts) {
			nodes = nodeCounts[i]
		}
		clusters = append(clusters, provisionedCluster{
			Name:       name,
			Endpoint:   server,
			NodeCount:  nodes,
			Kubeconfig: kc,
		})
	}
	return clusters, nil
}

// withHTTPS gives a bare API endpoint (GKE reports an IP address) its https
// scheme, and leaves an endpoint that has a scheme alone.
func withHTTPS(endpoint string) string {
	if strings.Contains(endpoint, "://") {
		return endpoint
	}
	return "https://" + endpoint
}

// execKubeconfig renders a single-context kubeconfig for one cluster that
// authenticates through an exec plugin.
func execKubeconfig(name, server string, ca []byte, exec *clientcmdapi.ExecConfig) ([]byte, error) {
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[name] = &clientcmdapi.Cluster{
		Server:                   server,
		CertificateAuthorityData: ca,
	}
	cfg.AuthInfos[name] = &clientcmdapi.AuthInfo{Exec: exec}
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

// outputSet is one IaC run's outputs by name, from either tool.
type outputSet struct {
	// tool names the IaC tool in error messages.
	tool   string
	values map[string]json.RawMessage
}

func terraformOutputSet(outputs map[string]tfprov.OutputValue) outputSet {
	values := make(map[string]json.RawMessage, len(outputs))
	for k, v := range outputs {
		values[k] = v.Value
	}
	return outputSet{tool: "terraform", values: values}
}

func pulumiOutputSet(outputs map[string]pulumiprov.OutputValue) outputSet {
	values := make(map[string]json.RawMessage, len(outputs))
	for k, v := range outputs {
		values[k] = v.Value
	}
	return outputSet{tool: "pulumi", values: values}
}

// str decodes a string-typed output. Errors name the output and never its
// value, which may be sensitive.
func (s outputSet) str(key string) (string, error) {
	raw, ok := s.values[key]
	if !ok {
		return "", fmt.Errorf("%s output %q missing", s.tool, key)
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("%s output %q is not a string", s.tool, key)
	}
	if v == "" {
		return "", fmt.Errorf("%s output %q is empty", s.tool, key)
	}
	return v, nil
}
