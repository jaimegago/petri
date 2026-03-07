package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jaimegago/petri/pkg/types"
)

// CredentialBundle holds all connection details for a lab, suitable for
// handing to Joe (the LLM copilot).
type CredentialBundle struct {
	// LabName identifies the lab.
	LabName string `json:"lab_name"`
	// Company is the lab's company profile.
	Company string `json:"company"`
	// Level is the complexity level.
	Level int `json:"level"`
	// Provider is the cloud provider.
	Provider string `json:"provider"`
	// ExpiresAt is when the lab will be auto-destroyed.
	ExpiresAt time.Time `json:"expires_at"`
	// Kubeconfig is the raw kubeconfig YAML for the primary cluster.
	Kubeconfig string `json:"kubeconfig,omitempty"`
	// GitRepos lists all git repositories created for the lab.
	GitRepos []types.GitRepo `json:"git_repos,omitempty"`
	// GitToken is the GitHub PAT (if available).
	GitToken string `json:"git_token,omitempty"`
	// ExtraCredentials holds any additional named credentials.
	ExtraCredentials map[string]string `json:"extra_credentials,omitempty"`
}

// ExportCredentials reads all credentials for a lab, builds a CredentialBundle,
// JSON-encodes it, encrypts it with the master key, and writes it to outputPath.
func (o *Orchestrator) ExportCredentials(ctx context.Context, lab *types.Lab, outputPath string) error {
	bundle := &CredentialBundle{
		LabName:   lab.Name,
		Company:   lab.Company,
		Level:     lab.Level,
		Provider:  string(lab.CloudProvider),
		ExpiresAt: lab.ExpiresAt,
		GitRepos:  lab.Metadata.GitRepos,
	}

	// Retrieve and decrypt the kubeconfig credential.
	kc, err := o.deps.State.GetCredential(ctx, lab.ID, "kubeconfig")
	if err == nil {
		decrypted, decErr := o.deps.Cipher.Decrypt(kc.EncryptedValue)
		if decErr != nil {
			o.log.Warn("Failed to decrypt kubeconfig credential", "error", decErr)
		} else {
			bundle.Kubeconfig = string(decrypted)
		}
	}

	// Retrieve any git token credential.
	gt, err := o.deps.State.GetCredential(ctx, lab.ID, "github_token")
	if err == nil {
		decrypted, decErr := o.deps.Cipher.Decrypt(gt.EncryptedValue)
		if decErr != nil {
			o.log.Warn("Failed to decrypt github_token credential", "error", decErr)
		} else {
			bundle.GitToken = string(decrypted)
		}
	}

	// JSON-encode the bundle.
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling credential bundle: %w", err)
	}

	// Encrypt the bundle.
	encrypted, err := o.deps.Cipher.Encrypt(raw)
	if err != nil {
		return fmt.Errorf("encrypting bundle: %w", err)
	}

	// Write to output file with restrictive permissions.
	if err := os.WriteFile(outputPath, []byte(encrypted), 0o600); err != nil {
		return fmt.Errorf("writing bundle to %s: %w", outputPath, err)
	}

	fmt.Printf("Credentials exported to %s\n", outputPath)
	fmt.Printf("  Lab:      %s\n", lab.Name)
	fmt.Printf("  Expires:  %s\n", lab.ExpiresAt.Format("2006-01-02 15:04 UTC"))
	if bundle.Kubeconfig != "" {
		fmt.Printf("  Contents: kubeconfig\n")
	}
	if len(bundle.GitRepos) > 0 {
		fmt.Printf("  Git repos: %d\n", len(bundle.GitRepos))
	}
	fmt.Println("\nDecrypt with: petri decrypt-bundle --input", outputPath)
	return nil
}
