package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/jaimegago/petri/pkg/generators"
	"github.com/jaimegago/petri/pkg/generators/iac"
	"github.com/jaimegago/petri/pkg/logger"
	tfprov "github.com/jaimegago/petri/pkg/provisioners/terraform"
	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

// testCA stands in for a cluster CA certificate. Tests assert it never
// appears in an error message.
var testCA = []byte("-----BEGIN CERTIFICATE-----\nZmFrZS1jYQ==\n-----END CERTIFICATE-----\n")

func strOutput(t *testing.T, v string) tfprov.OutputValue {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return tfprov.OutputValue{Value: raw}
}

// eksOutputs returns the outputs the AWS template emits for the given clusters.
func eksOutputs(t *testing.T, shorts ...string) map[string]tfprov.OutputValue {
	t.Helper()
	out := map[string]tfprov.OutputValue{awsRegionOutput: strOutput(t, "eu-west-1")}
	for _, s := range shorts {
		nameKey, endpointKey, caKey := eksOutputKeys(s)
		out[nameKey] = strOutput(t, "acme-lab-"+s)
		out[endpointKey] = strOutput(t, "https://"+s+".eks.example")
		out[caKey] = strOutput(t, base64.StdEncoding.EncodeToString(testCA))
	}
	return out
}

func TestEKSClustersFromOutputs(t *testing.T) {
	clusters, err := eksClustersFromOutputs(eksOutputs(t, "prod", "staging"), []string{"prod", "staging"}, []int{4, 2})
	if err != nil {
		t.Fatalf("eksClustersFromOutputs: %v", err)
	}
	if len(clusters) != 2 {
		t.Fatalf("got %d clusters, want 2", len(clusters))
	}

	for i, short := range []string{"prod", "staging"} {
		c := clusters[i]
		wantName := "acme-lab-" + short
		if c.Name != wantName || c.Endpoint != "https://"+short+".eks.example" {
			t.Errorf("cluster %d = %q at %q", i, c.Name, c.Endpoint)
		}
		if want := []int{4, 2}[i]; c.NodeCount != want {
			t.Errorf("cluster %s NodeCount = %d, want %d", short, c.NodeCount, want)
		}

		cfg, err := clientcmd.Load(c.Kubeconfig)
		if err != nil {
			t.Fatalf("kubeconfig for %s does not parse: %v", short, err)
		}
		if cfg.CurrentContext != wantName || len(cfg.Contexts) != 1 {
			t.Errorf("kubeconfig for %s: current %q, %d contexts", short, cfg.CurrentContext, len(cfg.Contexts))
		}
		cl := cfg.Clusters[wantName]
		if cl == nil || cl.Server != c.Endpoint || string(cl.CertificateAuthorityData) != string(testCA) {
			t.Errorf("kubeconfig for %s: cluster entry wrong", short)
		}
		auth := cfg.AuthInfos[wantName]
		if auth == nil || auth.Exec == nil {
			t.Fatalf("kubeconfig for %s: no exec auth", short)
		}
		if auth.Token != "" || auth.ClientKeyData != nil {
			t.Errorf("kubeconfig for %s carries a static credential", short)
		}
		gotArgs := strings.Join(auth.Exec.Args, " ")
		wantArgs := "eks get-token --cluster-name " + wantName + " --region eu-west-1 --output json"
		if auth.Exec.Command != "aws" || gotArgs != wantArgs || auth.Exec.APIVersion != eksExecAPIVersion {
			t.Errorf("exec = %s %s (%s)", auth.Exec.Command, gotArgs, auth.Exec.APIVersion)
		}
	}
}

func TestEKSClustersFromOutputs_Errors(t *testing.T) {
	nameKey, endpointKey, caKey := eksOutputKeys("prod")
	notB64 := "not base64 !!"
	tests := []struct {
		name    string
		mutate  func(map[string]tfprov.OutputValue)
		wantErr string
	}{
		{"missing region", func(o map[string]tfprov.OutputValue) { delete(o, awsRegionOutput) }, awsRegionOutput},
		{"missing name", func(o map[string]tfprov.OutputValue) { delete(o, nameKey) }, nameKey},
		{"missing endpoint", func(o map[string]tfprov.OutputValue) { delete(o, endpointKey) }, endpointKey},
		{"missing certificate", func(o map[string]tfprov.OutputValue) { delete(o, caKey) }, caKey},
		{"empty endpoint", func(o map[string]tfprov.OutputValue) { o[endpointKey] = strOutput(t, "") }, endpointKey},
		{"non-string name", func(o map[string]tfprov.OutputValue) {
			o[nameKey] = tfprov.OutputValue{Value: json.RawMessage(`["x"]`)}
		}, nameKey},
		{"certificate not base64", func(o map[string]tfprov.OutputValue) { o[caKey] = strOutput(t, notB64) }, caKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputs := eksOutputs(t, "prod")
			tt.mutate(outputs)
			_, err := eksClustersFromOutputs(outputs, []string{"prod"}, []int{1})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not name %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), notB64) || strings.Contains(err.Error(), "BEGIN CERTIFICATE") {
				t.Errorf("error echoes an output value: %q", err)
			}
		})
	}

	if _, err := eksClustersFromOutputs(eksOutputs(t), nil, nil); err == nil {
		t.Error("expected an error for a level spec with no clusters")
	}
}

func TestMergeKubeconfigs(t *testing.T) {
	clusters, err := eksClustersFromOutputs(eksOutputs(t, "prod", "staging"), []string{"prod", "staging"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := mergeKubeconfigs(clusters)
	if err != nil {
		t.Fatalf("mergeKubeconfigs: %v", err)
	}
	cfg, err := clientcmd.Load(merged)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentContext != "acme-lab-prod" {
		t.Errorf("current context = %q, want the primary cluster", cfg.CurrentContext)
	}
	for _, n := range []string{"acme-lab-prod", "acme-lab-staging"} {
		if cfg.Contexts[n] == nil || cfg.Clusters[n] == nil || cfg.AuthInfos[n] == nil {
			t.Errorf("merged kubeconfig lacks %s", n)
		}
	}

	if _, err := mergeKubeconfigs([]provisionedCluster{{Name: "x", Kubeconfig: []byte("{{{")}}); err == nil {
		t.Error("expected an error for an unparseable kubeconfig")
	}
}

// TestAWSTemplateDeclaresKubeconfigOutputs holds the real AWS template and the
// kubeconfig assembly in step: every output eksClustersFromOutputs reads is
// declared, and the database subnets the RDS module is placed in exist.
func TestAWSTemplateDeclaresKubeconfigOutputs(t *testing.T) {
	tmplCtx := generators.TemplateContext{
		Lab:     generators.LabInfo{Name: "lab"},
		Company: generators.CompanyInfo{Name: "acme", CloudProvider: "aws", IaCTool: "terraform"},
		Level: generators.LevelInfo{
			Number:        2,
			ClusterNames:  []string{"prod", "staging"},
			NodeCounts:    []int{4, 2},
			InstanceTypes: []string{"t3.large", "t3.medium"},
			Databases: []generators.DatabaseInfo{
				{Type: "postgresql", EngineVersion: "15", InstanceClass: "db.t3.small"},
			},
		},
		Now: time.Now(),
	}
	render := func(tc generators.TemplateContext) map[string]string {
		t.Helper()
		files, err := iac.New().Generate(context.Background(), tc)
		if err != nil {
			t.Fatalf("rendering AWS template: %v", err)
		}
		byPath := map[string]string{}
		for _, f := range files {
			byPath[f.Path] = f.Content
		}
		return byPath
	}

	files := render(tmplCtx)
	outputs := files["outputs.tf"]
	keys := []string{awsRegionOutput}
	for _, s := range tmplCtx.Level.ClusterNames {
		n, e, c := eksOutputKeys(s)
		keys = append(keys, n, e, c)
	}
	for _, k := range keys {
		if !strings.Contains(outputs, fmt.Sprintf("output %q", k)) {
			t.Errorf("outputs.tf does not declare %q", k)
		}
	}

	main := files["main.tf"]
	if !strings.Contains(main, "database_subnets") || !strings.Contains(main, "module.vpc.database_subnet_group_name") {
		t.Error("main.tf places RDS in a subnet group the VPC does not declare")
	}
	if !strings.Contains(main, "enable_cluster_creator_admin_permissions = true") {
		t.Error("main.tf does not grant the terraform identity cluster access")
	}

	tmplCtx.Level.Databases = nil
	if strings.Contains(render(tmplCtx)["main.tf"], "database_subnets") {
		t.Error("main.tf declares database subnets for a level with no RDS")
	}
}

// recordingKubectl is a per-kubeconfig kubectl mock.
type recordingKubectl struct {
	mockKubectl
	waited int
}

func (m *recordingKubectl) WaitForNodes(_ context.Context, _ time.Duration) error {
	m.waited++
	return nil
}

func newCloudTestOrchestrator(t *testing.T, mgr state.Manager, tf *mockTFProv) (*Orchestrator, map[string]*recordingKubectl) {
	t.Helper()
	kctls := map[string]*recordingKubectl{}
	return New(Config{WorkDir: t.TempDir()}, Deps{
		State:   mgr,
		Cipher:  &mockCipher{},
		Log:     logger.Nop(),
		GitProv: &mockGitProv{},
		TFProv:  tf,
		KubectlFactory: func(path string) KubectlClient {
			if kctls[path] == nil {
				kctls[path] = &recordingKubectl{}
			}
			return kctls[path]
		},
		AppsGen:          &mockAppsGen{},
		PlatformGen:      &mockPlatformGen{},
		ObservabilityGen: &mockObsGen{},
	}), kctls
}

func cloudTestCompany() (*types.Company, types.LevelSpec) {
	company := newTestCompany()
	spec := types.LevelSpec{
		Clusters:        2,
		ClusterNames:    []string{"prod", "staging"},
		NodesPerCluster: []int{4, 2},
		Apps:            []string{"frontend"},
		Namespaces:      []string{"default"},
	}
	return company, spec
}

func TestCreate_CloudAWS_ObtainsKubeconfigAndInstallsPlatform(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newTestLab(types.CloudProviderAWS)
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}
	tf := &mockTFProv{
		applyFn: func(_ context.Context, _ tfprov.ApplyOptions) (*tfprov.ApplyResult, error) {
			return &tfprov.ApplyResult{Outputs: eksOutputs(t, "prod", "staging")}, nil
		},
	}
	o, kctls := newCloudTestOrchestrator(t, mgr, tf)
	company, spec := cloudTestCompany()

	if err := o.Create(context.Background(), CreateOptions{Lab: lab, Company: company, Spec: spec}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if lab.Status != types.LabStatusActive {
		t.Errorf("status = %s, want ACTIVE", lab.Status)
	}

	// The merged kubeconfig is stored as the lab's encrypted credential.
	cred, err := mgr.GetCredential(context.Background(), lab.ID, "kubeconfig")
	if err != nil {
		t.Fatalf("no kubeconfig credential stored: %v", err)
	}
	stored, err := clientcmd.Load([]byte(cred.EncryptedValue)) // mockCipher is the identity
	if err != nil {
		t.Fatal(err)
	}
	if stored.CurrentContext != "acme-lab-prod" || len(stored.Contexts) != 2 {
		t.Errorf("stored kubeconfig: current %q, %d contexts", stored.CurrentContext, len(stored.Contexts))
	}

	// Each cluster is recorded with a 0600 kubeconfig, and gets the local
	// path's treatment: node wait, platform, observability, apps.
	if len(lab.Metadata.Clusters) != 2 {
		t.Fatalf("recorded %d clusters, want 2", len(lab.Metadata.Clusters))
	}
	for _, c := range lab.Metadata.Clusters {
		fi, err := os.Stat(c.KubeconfigPath)
		if err != nil {
			t.Fatalf("kubeconfig for %s: %v", c.Name, err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("kubeconfig for %s mode = %v, want 0600", c.Name, fi.Mode().Perm())
		}
		k := kctls[c.KubeconfigPath]
		if k == nil {
			t.Fatalf("no kubectl client built for %s", c.Name)
		}
		if k.waited != 1 || len(k.applied) != 3 {
			t.Errorf("cluster %s: waited %d, applied %d manifests (want 1, 3)", c.Name, k.waited, len(k.applied))
		}
	}
	if lab.Metadata.Clusters[0].Endpoint != "https://prod.eks.example" || lab.Metadata.Clusters[0].NodeCount != 4 {
		t.Errorf("primary cluster metadata = %+v", lab.Metadata.Clusters[0])
	}
}

func TestCreate_CloudAWS_NoAppsSkipsAppManifests(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newTestLab(types.CloudProviderAWS)
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}
	tf := &mockTFProv{
		applyFn: func(_ context.Context, _ tfprov.ApplyOptions) (*tfprov.ApplyResult, error) {
			return &tfprov.ApplyResult{Outputs: eksOutputs(t, "prod", "staging")}, nil
		},
	}
	o, kctls := newCloudTestOrchestrator(t, mgr, tf)
	company, spec := cloudTestCompany()

	if err := o.Create(context.Background(), CreateOptions{Lab: lab, Company: company, Spec: spec, NoApps: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for path, k := range kctls {
		if len(k.applied) != 2 {
			t.Errorf("%s: applied %d manifests, want platform + observability only", path, len(k.applied))
		}
	}
}

func TestCreate_CloudAWS_MissingOutputRollsBack(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newTestLab(types.CloudProviderAWS)
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}
	destroyed := false
	tf := &mockTFProv{
		applyFn: func(_ context.Context, _ tfprov.ApplyOptions) (*tfprov.ApplyResult, error) {
			out := eksOutputs(t, "prod", "staging")
			delete(out, awsRegionOutput)
			return &tfprov.ApplyResult{Outputs: out}, nil
		},
		destroyFn: func(_ context.Context, _ tfprov.DestroyOptions) error {
			destroyed = true
			return nil
		},
	}
	o, _ := newCloudTestOrchestrator(t, mgr, tf)
	company, spec := cloudTestCompany()

	err := o.Create(context.Background(), CreateOptions{Lab: lab, Company: company, Spec: spec})
	if err == nil || !strings.Contains(err.Error(), awsRegionOutput) {
		t.Fatalf("Create error = %v, want one naming %q", err, awsRegionOutput)
	}
	if !destroyed {
		t.Error("terraform destroy did not run on rollback")
	}
	if lab.Status != types.LabStatusError {
		t.Errorf("status = %s, want ERROR", lab.Status)
	}
}

func TestCreate_CloudAWS_ApplyFailureRollsBack(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newTestLab(types.CloudProviderAWS)
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}
	destroyed := false
	tf := &mockTFProv{
		applyFn: func(_ context.Context, _ tfprov.ApplyOptions) (*tfprov.ApplyResult, error) {
			return nil, errors.New("partial apply")
		},
		destroyFn: func(_ context.Context, _ tfprov.DestroyOptions) error {
			destroyed = true
			return nil
		},
	}
	o, _ := newCloudTestOrchestrator(t, mgr, tf)
	company, spec := cloudTestCompany()

	if err := o.Create(context.Background(), CreateOptions{Lab: lab, Company: company, Spec: spec}); err == nil {
		t.Fatal("expected Create to fail")
	}
	if !destroyed {
		t.Error("a partial apply was not torn down")
	}
}

func TestCreate_CloudGCP_NoKubeconfigSkipsClusterSteps(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newTestLab(types.CloudProviderGCP)
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}
	o, kctls := newCloudTestOrchestrator(t, mgr, &mockTFProv{})
	company, spec := cloudTestCompany()

	if err := o.Create(context.Background(), CreateOptions{Lab: lab, Company: company, Spec: spec}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(kctls) != 0 || len(lab.Metadata.Clusters) != 0 {
		t.Errorf("cluster steps ran without a kubeconfig: %d clients, %d clusters", len(kctls), len(lab.Metadata.Clusters))
	}
	if _, err := mgr.GetCredential(context.Background(), lab.ID, "kubeconfig"); err == nil {
		t.Error("a kubeconfig credential was stored without a kubeconfig")
	}
}
