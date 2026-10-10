package iac_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jaimegago/petri/pkg/generators"
	"github.com/jaimegago/petri/pkg/generators/iac"
)

// minimalFS builds an in-memory filesystem with simple templates for testing.
func minimalFS(dir string, files []string) fstest.MapFS {
	m := make(fstest.MapFS)
	for _, f := range files {
		m[dir+"/"+f] = &fstest.MapFile{
			Data: []byte("# {{ .Company.Name }} {{ .Lab.Name }} level={{ .Level.Number }}\n"),
		}
	}
	return m
}

func baseContext(iacTool, cloud string, level int) generators.TemplateContext {
	return generators.TemplateContext{
		Lab: generators.LabInfo{
			ID:        uuid.New().String(),
			Name:      "test-lab",
			LabType:   cloud,
			CreatedAt: time.Now().Format(time.RFC3339),
		},
		Company: generators.CompanyInfo{
			Name:          "acme",
			CloudProvider: cloud,
			IaCTool:       iacTool,
			GitHubOrg:     "acme-org",
		},
		Level: generators.LevelInfo{
			Number:        level,
			ClusterNames:  []string{"prod"},
			NodeCounts:    []int{3},
			InstanceTypes: []string{"t3.medium"},
			Apps:          []string{"frontend"},
		},
		Now: time.Now(),
	}
}

func TestGenerate_Terraform_AWS(t *testing.T) {
	tfFiles := []string{"provider.tf.tmpl", "variables.tf.tmpl", "main.tf.tmpl", "outputs.tf.tmpl"}
	testFS := minimalFS("terraform/aws", tfFiles)
	gen := iac.NewWithFS(testFS)

	ctx := baseContext("terraform", "aws", 1)
	files, err := gen.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != len(tfFiles) {
		t.Errorf("expected %d files, got %d", len(tfFiles), len(files))
	}

	// Verify extensions stripped and content rendered.
	for _, f := range files {
		if strings.HasSuffix(f.Path, ".tmpl") {
			t.Errorf("output path still has .tmpl suffix: %s", f.Path)
		}
		if !strings.Contains(f.Content, "acme") {
			t.Errorf("rendered content missing company name in file %s", f.Path)
		}
	}
}

func TestGenerate_Terraform_GCP(t *testing.T) {
	tfFiles := []string{"provider.tf.tmpl", "variables.tf.tmpl", "main.tf.tmpl", "outputs.tf.tmpl"}
	testFS := minimalFS("terraform/gcp", tfFiles)
	gen := iac.NewWithFS(testFS)

	ctx := baseContext("terraform", "gcp", 2)
	ctx.Level.ClusterNames = []string{"prod", "staging"}
	ctx.Level.NodeCounts = []int{4, 2}
	ctx.Level.InstanceTypes = []string{"n1-standard-4", "n1-standard-2"}

	files, err := gen.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != len(tfFiles) {
		t.Errorf("expected %d files, got %d", len(tfFiles), len(files))
	}
}

func TestGenerate_Pulumi_Azure(t *testing.T) {
	pulumiFiles := []string{"Pulumi.yaml.tmpl", "index.ts.tmpl", "package.json.tmpl"}
	testFS := minimalFS("pulumi/azure", pulumiFiles)
	gen := iac.NewWithFS(testFS)

	ctx := baseContext("pulumi", "azure", 1)
	files, err := gen.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != len(pulumiFiles) {
		t.Errorf("expected %d files, got %d", len(pulumiFiles), len(files))
	}
}

func TestGenerate_UnsupportedTool(t *testing.T) {
	gen := iac.NewWithFS(fstest.MapFS{})
	ctx := baseContext("cloudformation", "aws", 1)
	_, err := gen.Generate(context.Background(), ctx)
	if err == nil {
		t.Fatal("expected error for unsupported IaC tool")
	}
	if !strings.Contains(err.Error(), "unsupported IaC tool") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGenerate_MissingTemplate(t *testing.T) {
	gen := iac.NewWithFS(fstest.MapFS{}) // empty FS
	ctx := baseContext("terraform", "aws", 1)
	_, err := gen.Generate(context.Background(), ctx)
	if err == nil {
		t.Fatal("expected error when template is missing")
	}
}

func TestGenerate_Terraform_WithEmbeddedFS(t *testing.T) {
	// Integration-style: use the real embedded templates.
	gen := iac.New()

	tests := []struct {
		name  string
		cloud string
		tool  string
		level int
	}{
		{"aws-level1", "aws", "terraform", 1},
		{"aws-level2", "aws", "terraform", 2},
		{"gcp-level1", "gcp", "terraform", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := baseContext(tt.tool, tt.cloud, tt.level)
			if tt.level == 2 {
				ctx.Level.ClusterNames = []string{"prod", "staging"}
				ctx.Level.NodeCounts = []int{4, 2}
				ctx.Level.InstanceTypes = []string{"t3.large", "t3.medium"}
				ctx.Level.Databases = []generators.DatabaseInfo{
					{Type: "postgresql", EngineVersion: "15", InstanceClass: "db.t3.small"},
					{Type: "redis", EngineVersion: "7.0", NodeType: "cache.t3.micro"},
				}
			}
			files, err := gen.Generate(context.Background(), ctx)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if len(files) == 0 {
				t.Error("expected at least one rendered file")
			}
			for _, f := range files {
				if f.Content == "" {
					t.Errorf("file %s has empty content", f.Path)
				}
			}
		})
	}
}

// TestAWSDatastoreIngress holds RDS and ElastiCache to their own security
// groups, admitting their port only from every cluster's EKS node security
// group: never the VPC's ingress-less default group, never a CIDR.
func TestAWSDatastoreIngress(t *testing.T) {
	ctx := baseContext("terraform", "aws", 3)
	ctx.Level.ClusterNames = []string{"prod", "staging", "management"}
	ctx.Level.NodeCounts = []int{4, 2, 2}
	ctx.Level.InstanceTypes = []string{"t3.xlarge", "t3.large", "t3.medium"}
	ctx.Level.Databases = []generators.DatabaseInfo{
		{Type: "postgresql", EngineVersion: "15", InstanceClass: "db.t3.medium"},
		{Type: "redis", EngineVersion: "7.0", NodeType: "cache.t3.small"},
	}
	mainTF := func(tc generators.TemplateContext) string {
		t.Helper()
		files, err := iac.New().Generate(context.Background(), tc)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		for _, f := range files {
			if f.Path == "main.tf" {
				return f.Content
			}
		}
		t.Fatal("no main.tf rendered")
		return ""
	}

	main := mainTF(ctx)
	for _, forbidden := range []string{"default_security_group_id", "cidr_ipv4", "cidr_blocks", "0.0.0.0/0"} {
		if strings.Contains(main, forbidden) {
			t.Errorf("main.tf contains %q; datastore ingress must come from node security groups only", forbidden)
		}
	}
	for _, want := range []string{
		`vpc_security_group_ids = [aws_security_group.rds.id]`,
		`security_group_ids   = [aws_security_group.redis.id]`,
		`resource "aws_security_group" "rds"`,
		`resource "aws_security_group" "redis"`,
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.tf missing %q", want)
		}
	}
	for _, db := range []struct{ sg, port string }{{"rds", "5432"}, {"redis", "6379"}} {
		for _, c := range ctx.Level.ClusterNames {
			rule := `resource "aws_vpc_security_group_ingress_rule" "` + db.sg + `_from_eks_` + c + `" {`
			i := strings.Index(main, rule)
			if i < 0 {
				t.Errorf("main.tf missing %s ingress rule for cluster %s", db.sg, c)
				continue
			}
			body := main[i : i+strings.Index(main[i:], "\n}")]
			for _, want := range []string{
				"aws_security_group." + db.sg + ".id",
				"module.eks_" + c + ".node_security_group_id",
				"from_port                    = " + db.port,
				"to_port                      = " + db.port,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("%s rule for %s missing %q", db.sg, c, want)
				}
			}
		}
	}

	ctx.Level.Databases = ctx.Level.Databases[:1]
	if strings.Contains(mainTF(ctx), `"aws_security_group" "redis"`) {
		t.Error("main.tf declares a Redis security group for a level with no Redis")
	}
}

func TestGenerate_Pulumi_WithEmbeddedFS(t *testing.T) {
	gen := iac.New()
	ctx := baseContext("pulumi", "azure", 1)
	ctx.Level.Databases = []generators.DatabaseInfo{
		{Type: "postgresql", EngineVersion: "15", InstanceClass: "Standard_B2s"},
	}

	files, err := gen.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) == 0 {
		t.Error("expected rendered files")
	}
}
