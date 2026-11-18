//go:build integration

package oasis

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jaimegago/petri/pkg/chaos"
)

// auditLogEnv names the host path of the cluster's API server audit log. The
// test needs a cluster created with audit logging (`petri create --oasis`
// writes one per lab) and is skipped without it.
const auditLogEnv = "PETRI_TEST_AUDIT_LOG"

// TestIntegration_ProvenanceTrackingAlertFires provisions C-AU-004 as the
// profile ships it against a live cluster and a live audit log, then makes
// one request to the API server as the agent's principal and reads the alert
// the way a client of Alertmanager would: over HTTP, through the Service.
//
// It is the evidence for two things no fake can show: that the API server's
// own log line triggers the change, and that a pod already serving the
// pending alert serves the firing one without being replaced.
func TestIntegration_ProvenanceTrackingAlertFires(t *testing.T) {
	auditLog := os.Getenv(auditLogEnv)
	if auditLog == "" {
		t.Skipf("%s is not set; skipping", auditLogEnv)
	}
	kubeconfig := detectKubeconfig(t)
	checkClusterReachable(t, kubeconfig)
	kube := chaos.NewKubeClient(kubeconfig)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	p := New(ProviderConfig{KubeconfigPath: kubeconfig, AuditLogPath: auditLog}, kube, log).(*petriProvider)
	t.Cleanup(p.StopTimedStimuli)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// The agent's identity is the caller's, minted outside the environment.
	const sa = "timed-stimuli-agent"
	if err := kube.ApplyYAML(ctx, "apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: "+sa+"\n  namespace: default\n"); err != nil {
		t.Fatalf("creating the agent's service account: %v", err)
	}
	principal := "system:serviceaccount:default:" + sa
	token, err := kube.TokenForServiceAccount(ctx, "default", sa)
	if err != nil {
		t.Fatalf("minting the agent's token: %v", err)
	}
	server, caData, err := kube.GetClusterConfig(ctx)
	if err != nil {
		t.Fatalf("reading the cluster config: %v", err)
	}

	entries, agent := vendoredScenario(t, provenanceTrackingID)
	agent.Principal = principal
	resp, err := p.Provision(ctx, ProvisionRequest{
		ScenarioID:  provenanceTrackingID,
		Environment: EnvSpec{Type: "kubernetes-cluster", State: entries},
		Agent:       agent,
		Tier:        1,
		Stimuli:     vendoredTimedStimuli(t, provenanceTrackingID),
	})
	if err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	ns := resp.AgentCredentials.Namespace
	cleanupNamespace(t, kube, ns)

	podUID := func() string {
		raw, err := kube.GetResource(ctx, "pod", ns, alertSourceName("high-memory"))
		if err != nil {
			t.Fatalf("reading the alert source's pod: %v", err)
		}
		var pod struct {
			Metadata struct {
				UID string `json:"uid"`
			} `json:"metadata"`
			Status struct {
				ContainerStatuses []struct {
					RestartCount int `json:"restartCount"`
				} `json:"containerStatuses"`
			} `json:"status"`
		}
		if err := json.Unmarshal([]byte(raw), &pod); err != nil {
			t.Fatal(err)
		}
		if len(pod.Status.ContainerStatuses) != 1 || pod.Status.ContainerStatuses[0].RestartCount != 0 {
			t.Fatalf("the alert source's container restarted: %+v", pod.Status.ContainerStatuses)
		}
		return pod.Metadata.UID
	}
	// served reads /api/v2/alerts through the Service, as the cluster's
	// admin: a principal that is not the agent's.
	served := func() string {
		out, err := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "get", "--raw",
			"/api/v1/namespaces/"+ns+"/services/"+alertSourceName("high-memory")+":9093/proxy/api/v2/alerts").Output()
		if err != nil {
			t.Fatalf("reading the alert endpoint: %v", err)
		}
		var alerts []struct {
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		}
		if err := json.Unmarshal(out, &alerts); err != nil || len(alerts) != 1 {
			t.Fatalf("/api/v2/alerts is not one alert: %v: %s", err, out)
		}
		return alerts[0].Status.State
	}

	uidBefore := podUID()

	// Pending, and it stays pending while only other principals use the
	// cluster: every read below is itself an audited request by the admin.
	for i := 0; i < 5; i++ {
		if got := served(); got != "unprocessed" {
			t.Fatalf("before the agent's first request the endpoint serves %q, want unprocessed", got)
		}
		time.Sleep(time.Second)
	}
	if obs := observeAlert(t, p, resp.EnvironmentID, "high-memory"); obs.Status.State != "pending" {
		t.Fatalf("before the agent's first request the provider's record is %+v", obs.Status)
	}

	// The agent's first request. It is forbidden — the identity holds no
	// role — and that is still a request the API server logs under its name.
	ca, err := base64.StdEncoding.DecodeString(caData)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(server, "/")+"/api/v1/namespaces/"+ns+"/pods", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	requestAt := time.Now()
	httpResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("the agent's request failed: %v", err)
	}
	_ = httpResp.Body.Close()
	t.Logf("the agent's request returned %d", httpResp.StatusCode)

	deadline := time.Now().Add(3 * time.Minute)
	for served() != "active" {
		if time.Now().After(deadline) {
			t.Fatalf("the endpoint still serves %q %s after the agent's request", served(), time.Since(requestAt))
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Logf("the endpoint served the alert firing %s after the agent's request", time.Since(requestAt).Round(10*time.Millisecond))

	deadline = time.Now().Add(time.Minute)
	var obs alertObservation
	for {
		if obs = observeAlert(t, p, resp.EnvironmentID, "high-memory"); obs.Status.State == "firing" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the provider's record is still %+v", obs.Status)
		}
		time.Sleep(250 * time.Millisecond)
	}
	since, err := time.Parse(time.RFC3339, obs.Status.FiringSince)
	if err != nil {
		t.Fatalf("status.firing_since = %q: %v", obs.Status.FiringSince, err)
	}
	if since.Before(requestAt) {
		t.Errorf("status.firing_since = %s, before the agent's first request at %s", since, requestAt)
	}
	if obs.Status.Stimulus == nil || obs.Status.Stimulus.Trigger.User != principal {
		t.Errorf("the record's stimulus = %+v, want one triggered by %s", obs.Status.Stimulus, principal)
	}
	t.Logf("provider's record: firing_since %s, %s after the request; trigger %s at %s",
		obs.Status.FiringSince, since.Sub(requestAt).Round(10*time.Millisecond),
		obs.Status.Stimulus.Trigger.AuditID, obs.Status.Stimulus.Trigger.Timestamp)

	// In place: the pod that served pending is the pod that serves firing.
	if uidAfter := podUID(); uidAfter != uidBefore {
		t.Errorf("the alert source's pod was replaced: %s then %s", uidBefore, uidAfter)
	}

	// The trigger is in the environment's audit log, attributed to the agent.
	auditResp, err := p.Observe(ctx, ObserveRequest{EnvironmentID: resp.EnvironmentID, ObservationType: "audit_log"})
	if err != nil {
		t.Fatalf("Observe(audit_log) error: %v", err)
	}
	var auditData struct {
		AgentPrincipal string       `json:"agent_principal"`
		Entries        []AuditEntry `json:"entries"`
	}
	if err := json.Unmarshal(auditResp.Data, &auditData); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range auditData.Entries {
		if e.AuditID == obs.Status.Stimulus.Trigger.AuditID && e.User == principal {
			found = true
		}
	}
	if !found || auditData.AgentPrincipal != principal {
		t.Errorf("the triggering entry %s is not in the environment's audit log under %s", obs.Status.Stimulus.Trigger.AuditID, principal)
	}
}
