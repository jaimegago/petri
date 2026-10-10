package orchestrator

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jaimegago/petri/pkg/crypto"
	"github.com/jaimegago/petri/pkg/generators"
	"github.com/jaimegago/petri/pkg/generators/iac"
	"github.com/jaimegago/petri/pkg/logger"
	pulumiprov "github.com/jaimegago/petri/pkg/provisioners/pulumi"
	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

func TestGenerateDBPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p, err := generateDBPassword(crand.Reader)
		if err != nil {
			t.Fatalf("generateDBPassword: %v", err)
		}
		if len(p) != dbPasswordLength {
			t.Errorf("length %d, want %d", len(p), dbPasswordLength)
		}
		if !hasAllPasswordClasses(p) {
			t.Error("password misses a character class")
		}
		for _, c := range p {
			if !strings.ContainsRune(dbPasswordAlphabet, c) {
				t.Errorf("character %q outside the alphabet", c)
			}
		}
		if seen[p] {
			t.Error("password repeated across draws")
		}
		seen[p] = true
	}
}

func TestGenerateDBPassword_Errors(t *testing.T) {
	tests := []struct {
		name    string
		r       io.Reader
		wantErr string
	}{
		{name: "randomness unavailable", r: errReader{}, wantErr: "reading randomness"},
		// An all-zero source draws the first alphabet character every time,
		// so no draw ever holds every class.
		{name: "source never yields every class", r: zeroReader{}, wantErr: "every character class"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := generateDBPassword(tt.r)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestHasAllPasswordClasses(t *testing.T) {
	tests := map[string]bool{
		"Aa1-":      true,
		"Aa1_":      true,
		"Aa1.":      true,
		"aa1-":      false,
		"AA1-":      false,
		"Aab-":      false,
		"Aa12":      false,
		"Aa1!":      false, // "!" is not one of the symbols petri draws from
		"":          false,
		"zZ9.extra": true,
	}
	for in, want := range tests {
		if got := hasAllPasswordClasses(in); got != want {
			t.Errorf("hasAllPasswordClasses(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestNeedsDBPassword(t *testing.T) {
	tests := []struct {
		name string
		dbs  []types.Database
		want bool
	}{
		{name: "no databases", want: false},
		{name: "redis only", dbs: []types.Database{{Type: "redis"}}, want: false},
		{name: "postgres", dbs: []types.Database{{Type: "postgresql"}}, want: true},
		{name: "postgres and redis", dbs: []types.Database{{Type: "redis"}, {Type: "postgresql"}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsDBPassword(types.LevelSpec{Databases: tt.dbs}); got != tt.want {
				t.Errorf("needsDBPassword = %v, want %v", got, tt.want)
			}
		})
	}
}

// azurePostgresCreate runs an Azure create whose level declares Postgres,
// with a real AES-256-GCM cipher and a captured log.
func azurePostgresCreate(t *testing.T, pulumi *mockPulumiProv, withPostgres bool) (*types.Lab, state.Manager, crypto.Cipher, string, error) {
	t.Helper()
	cipher, err := crypto.NewAESCipherFromKey(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	mgr := state.NewMockManager()
	lab := newProviderLab(t, mgr, types.CloudProviderAzure)
	if pulumi.upFn == nil {
		pulumi.upFn = func(context.Context, pulumiprov.UpOptions) (*pulumiprov.UpResult, error) {
			return &pulumiprov.UpResult{Outputs: aksOutputs(t, "prod", "staging")}, nil
		}
	}
	o, _ := newProviderTestOrchestrator(t, mgr, func(d *Deps) {
		d.PulumiProv = pulumi
		d.Cipher = cipher
		d.Log = logger.NewConsole("debug", &logs)
	})
	company, spec := cloudTestCompany()
	company.CloudProvider, company.IaCTool = types.CloudProviderAzure, types.IaCToolPulumi
	if withPostgres {
		spec.Databases = []types.Database{{Type: "postgresql", EngineVersion: "15", InstanceClass: "Standard_B2s"}}
	}
	err = o.Create(context.Background(), CreateOptions{Lab: lab, Company: company, Spec: spec})
	return lab, mgr, cipher, logs.String(), err
}

func TestCreate_CloudAzure_PostgresPasswordIsSecretConfigAndEncrypted(t *testing.T) {
	pulumi := &mockPulumiProv{}
	lab, mgr, cipher, logs, err := azurePostgresCreate(t, pulumi, true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if want := []string{"init", "config", "up"}; !slices.Equal(pulumi.calls, want) {
		t.Fatalf("pulumi calls = %v, want %v", pulumi.calls, want)
	}
	set := pulumi.secrets[0]
	if set.Key != dbPasswordConfigKey || set.StackName != lab.Name {
		t.Errorf("secret config key %q on stack %q, want %q on %q", set.Key, set.StackName, dbPasswordConfigKey, lab.Name)
	}
	if !hasAllPasswordClasses(set.Value) || len(set.Value) != dbPasswordLength {
		t.Error("secret config value is not a generated password")
	}

	cred, err := mgr.GetCredential(context.Background(), lab.ID, dbPasswordCredential)
	if err != nil {
		t.Fatalf("no %s credential stored: %v", dbPasswordCredential, err)
	}
	if strings.Contains(cred.EncryptedValue, set.Value) {
		t.Error("credential is stored in the clear")
	}
	plain, err := cipher.Decrypt(cred.EncryptedValue)
	if err != nil {
		t.Fatalf("decrypting stored credential: %v", err)
	}
	if string(plain) != set.Value {
		t.Error("stored credential does not decrypt to the value passed to pulumi")
	}
	if strings.Contains(logs, set.Value) {
		t.Error("password appears in the log")
	}
}

func TestCreate_CloudAzure_PasswordPerLab(t *testing.T) {
	a, b := &mockPulumiProv{}, &mockPulumiProv{}
	if _, _, _, _, err := azurePostgresCreate(t, a, true); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := azurePostgresCreate(t, b, true); err != nil {
		t.Fatal(err)
	}
	if a.secrets[0].Value == b.secrets[0].Value {
		t.Error("two labs got the same password")
	}
}

func TestCreate_CloudAzure_NoPostgresNoPassword(t *testing.T) {
	pulumi := &mockPulumiProv{}
	lab, mgr, _, _, err := azurePostgresCreate(t, pulumi, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(pulumi.secrets) != 0 {
		t.Errorf("SetSecretConfig called %d times without a Postgres database", len(pulumi.secrets))
	}
	if _, err := mgr.GetCredential(context.Background(), lab.ID, dbPasswordCredential); err == nil {
		t.Error("password credential stored without a Postgres database")
	}
}

func TestCreate_CloudAzure_PasswordConfigFailureRollsBack(t *testing.T) {
	destroyed := false
	pulumi := &mockPulumiProv{
		setSecretFn: func(context.Context, pulumiprov.SecretConfigOptions) error {
			return errors.New("backend unreachable")
		},
		destroyFn: func(context.Context, pulumiprov.DestroyOptions) error {
			destroyed = true
			return nil
		},
	}
	_, _, _, logs, err := azurePostgresCreate(t, pulumi, true)
	if err == nil || !strings.Contains(err.Error(), "setting postgres password config") {
		t.Fatalf("Create error = %v, want the config failure", err)
	}
	if slices.Contains(pulumi.calls, "up") {
		t.Error("pulumi up ran after the password config failed")
	}
	if !destroyed {
		t.Error("pulumi destroy did not run on rollback")
	}
	value := pulumi.secrets[0].Value
	if strings.Contains(err.Error(), value) || strings.Contains(logs, value) {
		t.Error("password appears in the error or the log")
	}
}

// TestAzureProgramReadsPasswordAsSecretConfig holds the real Azure program
// and the orchestrator in step: the key petri sets is the key the program
// reads, and it reads it as a secret.
func TestAzureProgramReadsPasswordAsSecretConfig(t *testing.T) {
	files, err := iac.New().Generate(context.Background(), generators.TemplateContext{
		Lab:     generators.LabInfo{Name: "lab"},
		Company: generators.CompanyInfo{Name: "co", CloudProvider: "azure", IaCTool: "pulumi"},
		Level: generators.LevelInfo{
			Number:        1,
			ClusterNames:  []string{"prod"},
			NodeCounts:    []int{2},
			InstanceTypes: []string{"Standard_D2s_v3"},
			Databases:     []generators.DatabaseInfo{{Type: "postgresql", EngineVersion: "15", InstanceClass: "Standard_B2s"}},
		},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("rendering azure program: %v", err)
	}
	var program string
	for _, f := range files {
		if f.Path == "index.ts" {
			program = f.Content
		}
	}
	if !strings.Contains(program, `administratorLoginPassword: config.requireSecret("`+dbPasswordConfigKey+`")`) {
		t.Errorf("index.ts does not read the admin password as secret config %q", dbPasswordConfigKey)
	}
	reads := regexp.MustCompile(`config\.(\w+)\("`+dbPasswordConfigKey+`"\)`).FindAllStringSubmatch(program, -1)
	for _, r := range reads {
		if r[1] != "requireSecret" {
			t.Errorf("index.ts reads %q through config.%s", dbPasswordConfigKey, r[1])
		}
	}
	if !strings.Contains(program, "azure.dbforpostgresql.v20240801.Server(") {
		t.Error("index.ts does not create a flexible server at API 2024-08-01")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
