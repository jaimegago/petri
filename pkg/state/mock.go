package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/types"
)

// MockManager is a thread-safe in-memory Manager implementation for use in tests.
// It stores all data in maps and can be pre-loaded with fixtures or inspected
// after the fact to verify interactions.
type MockManager struct {
	mu          sync.RWMutex
	labs        map[uuid.UUID]*types.Lab
	resources   map[uuid.UUID]*types.LabResource // keyed by resource ID
	credentials map[string]*types.LabCredential  // keyed by "labID/credType"

	// CloseErr is returned by Close if set.
	CloseErr error
}

// NewMockManager returns an initialised MockManager ready for use in tests.
func NewMockManager() *MockManager {
	return &MockManager{
		labs:        make(map[uuid.UUID]*types.Lab),
		resources:   make(map[uuid.UUID]*types.LabResource),
		credentials: make(map[string]*types.LabCredential),
	}
}

func (m *MockManager) CreateLab(_ context.Context, lab *types.Lab) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.labs {
		if l.Name == lab.Name {
			return errors.New("lab name already exists")
		}
	}
	copy := *lab
	m.labs[lab.ID] = &copy
	return nil
}

func (m *MockManager) GetLab(_ context.Context, id uuid.UUID) (*types.Lab, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	lab, ok := m.labs[id]
	if !ok {
		return nil, fmt.Errorf("lab %s not found: %w", id, sql.ErrNoRows)
	}
	copy := *lab
	return &copy, nil
}

func (m *MockManager) GetLabByName(_ context.Context, name string) (*types.Lab, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, lab := range m.labs {
		if lab.Name == name {
			copy := *lab
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("lab %q not found: %w", name, sql.ErrNoRows)
}

func (m *MockManager) UpdateLab(_ context.Context, lab *types.Lab) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.labs[lab.ID]; !ok {
		return fmt.Errorf("lab %s not found for update", lab.ID)
	}
	copy := *lab
	m.labs[lab.ID] = &copy
	return nil
}

func (m *MockManager) DeleteLab(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.labs, id)
	return nil
}

func (m *MockManager) ListLabs(_ context.Context, filter ListFilter) ([]*types.Lab, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*types.Lab
	for _, lab := range m.labs {
		if filter.Company != "" && lab.Company != filter.Company {
			continue
		}
		if filter.Level > 0 && lab.Level != filter.Level {
			continue
		}
		if filter.Status != "" && lab.Status != filter.Status {
			continue
		}
		if !filter.IncludeExpired && time.Now().After(lab.ExpiresAt) {
			continue
		}
		copy := *lab
		result = append(result, &copy)
	}
	return result, nil
}

func (m *MockManager) FindExpiredLabs(_ context.Context, gracePeriod time.Duration) ([]*types.Lab, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cutoff := time.Now().Add(-gracePeriod)
	var result []*types.Lab
	for _, lab := range m.labs {
		if lab.ExpiresAt.Before(cutoff) &&
			lab.Status != types.LabStatusDestroyed &&
			lab.Status != types.LabStatusDestroying {
			copy := *lab
			result = append(result, &copy)
		}
	}
	return result, nil
}

func (m *MockManager) CreateResource(_ context.Context, r *types.LabResource) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := *r
	m.resources[r.ID] = &copy
	return nil
}

func (m *MockManager) ListResources(_ context.Context, labID uuid.UUID) ([]*types.LabResource, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*types.LabResource
	for _, r := range m.resources {
		if r.LabID == labID {
			copy := *r
			result = append(result, &copy)
		}
	}
	return result, nil
}

func (m *MockManager) DeleteResources(_ context.Context, labID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, r := range m.resources {
		if r.LabID == labID {
			delete(m.resources, id)
		}
	}
	return nil
}

func (m *MockManager) StoreCredential(_ context.Context, cred *types.LabCredential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := cred.LabID.String() + "/" + cred.CredentialType
	copy := *cred
	m.credentials[key] = &copy
	return nil
}

func (m *MockManager) GetCredential(_ context.Context, labID uuid.UUID, credType string) (*types.LabCredential, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := labID.String() + "/" + credType
	cred, ok := m.credentials[key]
	if !ok {
		return nil, fmt.Errorf("credential %q not found for lab %s: %w", credType, labID, sql.ErrNoRows)
	}
	copy := *cred
	return &copy, nil
}

func (m *MockManager) DeleteCredentials(_ context.Context, labID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := labID.String() + "/"
	for key := range m.credentials {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			delete(m.credentials, key)
		}
	}
	return nil
}

func (m *MockManager) Close() error {
	return m.CloseErr
}
