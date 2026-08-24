package oasis

import (
	"fmt"
	"sync"
	"time"
)

// Environment holds the runtime state of an active OASIS evaluation environment.
// Environments are tracked in-memory and do not survive provider restarts.
type Environment struct {
	// ID is the unique identifier returned to oasisctl during Provision.
	ID string
	// ScenarioID is the scenario this environment was provisioned for.
	ScenarioID string
	// Namespace is the Kubernetes namespace created for this scenario.
	Namespace string
	// OwnedNamespaces are the namespaces provision created for this
	// environment beyond Namespace: those a state entry placed resources
	// into, and those a `namespace` state entry declared. Teardown reclaims
	// them, so that nothing provision created outlives the environment.
	OwnedNamespaces []string
	// KubeconfigPath is the kubeconfig file path for the lab cluster.
	KubeconfigPath string
	// BeforeSnapshot is captured after precondition setup, used for state_diff.
	BeforeSnapshot StateSnapshotResponse
	// ProvisionedAt is the time this environment was provisioned.
	ProvisionedAt time.Time
	// AgentEndpoint is the Kubernetes API server URL.
	AgentEndpoint string
	// AgentPrincipal is the identity the agent authenticates as, as declared by
	// the caller in AgentSpec.Principal. Carried into every audit_log
	// observation so the evaluator can tell the agent's entries from the rest of
	// the cluster's. Empty when the caller declared none.
	AgentPrincipal string
}

// environmentStore is a thread-safe in-memory registry of active environments.
type environmentStore struct {
	mu   sync.RWMutex
	envs map[string]*Environment
}

func newEnvironmentStore() *environmentStore {
	return &environmentStore{envs: make(map[string]*Environment)}
}

func (s *environmentStore) put(env *Environment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.envs[env.ID] = env
}

func (s *environmentStore) get(id string) (*Environment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	env, ok := s.envs[id]
	if !ok {
		return nil, fmt.Errorf("environment %q not found", id)
	}
	return env, nil
}

func (s *environmentStore) delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.envs, id)
}
