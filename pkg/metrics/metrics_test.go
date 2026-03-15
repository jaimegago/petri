package metrics

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/jaimegago/petri/pkg/logger"
)

func newTestRecorder(t *testing.T) (*Recorder, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	rec := New(reg)
	return rec, reg
}

func TestNew_RegistersMetrics(t *testing.T) {
	rec, reg := newTestRecorder(t)

	// Trigger observations so label vecs materialise in the registry.
	rec.LabCreated("acme", 1, "local")
	rec.LabDestroyed("acme", 1, "local", "manual")
	rec.ObserveCreate("acme", 1, "local", time.Second)
	rec.ObserveDestroy("acme", 1, "local", time.Second)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	names := make(map[string]bool)
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}

	want := []string{
		"petri_labs_created_total",
		"petri_labs_destroyed_total",
		"petri_labs_active",
		"petri_lab_create_duration_seconds",
		"petri_lab_destroy_duration_seconds",
	}
	for _, n := range want {
		if !names[n] {
			t.Errorf("metric %q not registered", n)
		}
	}
}

func TestLabCreated_IncrementsCounter(t *testing.T) {
	rec, reg := newTestRecorder(t)

	rec.LabCreated("acme", 1, "local")
	rec.LabCreated("acme", 1, "local")
	rec.LabCreated("techflow", 2, "aws")

	mfs, _ := reg.Gather()
	for _, mf := range mfs {
		if mf.GetName() != "petri_labs_created_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			var company, level, provider string
			for _, lp := range m.GetLabel() {
				switch lp.GetName() {
				case "company":
					company = lp.GetValue()
				case "level":
					level = lp.GetValue()
				case "provider":
					provider = lp.GetValue()
				}
			}
			val := m.GetCounter().GetValue()
			if company == "acme" && level == "1" && provider == "local" && val != 2 {
				t.Errorf("acme/1/local: got %v, want 2", val)
			}
			if company == "techflow" && level == "2" && provider == "aws" && val != 1 {
				t.Errorf("techflow/2/aws: got %v, want 1", val)
			}
		}
	}
}

func TestLabDestroyed_IncrementsCounter(t *testing.T) {
	rec, reg := newTestRecorder(t)
	rec.LabCreated("acme", 1, "local")

	rec.LabDestroyed("acme", 1, "local", "manual")

	mfs, _ := reg.Gather()
	for _, mf := range mfs {
		if mf.GetName() != "petri_labs_destroyed_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			val := m.GetCounter().GetValue()
			if val != 1 {
				t.Errorf("labs_destroyed_total: got %v, want 1", val)
			}
		}
	}
}

func TestLabsActiveGauge(t *testing.T) {
	rec, reg := newTestRecorder(t)

	rec.LabCreated("acme", 1, "local")
	rec.LabCreated("acme", 2, "local")
	rec.LabDestroyed("acme", 1, "local", "ttl_expired")

	mfs, _ := reg.Gather()
	for _, mf := range mfs {
		if mf.GetName() != "petri_labs_active" {
			continue
		}
		val := mf.GetMetric()[0].GetGauge().GetValue()
		if val != 1 {
			t.Errorf("labs_active gauge: got %v, want 1", val)
		}
	}
}

func TestObserveCreate_RecordsDuration(t *testing.T) {
	rec, reg := newTestRecorder(t)

	rec.ObserveCreate("acme", 1, "local", 45*time.Second)
	rec.ObserveCreate("acme", 1, "local", 90*time.Second)

	mfs, _ := reg.Gather()
	for _, mf := range mfs {
		if mf.GetName() != "petri_lab_create_duration_seconds" {
			continue
		}
		for _, m := range mf.GetMetric() {
			h := m.GetHistogram()
			if h.GetSampleCount() != 2 {
				t.Errorf("sample count: got %d, want 2", h.GetSampleCount())
			}
		}
	}
}

func TestObserveDestroy_RecordsDuration(t *testing.T) {
	rec, reg := newTestRecorder(t)

	rec.ObserveDestroy("acme", 1, "local", 20*time.Second)

	mfs, _ := reg.Gather()
	for _, mf := range mfs {
		if mf.GetName() != "petri_lab_destroy_duration_seconds" {
			continue
		}
		for _, m := range mf.GetMetric() {
			h := m.GetHistogram()
			if h.GetSampleCount() != 1 {
				t.Errorf("sample count: got %d, want 1", h.GetSampleCount())
			}
		}
	}
}

func TestStartServer_HealthzAndMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	New(reg) // register metrics

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := "127.0.0.1:19091"
	errCh := make(chan error, 1)
	go func() {
		errCh <- StartServer(ctx, addr, reg, logger.Nop())
	}()

	// Wait for server to start.
	var resp *http.Response
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get("http://" + addr + "/healthz") //nolint:noctx
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("healthz not reachable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status: got %d, want 200", resp.StatusCode)
	}

	// Check /metrics endpoint.
	metricsResp, err := http.Get("http://" + addr + "/metrics") //nolint:noctx
	if err != nil {
		t.Fatalf("metrics not reachable: %v", err)
	}
	defer func() { _ = metricsResp.Body.Close() }()
	body, _ := io.ReadAll(metricsResp.Body)
	if !strings.Contains(string(body), "petri_labs_active") {
		t.Error("metrics response missing petri_labs_active")
	}

	// Graceful shutdown.
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("StartServer returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("server did not shut down in time")
	}
}
