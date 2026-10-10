package orchestrator

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/jaimegago/petri/pkg/generators"
	"github.com/jaimegago/petri/pkg/generators/iac"
	"github.com/jaimegago/petri/pkg/logger"
	pulumiprov "github.com/jaimegago/petri/pkg/provisioners/pulumi"
	tfprov "github.com/jaimegago/petri/pkg/provisioners/terraform"
	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

const testGCPProject = "lab-project"

// gkeOutputs returns the outputs the GCP template emits for the given
// clusters. GKE reports its endpoint as a bare address.
func gkeOutputs(t *testing.T, shorts ...string) map[string]tfprov.OutputValue {
	t.Helper()
	out := map[string]tfprov.OutputValue{}
	for i, s := range shorts {
		k := gkeOutputKeys(s)
		out[k.name] = strOutput(t, "cloudnative-lab-"+s)
		out[k.endpoint] = strOutput(t, fmt.Sprintf("34.1.2.%d", i+1))
		out[k.certificate] = strOutput(t, base64.StdEncoding.EncodeToString(testCA))
	}
	return out
}

// aksOutputs returns the exports the Azure program emits for the given
// clusters.
func aksOutputs(t *testing.T, shorts ...string) map[string]pulumiprov.OutputValue {
	t.Helper()
	out := map[string]pulumiprov.OutputValue{}
	for _, s := range shorts {
		k := aksOutputKeys(s)
		out[k.name] = pulumiprov.OutputValue{Value: strOutput(t, "techflow-lab-"+s).Value}
		out[k.endpoint] = pulumiprov.OutputValue{Value: strOutput(t, "https://"+s+".hcp.eastus.azmk8s.io").Value}
		out[k.certificate] = pulumiprov.OutputValue{Value: strOutput(t, base64.StdEncoding.EncodeToString(testCA)).Value}
	}
	return out
}

// assertExecKubeconfig checks one cluster's kubeconfig: a single context on
// the cluster, the CA and server it was given, and exec authentication with
// no static credential.
func assertExecKubeconfig(t *testing.T, c provisionedCluster, wantServer string) *clientcmdapi.ExecConfig {
	t.Helper()
	cfg, err := clientcmd.Load(c.Kubeconfig)
	if err != nil {
		t.Fatalf("kubeconfig for %s does not parse: %v", c.Name, err)
	}
	if cfg.CurrentContext != c.Name || len(cfg.Contexts) != 1 {
		t.Errorf("kubeconfig for %s: current %q, %d contexts", c.Name, cfg.CurrentContext, len(cfg.Contexts))
	}
	cl := cfg.Clusters[c.Name]
	if cl == nil || cl.Server != wantServer || string(cl.CertificateAuthorityData) != string(testCA) {
		t.Errorf("kubeconfig for %s: cluster entry wrong", c.Name)
	}
	if c.Endpoint != wantServer {
		t.Errorf("cluster %s Endpoint = %q, want %q", c.Name, c.Endpoint, wantServer)
	}
	auth := cfg.AuthInfos[c.Name]
	if auth == nil || auth.Exec == nil {
		t.Fatalf("kubeconfig for %s: no exec auth", c.Name)
	}
	if auth.Token != "" || auth.TokenFile != "" || auth.ClientKeyData != nil || auth.ClientCertificateData != nil ||
		auth.Password != "" || auth.AuthProvider != nil {
		t.Errorf("kubeconfig for %s carries a static credential", c.Name)
	}
	if auth.Exec.APIVersion != execAPIVersion || auth.Exec.InteractiveMode != clientcmdapi.NeverExecInteractiveMode {
		t.Errorf("exec for %s: api %q, interactive %q", c.Name, auth.Exec.APIVersion, auth.Exec.InteractiveMode)
	}
	return auth.Exec
}

func TestGKEClustersFromOutputs(t *testing.T) {
	clusters, err := gkeClustersFromOutputs(gkeOutputs(t, "prod", "staging"), []string{"prod", "staging"}, []int{3, 1})
	if err != nil {
		t.Fatalf("gkeClustersFromOutputs: %v", err)
	}
	if len(clusters) != 2 {
		t.Fatalf("got %d clusters, want 2", len(clusters))
	}
	for i, short := range []string{"prod", "staging"} {
		c := clusters[i]
		if c.Name != "cloudnative-lab-"+short || c.NodeCount != []int{3, 1}[i] {
			t.Errorf("cluster %d = %q with %d nodes", i, c.Name, c.NodeCount)
		}
		exec := assertExecKubeconfig(t, c, fmt.Sprintf("https://34.1.2.%d", i+1))
		if exec.Command != gkeAuthPlugin || len(exec.Args) != 0 || !exec.ProvideClusterInfo || exec.InstallHint == "" {
			t.Errorf("exec for %s = %s %v (provideClusterInfo %v)", short, exec.Command, exec.Args, exec.ProvideClusterInfo)
		}
	}
}

func TestAKSClustersFromOutputs(t *testing.T) {
	clusters, err := aksClustersFromOutputs(aksOutputs(t, "prod", "staging"), []string{"prod", "staging"}, []int{2, 1})
	if err != nil {
		t.Fatalf("aksClustersFromOutputs: %v", err)
	}
	if len(clusters) != 2 {
		t.Fatalf("got %d clusters, want 2", len(clusters))
	}
	for i, short := range []string{"prod", "staging"} {
		c := clusters[i]
		if c.Name != "techflow-lab-"+short || c.NodeCount != []int{2, 1}[i] {
			t.Errorf("cluster %d = %q with %d nodes", i, c.Name, c.NodeCount)
		}
		exec := assertExecKubeconfig(t, c, "https://"+short+".hcp.eastus.azmk8s.io")
		want := "get-token --login azurecli --server-id " + aksAADServerID
		if exec.Command != "kubelogin" || strings.Join(exec.Args, " ") != want {
			t.Errorf("exec for %s = %s %v", short, exec.Command, exec.Args)
		}
	}
}

func TestAKSOutputKeys(t *testing.T) {
	k := aksOutputKeys("staging")
	if k.name != "aksStagingName" || k.endpoint != "aksStagingEndpoint" || k.certificate != "aksStagingCertificate" {
		t.Errorf("aksOutputKeys(staging) = %+v", k)
	}
}

func TestGKEAndAKSClustersFromOutputs_Errors(t *testing.T) {
	notB64 := "not base64 !!"
	gk, ak := gkeOutputKeys("prod"), aksOutputKeys("prod")

	type build func(t *testing.T, drop, corrupt string) error
	gke := func(t *testing.T, drop, corrupt string) error {
		out := gkeOutputs(t, "prod")
		delete(out, drop)
		if corrupt != "" {
			out[corrupt] = strOutput(t, notB64)
		}
		_, err := gkeClustersFromOutputs(out, []string{"prod"}, []int{1})
		return err
	}
	aks := func(t *testing.T, drop, corrupt string) error {
		out := aksOutputs(t, "prod")
		delete(out, drop)
		if corrupt != "" {
			out[corrupt] = pulumiprov.OutputValue{Value: strOutput(t, notB64).Value}
		}
		_, err := aksClustersFromOutputs(out, []string{"prod"}, []int{1})
		return err
	}

	tests := []struct {
		name    string
		build   build
		drop    string
		corrupt string
		want    string
	}{
		{"gke missing name", gke, gk.name, "", `terraform output "` + gk.name + `" missing`},
		{"gke missing endpoint", gke, gk.endpoint, "", gk.endpoint},
		{"gke missing certificate", gke, gk.certificate, "", gk.certificate},
		{"gke certificate not base64", gke, "", gk.certificate, gk.certificate},
		{"aks missing name", aks, ak.name, "", `pulumi output "` + ak.name + `" missing`},
		{"aks missing endpoint", aks, ak.endpoint, "", ak.endpoint},
		{"aks missing certificate", aks, ak.certificate, "", ak.certificate},
		{"aks certificate not base64", aks, "", ak.certificate, ak.certificate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.build(t, tt.drop, tt.corrupt)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
			if strings.Contains(err.Error(), notB64) || strings.Contains(err.Error(), "BEGIN CERTIFICATE") {
				t.Errorf("error echoes an output value: %q", err)
			}
		})
	}

	if _, err := gkeClustersFromOutputs(gkeOutputs(t), nil, nil); err == nil {
		t.Error("expected an error for a level spec with no clusters")
	}
	if _, err := aksClustersFromOutputs(aksOutputs(t), nil, nil); err == nil {
		t.Error("expected an error for a level spec with no clusters")
	}
}

func TestWithHTTPS(t *testing.T) {
	tests := map[string]string{
		"34.1.2.3":                   "https://34.1.2.3",
		"https://x.eks.example":      "https://x.eks.example",
		"http://127.0.0.1:6443":      "http://127.0.0.1:6443",
		"x.hcp.eastus.azmk8s.io:443": "https://x.hcp.eastus.azmk8s.io:443",
	}
	for in, want := range tests {
		if got := withHTTPS(in); got != want {
			t.Errorf("withHTTPS(%q) = %q, want %q", in, got, want)
		}
	}
}

// renderIaC renders the real templates for a provider and returns them by path.
func renderIaC(t *testing.T, provider, tool string, clusters []string) map[string]string {
	t.Helper()
	nodes := make([]int, len(clusters))
	instances := make([]string, len(clusters))
	for i := range clusters {
		nodes[i], instances[i] = 2, "small"
	}
	files, err := iac.New().Generate(context.Background(), generators.TemplateContext{
		Lab:     generators.LabInfo{Name: "lab"},
		Company: generators.CompanyInfo{Name: "co", CloudProvider: provider, IaCTool: tool},
		Level: generators.LevelInfo{
			Number:        2,
			ClusterNames:  clusters,
			NodeCounts:    nodes,
			InstanceTypes: instances,
		},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("rendering %s/%s templates: %v", tool, provider, err)
	}
	byPath := map[string]string{}
	for _, f := range files {
		byPath[f.Path] = f.Content
	}
	return byPath
}

// TestGCPTemplateDeclaresKubeconfigOutputs holds the real GCP template and the
// kubeconfig assembly in step, and pins that project_id is the input petri
// passes.
func TestGCPTemplateDeclaresKubeconfigOutputs(t *testing.T) {
	clusters := []string{"prod", "staging"}
	files := renderIaC(t, "gcp", "terraform", clusters)
	outputs := files["outputs.tf"]
	for _, s := range clusters {
		k := gkeOutputKeys(s)
		for _, key := range []string{k.name, k.endpoint, k.certificate} {
			if !strings.Contains(outputs, fmt.Sprintf("output %q", key)) {
				t.Errorf("outputs.tf does not declare %q", key)
			}
		}
		if !strings.Contains(outputs, "google_container_cluster."+s+".master_auth[0].cluster_ca_certificate") {
			t.Errorf("CA output for %s does not read the cluster's master_auth", s)
		}
	}
	if !strings.Contains(files["variables.tf"], `variable "project_id"`) {
		t.Error(`variables.tf does not declare "project_id", the variable petri passes`)
	}
}

// TestAzureProgramExportsKubeconfigOutputs holds the real Azure program and
// the kubeconfig assembly in step, and pins the cluster auth posture: Entra
// ID with Azure RBAC, the deploying identity granted cluster admin, and no
// credential exported.
func TestAzureProgramExportsKubeconfigOutputs(t *testing.T) {
	clusters := []string{"prod", "staging"}
	program := renderIaC(t, "azure", "pulumi", clusters)["index.ts"]
	for _, s := range clusters {
		k := aksOutputKeys(s)
		for _, key := range []string{k.name, k.endpoint, k.certificate} {
			if !strings.Contains(program, "export const "+key+" = ") {
				t.Errorf("index.ts does not export %q", key)
			}
		}
	}
	if n := strings.Count(program, "enableAzureRBAC: true"); n != len(clusters) {
		t.Errorf("%d clusters enable Azure RBAC, want %d", n, len(clusters))
	}
	if n := strings.Count(program, "new azure.authorization.RoleAssignment("); n != len(clusters) {
		t.Errorf("%d cluster-admin role assignments, want %d", n, len(clusters))
	}
	if strings.Contains(program, "listManagedClusterAdminCredentials") {
		t.Error("index.ts reads admin credentials")
	}
	if regexp.MustCompile(`export const \w*([Kk]ubeconfig|Credential)`).MatchString(program) {
		t.Error("index.ts exports a kubeconfig or credential")
	}
}

// newProviderTestOrchestrator builds a cloud test orchestrator; set adjusts
// its deps (leave an IaC provisioner unset rather than assigning a nil
// pointer to it).
func newProviderTestOrchestrator(t *testing.T, mgr state.Manager, set func(*Deps)) (*Orchestrator, map[string]*recordingKubectl) {
	t.Helper()
	kctls := map[string]*recordingKubectl{}
	deps := Deps{
		State:   mgr,
		Cipher:  &mockCipher{},
		Log:     logger.Nop(),
		GitProv: &mockGitProv{},
		KubectlFactory: func(path string) KubectlClient {
			if kctls[path] == nil {
				kctls[path] = &recordingKubectl{}
			}
			return kctls[path]
		},
		AppsGen:          &mockAppsGen{},
		PlatformGen:      &mockPlatformGen{},
		ObservabilityGen: &mockObsGen{},
	}
	set(&deps)
	return New(Config{WorkDir: t.TempDir()}, deps), kctls
}

func newProviderLab(t *testing.T, mgr state.Manager, provider types.CloudProvider) *types.Lab {
	t.Helper()
	lab := newTestLab(provider)
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}
	return lab
}

// assertClusterSteps checks every recorded cluster got the local path's
// treatment: node wait, platform, observability, apps.
func assertClusterSteps(t *testing.T, lab *types.Lab, kctls map[string]*recordingKubectl, want int) {
	t.Helper()
	if len(lab.Metadata.Clusters) != want {
		t.Fatalf("recorded %d clusters, want %d", len(lab.Metadata.Clusters), want)
	}
	for _, c := range lab.Metadata.Clusters {
		k := kctls[c.KubeconfigPath]
		if k == nil {
			t.Fatalf("no kubectl client built for %s", c.Name)
		}
		if k.waited != 1 || len(k.applied) != 3 {
			t.Errorf("cluster %s: waited %d, applied %d manifests (want 1, 3)", c.Name, k.waited, len(k.applied))
		}
	}
}

func TestCreate_CloudGCP_RefusesMissingProjectBeforeAnything(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newProviderLab(t, mgr, types.CloudProviderGCP)
	git := &mockGitProv{}
	applied := false
	tf := &mockTFProv{applyFn: func(context.Context, tfprov.ApplyOptions) (*tfprov.ApplyResult, error) {
		applied = true
		return &tfprov.ApplyResult{}, nil
	}}
	o, _ := newProviderTestOrchestrator(t, mgr, func(d *Deps) { d.TFProv, d.GitProv = tf, git })
	company, spec := cloudTestCompany()

	err := o.Create(context.Background(), CreateOptions{Lab: lab, Company: company, Spec: spec})
	if !errors.Is(err, ErrNoGCPProject) {
		t.Fatalf("Create error = %v, want ErrNoGCPProject", err)
	}
	if !strings.Contains(err.Error(), "gcp.project_id") || !strings.Contains(err.Error(), "--gcp-project") {
		t.Errorf("error %q does not say how to set the project", err)
	}
	if applied || len(git.created) != 0 {
		t.Errorf("work ran before the refusal: applied %v, %d repos", applied, len(git.created))
	}
}

func TestCreate_CloudGCP_ObtainsKubeconfigAndInstallsPlatform(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newProviderLab(t, mgr, types.CloudProviderGCP)
	var gotVars map[string]string
	tf := &mockTFProv{applyFn: func(_ context.Context, opts tfprov.ApplyOptions) (*tfprov.ApplyResult, error) {
		gotVars = opts.Vars
		return &tfprov.ApplyResult{Outputs: gkeOutputs(t, "prod", "staging")}, nil
	}}
	o, kctls := newProviderTestOrchestrator(t, mgr, func(d *Deps) { d.TFProv = tf })
	company, spec := cloudTestCompany()

	if err := o.Create(context.Background(), CreateOptions{Lab: lab, Company: company, Spec: spec, GCPProjectID: testGCPProject}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotVars["project_id"] != testGCPProject || len(gotVars) != 1 {
		t.Errorf("apply vars = %v, want project_id only", gotVars)
	}
	if lab.Metadata.GCPProjectID != testGCPProject {
		t.Errorf("lab metadata project = %q", lab.Metadata.GCPProjectID)
	}
	if lab.Status != types.LabStatusActive {
		t.Errorf("status = %s, want ACTIVE", lab.Status)
	}
	cred, err := mgr.GetCredential(context.Background(), lab.ID, "kubeconfig")
	if err != nil {
		t.Fatalf("no kubeconfig credential stored: %v", err)
	}
	stored, err := clientcmd.Load([]byte(cred.EncryptedValue)) // mockCipher is the identity
	if err != nil {
		t.Fatal(err)
	}
	if stored.CurrentContext != "cloudnative-lab-prod" || len(stored.Contexts) != 2 {
		t.Errorf("stored kubeconfig: current %q, %d contexts", stored.CurrentContext, len(stored.Contexts))
	}
	assertClusterSteps(t, lab, kctls, 2)
	if lab.Metadata.Clusters[0].Endpoint != "https://34.1.2.1" {
		t.Errorf("primary endpoint = %q, want an https URL", lab.Metadata.Clusters[0].Endpoint)
	}
}

func TestCreate_CloudGCP_RollbackDestroysWithProject(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newProviderLab(t, mgr, types.CloudProviderGCP)
	var destroyVars map[string]string
	tf := &mockTFProv{
		applyFn: func(context.Context, tfprov.ApplyOptions) (*tfprov.ApplyResult, error) {
			out := gkeOutputs(t, "prod", "staging")
			delete(out, gkeOutputKeys("staging").certificate)
			return &tfprov.ApplyResult{Outputs: out}, nil
		},
		destroyFn: func(_ context.Context, opts tfprov.DestroyOptions) error {
			destroyVars = opts.Vars
			return nil
		},
	}
	o, _ := newProviderTestOrchestrator(t, mgr, func(d *Deps) { d.TFProv = tf })
	company, spec := cloudTestCompany()

	err := o.Create(context.Background(), CreateOptions{Lab: lab, Company: company, Spec: spec, GCPProjectID: testGCPProject})
	if err == nil || !strings.Contains(err.Error(), gkeOutputKeys("staging").certificate) {
		t.Fatalf("Create error = %v, want one naming the missing CA output", err)
	}
	if destroyVars["project_id"] != testGCPProject {
		t.Errorf("rollback destroy vars = %v, want the lab's project", destroyVars)
	}
}

func TestDestroy_CloudGCP_UsesRecordedProject(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newProviderLab(t, mgr, types.CloudProviderGCP)
	lab.Status = types.LabStatusDestroying
	lab.Metadata.GCPProjectID = testGCPProject
	var destroyVars map[string]string
	tf := &mockTFProv{destroyFn: func(_ context.Context, opts tfprov.DestroyOptions) error {
		destroyVars = opts.Vars
		return nil
	}}
	o, _ := newProviderTestOrchestrator(t, mgr, func(d *Deps) { d.TFProv = tf })
	company, spec := cloudTestCompany()

	if err := o.Destroy(context.Background(), DestroyOptions{Lab: lab, Company: company, Spec: spec}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if destroyVars["project_id"] != testGCPProject {
		t.Errorf("destroy vars = %v, want the recorded project", destroyVars)
	}

	// A GCP lab with no recorded project cannot be torn down by terraform;
	// the destroy reports it rather than prompting.
	lab2 := newTestLab(types.CloudProviderGCP)
	lab2.Name = "test-lab-no-project"
	lab2.Status = types.LabStatusDestroying
	if err := mgr.CreateLab(context.Background(), lab2); err != nil {
		t.Fatal(err)
	}
	err := o.Destroy(context.Background(), DestroyOptions{Lab: lab2, Company: company, Spec: spec})
	if err == nil || !strings.Contains(err.Error(), "gcp.project_id") {
		t.Errorf("Destroy without a project = %v, want the project error", err)
	}
}

func TestCreate_CloudAzure_ObtainsKubeconfigAndInstallsPlatform(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newProviderLab(t, mgr, types.CloudProviderAzure)
	pulumi := &mockPulumiProv{upFn: func(context.Context, pulumiprov.UpOptions) (*pulumiprov.UpResult, error) {
		return &pulumiprov.UpResult{Outputs: aksOutputs(t, "prod", "staging")}, nil
	}}
	o, kctls := newProviderTestOrchestrator(t, mgr, func(d *Deps) { d.PulumiProv = pulumi })
	company, spec := cloudTestCompany()
	company.CloudProvider, company.IaCTool = types.CloudProviderAzure, types.IaCToolPulumi

	if err := o.Create(context.Background(), CreateOptions{Lab: lab, Company: company, Spec: spec}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cred, err := mgr.GetCredential(context.Background(), lab.ID, "kubeconfig")
	if err != nil {
		t.Fatalf("no kubeconfig credential stored: %v", err)
	}
	stored, err := clientcmd.Load([]byte(cred.EncryptedValue))
	if err != nil {
		t.Fatal(err)
	}
	if stored.CurrentContext != "techflow-lab-prod" || len(stored.Contexts) != 2 {
		t.Errorf("stored kubeconfig: current %q, %d contexts", stored.CurrentContext, len(stored.Contexts))
	}
	for name, a := range stored.AuthInfos {
		if a.Exec == nil || a.Exec.Command != "kubelogin" {
			t.Errorf("stored auth %s does not use kubelogin", name)
		}
	}
	assertClusterSteps(t, lab, kctls, 2)
}

func TestCreate_CloudAzure_MissingExportRollsBack(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newProviderLab(t, mgr, types.CloudProviderAzure)
	destroyed := false
	pulumi := &mockPulumiProv{
		upFn: func(context.Context, pulumiprov.UpOptions) (*pulumiprov.UpResult, error) {
			out := aksOutputs(t, "prod", "staging")
			delete(out, aksOutputKeys("prod").endpoint)
			return &pulumiprov.UpResult{Outputs: out}, nil
		},
		destroyFn: func(context.Context, pulumiprov.DestroyOptions) error {
			destroyed = true
			return nil
		},
	}
	o, _ := newProviderTestOrchestrator(t, mgr, func(d *Deps) { d.PulumiProv = pulumi })
	company, spec := cloudTestCompany()
	company.CloudProvider, company.IaCTool = types.CloudProviderAzure, types.IaCToolPulumi

	err := o.Create(context.Background(), CreateOptions{Lab: lab, Company: company, Spec: spec})
	if err == nil || !strings.Contains(err.Error(), aksOutputKeys("prod").endpoint) {
		t.Fatalf("Create error = %v, want one naming the missing export", err)
	}
	if !destroyed {
		t.Error("pulumi destroy did not run on rollback")
	}
}
