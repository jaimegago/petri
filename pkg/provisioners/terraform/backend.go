package terraform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BackendType identifies the Terraform remote state backend.
type BackendType string

const (
	// BackendLocal uses a local .tfstate file (default, no remote config needed).
	BackendLocal BackendType = "local"
	// BackendS3 stores state in an AWS S3 bucket.
	BackendS3 BackendType = "s3"
	// BackendGCS stores state in a GCP Cloud Storage bucket.
	BackendGCS BackendType = "gcs"
	// BackendAzureRM stores state in an Azure Storage container.
	BackendAzureRM BackendType = "azurerm"
)

// BackendConfig defines the remote state backend for a lab.
type BackendConfig struct {
	Type BackendType

	// S3 fields (AWS / BackendS3).
	S3Bucket string
	S3Key    string
	S3Region string

	// GCS fields (GCP / BackendGCS).
	GCSBucket string
	GCSPrefix string

	// AzureRM fields (Azure / BackendAzureRM).
	AzureStorageAccount string
	AzureContainer      string
	AzureKey            string
}

// writeBackendOverride writes a _petri_override.tf file into workDir that
// overrides the terraform backend block from the generated configuration.
// Terraform merges `*_override.tf` files last, so this takes precedence.
func writeBackendOverride(workDir string, cfg *BackendConfig) error {
	content := buildBackendHCL(cfg)
	path := filepath.Join(workDir, "_petri_override.tf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing backend override %s: %w", path, err)
	}
	return nil
}

// buildBackendHCL generates a terraform backend block in HCL for the given config.
func buildBackendHCL(cfg *BackendConfig) string {
	var b strings.Builder
	b.WriteString("# Managed by Petri — do not edit\n")
	b.WriteString("terraform {\n")

	switch cfg.Type {
	case BackendS3:
		b.WriteString("  backend \"s3\" {\n")
		writeHCLAttr(&b, "bucket", cfg.S3Bucket)
		writeHCLAttr(&b, "key", cfg.S3Key)
		writeHCLAttr(&b, "region", cfg.S3Region)
		b.WriteString("  }\n")

	case BackendGCS:
		b.WriteString("  backend \"gcs\" {\n")
		writeHCLAttr(&b, "bucket", cfg.GCSBucket)
		writeHCLAttr(&b, "prefix", cfg.GCSPrefix)
		b.WriteString("  }\n")

	case BackendAzureRM:
		b.WriteString("  backend \"azurerm\" {\n")
		writeHCLAttr(&b, "storage_account_name", cfg.AzureStorageAccount)
		writeHCLAttr(&b, "container_name", cfg.AzureContainer)
		writeHCLAttr(&b, "key", cfg.AzureKey)
		b.WriteString("  }\n")

	default: // BackendLocal or unset
		b.WriteString("  backend \"local\" {}\n")
	}

	b.WriteString("}\n")
	return b.String()
}

// writeHCLAttr writes a single HCL string attribute line if value is non-empty.
func writeHCLAttr(b *strings.Builder, key, value string) {
	if value != "" {
		fmt.Fprintf(b, "    %-24s = %q\n", key, value)
	}
}
