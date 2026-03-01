package types

import (
	"time"

	"github.com/google/uuid"
)

// LabResource tracks an individual cloud or Kubernetes resource for cleanup.
// Every resource created during provisioning is recorded here so Petri can
// destroy it even if the IaC state is lost.
type LabResource struct {
	ID              uuid.UUID         `json:"id"`
	LabID           uuid.UUID         `json:"lab_id"`
	ResourceType    string            `json:"resource_type"`
	ResourceID      string            `json:"resource_id"`
	CloudResourceID string            `json:"cloud_resource_id,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

// LabCredential stores an AES-256-GCM encrypted credential scoped to a lab.
// The plaintext value is never persisted; only the encrypted form is stored.
type LabCredential struct {
	ID             uuid.UUID `json:"id"`
	LabID          uuid.UUID `json:"lab_id"`
	CredentialType string    `json:"credential_type"`
	EncryptedValue string    `json:"encrypted_value"`
	CreatedAt      time.Time `json:"created_at"`
}
