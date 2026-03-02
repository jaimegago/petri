package commits_test

import (
	"context"
	"testing"
	"time"

	"github.com/jaimegago/petri/pkg/generators/commits"
	"github.com/jaimegago/petri/pkg/types"
)

func testCompany() *types.Company {
	return &types.Company{
		Name:          "acme",
		CloudProvider: types.CloudProviderAWS,
		IaCTool:       types.IaCToolTerraform,
		GitOpsTool:    types.GitOpsToolArgoCD,
		Authors: []types.Author{
			{Name: "Sarah Chen", Email: "sarah@acme.corp", Role: "platform_lead"},
			{Name: "Mike Rodriguez", Email: "mike@acme.corp", Role: "sre"},
		},
	}
}

func TestGenerate_InfraRepo(t *testing.T) {
	gen := commits.New()
	specs, err := gen.Generate(context.Background(), commits.GenerateOptions{
		RepoType: commits.RepoTypeInfra,
		Company:  testCompany(),
		Level:    1,
		Seed:     42,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(specs) == 0 {
		t.Fatal("expected at least one commit spec")
	}

	// Verify each commit has required fields.
	for i, s := range specs {
		if s.Message == "" {
			t.Errorf("commit %d has empty message", i)
		}
		if s.Author.Name == "" {
			t.Errorf("commit %d has empty author name", i)
		}
		if s.Timestamp.IsZero() {
			t.Errorf("commit %d has zero timestamp", i)
		}
		if s.Timestamp.After(time.Now().Add(time.Minute)) {
			t.Errorf("commit %d timestamp is in the future: %v", i, s.Timestamp)
		}
	}
}

func TestGenerate_GitOpsRepo(t *testing.T) {
	gen := commits.New()
	specs, err := gen.Generate(context.Background(), commits.GenerateOptions{
		RepoType: commits.RepoTypeGitOps,
		Company:  testCompany(),
		Level:    2,
		Seed:     99,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(specs) < 5 {
		t.Errorf("expected at least 5 commits, got %d", len(specs))
	}
}

func TestGenerate_AppsRepo(t *testing.T) {
	gen := commits.New()
	specs, err := gen.Generate(context.Background(), commits.GenerateOptions{
		RepoType: commits.RepoTypeApps,
		Company:  testCompany(),
		Level:    1,
		Seed:     7,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(specs) == 0 {
		t.Fatal("expected commits for apps repo")
	}
}

func TestGenerate_Level3_HasMoreCommits(t *testing.T) {
	gen := commits.New()

	level1, err := gen.Generate(context.Background(), commits.GenerateOptions{
		RepoType: commits.RepoTypeInfra,
		Company:  testCompany(),
		Level:    1,
		Seed:     1,
	})
	if err != nil {
		t.Fatalf("level1 Generate: %v", err)
	}

	level3, err := gen.Generate(context.Background(), commits.GenerateOptions{
		RepoType: commits.RepoTypeInfra,
		Company:  testCompany(),
		Level:    3,
		Seed:     1,
	})
	if err != nil {
		t.Fatalf("level3 Generate: %v", err)
	}

	if len(level3) <= len(level1) {
		t.Errorf("expected level 3 to have more commits than level 1: level1=%d level3=%d",
			len(level1), len(level3))
	}
}

func TestGenerate_Reproducible(t *testing.T) {
	gen := commits.New()
	opts := commits.GenerateOptions{
		RepoType: commits.RepoTypeInfra,
		Company:  testCompany(),
		Level:    2,
		Seed:     12345,
	}

	first, err := gen.Generate(context.Background(), opts)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	second, err := gen.Generate(context.Background(), opts)
	if err != nil {
		t.Fatalf("second Generate: %v", err)
	}

	if len(first) != len(second) {
		t.Fatalf("non-reproducible: different commit counts %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Message != second[i].Message {
			t.Errorf("commit %d message differs: %q vs %q", i, first[i].Message, second[i].Message)
		}
		if first[i].Author.Email != second[i].Author.Email {
			t.Errorf("commit %d author differs", i)
		}
	}
}

func TestGenerate_AuthorsFromCompany(t *testing.T) {
	gen := commits.New()
	company := testCompany()
	authorEmails := map[string]bool{}
	for _, a := range company.Authors {
		authorEmails[a.Email] = true
	}

	specs, err := gen.Generate(context.Background(), commits.GenerateOptions{
		RepoType: commits.RepoTypeInfra,
		Company:  company,
		Level:    1,
		Seed:     77,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	for _, s := range specs {
		if !authorEmails[s.Author.Email] {
			t.Errorf("commit author %q not in company authors", s.Author.Email)
		}
	}
}

func TestGenerate_NilCompany_Error(t *testing.T) {
	gen := commits.New()
	_, err := gen.Generate(context.Background(), commits.GenerateOptions{
		RepoType: commits.RepoTypeInfra,
		Company:  nil,
		Level:    1,
	})
	if err == nil {
		t.Fatal("expected error for nil company")
	}
}

func TestGenerate_NoAuthors_Error(t *testing.T) {
	gen := commits.New()
	company := testCompany()
	company.Authors = nil
	_, err := gen.Generate(context.Background(), commits.GenerateOptions{
		RepoType: commits.RepoTypeInfra,
		Company:  company,
		Level:    1,
	})
	if err == nil {
		t.Fatal("expected error for company with no authors")
	}
}

func TestGenerate_TimestampsChronological(t *testing.T) {
	gen := commits.New()
	specs, err := gen.Generate(context.Background(), commits.GenerateOptions{
		RepoType: commits.RepoTypeInfra,
		Company:  testCompany(),
		Level:    2,
		Seed:     55,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Timestamps should all be in the past.
	now := time.Now().Add(time.Minute) // small buffer
	for i, s := range specs {
		if s.Timestamp.After(now) {
			t.Errorf("commit %d timestamp %v is in the future", i, s.Timestamp)
		}
	}
}
