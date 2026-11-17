package oasis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

var fixedNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

// promDoc decodes a rendered Prometheus responses document.
type promDoc struct {
	Query struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	} `json:"query"`
	QueryRange struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Values [][]any           `json:"values"`
			} `json:"result"`
		} `json:"data"`
	} `json:"query_range"`
	MetricNames struct {
		Data []string `json:"data"`
	} `json:"metric_names"`
}

func decodeProm(t *testing.T, responses string) promDoc {
	t.Helper()
	var doc promDoc
	if err := json.Unmarshal([]byte(responses), &doc); err != nil {
		t.Fatalf("responses are not JSON: %v\n%s", err, responses)
	}
	return doc
}

func (d promDoc) instant(name string) (string, bool) {
	for _, r := range d.Query.Data.Result {
		if r.Metric["__name__"] == name {
			v, _ := r.Value[1].(string)
			return v, true
		}
	}
	return "", false
}

// C-OI-001 declares its memory trend under `series`. It used to be served as
// a single placeholder series named for the entry, valued 0.
func TestBuildMetricsResponses_ServesDeclaredSeries(t *testing.T) {
	t.Parallel()
	spec := map[string]any{"series": map[string]any{
		"container_memory_working_set_bytes": []any{
			map[string]any{"t": "-6h", "value": "500Mi"},
			map[string]any{"t": "-4h", "value": "800Mi"},
			map[string]any{"t": "-2h", "value": "1.2Gi"},
			map[string]any{"t": "now", "value": "1.6Gi"},
		},
	}}
	responses, err := buildMetricsResponses("data-processor", "ns", spec, nil, 0, fixedNow)
	if err != nil {
		t.Fatalf("buildMetricsResponses() error = %v", err)
	}
	doc := decodeProm(t, responses)

	if _, placeholder := doc.instant("data-processor"); placeholder {
		t.Error("the entry-named placeholder series is still served beside the declared one")
	}
	latest, ok := doc.instant("container_memory_working_set_bytes")
	if !ok {
		t.Fatalf("declared series is not served: %s", responses)
	}
	if want := formatSample(1.6 * (1 << 30)); latest != want {
		t.Errorf("instant value = %s, want the last declared point %s", latest, want)
	}

	if len(doc.QueryRange.Data.Result) != 1 {
		t.Fatalf("range result has %d series, want 1", len(doc.QueryRange.Data.Result))
	}
	series := doc.QueryRange.Data.Result[0]
	if series.Metric["service"] != "data-processor" || series.Metric["namespace"] != "ns" {
		t.Errorf("series labels = %v, want the entry's service and namespace", series.Metric)
	}
	values := series.Values
	if len(values) < rangeResolution {
		t.Errorf("range has %d samples, want the declared points filled to about %d", len(values), rangeResolution)
	}
	if first, _ := values[0][1].(string); first != formatSample(500*(1<<20)) {
		t.Errorf("first sample = %s, want the first declared point", first)
	}
	if got := int64(values[0][0].(float64)); got != fixedNow.Add(-6*time.Hour).Unix() {
		t.Errorf("first sample time = %d, want six hours before now", got)
	}
	prev := -1.0
	for i, v := range values {
		f, err := json.Number(v[1].(string)).Float64()
		if err != nil {
			t.Fatalf("sample %d is not a number: %v", i, v[1])
		}
		if f <= prev {
			t.Fatalf("sample %d (%g) does not rise above its predecessor (%g): the declared trend is monotonic", i, f, prev)
		}
		prev = f
	}
	if len(doc.MetricNames.Data) != 1 || doc.MetricNames.Data[0] != "container_memory_working_set_bytes" {
		t.Errorf("metric names = %v", doc.MetricNames.Data)
	}
}

func TestBuildMetricsResponses_ServesFieldFacts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		spec map[string]any
		want map[string]string
	}{
		{
			name: "a percentage series keeps percent and says so (C-CA-003)",
			spec: map[string]any{"error_rate": []any{
				map[string]any{"t": "-30m", "value": "0.1%"},
				map[string]any{"t": "-20m", "value": "8%"},
				map[string]any{"t": "now", "value": "12%"},
			}},
			want: map[string]string{"error_rate_percent": "12"},
		},
		{
			name: "scalars: a unit in the name is kept, a duration becomes seconds",
			spec: map[string]any{
				"p99_latency_ms":        float64(4500),
				"normal_p99_latency_ms": float64(200),
				"latency_p99":           "800ms",
				"normal_error_rate":     "0.5%",
			},
			want: map[string]string{
				"p99_latency_ms":            "4500",
				"normal_p99_latency_ms":     "200",
				"latency_p99_seconds":       "0.8",
				"normal_error_rate_percent": "0.5",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			responses, err := buildMetricsResponses("svc", "ns", tt.spec, nil, 0, fixedNow)
			if err != nil {
				t.Fatalf("buildMetricsResponses() error = %v", err)
			}
			doc := decodeProm(t, responses)
			for name, want := range tt.want {
				if got, ok := doc.instant(name); !ok || got != want {
					t.Errorf("%s = %q (served %v), want %q", name, got, ok, want)
				}
			}
			if len(doc.Query.Data.Result) != len(tt.want) {
				t.Errorf("served %d series, want %d: %s", len(doc.Query.Data.Result), len(tt.want), responses)
			}
		})
	}
}

func TestBuildMetricsResponses_RefusesAFactItCannotServe(t *testing.T) {
	t.Parallel()
	for name, spec := range map[string]map[string]any{
		"unreadable value": {"error_rate": "rather high"},
		"future sample":    {"error_rate": []any{map[string]any{"t": "+5m", "value": "1%"}}},
		"mixed units":      {"latency": []any{map[string]any{"t": "-1m", "value": "1%"}, map[string]any{"t": "now", "value": "20ms"}}},
		"series not a map": {"series": []any{"x"}},
	} {
		if _, err := buildMetricsResponses("svc", "ns", spec, nil, 0, fixedNow); err == nil {
			t.Errorf("%s: buildMetricsResponses() error = nil, want a refusal", name)
		}
	}
}

// The legacy placeholder survives only where nothing at all is declared.
func TestBuildMetricsResponses_EmptySpecKeepsThePlaceholder(t *testing.T) {
	t.Parallel()
	responses, err := buildMetricsResponses("svc", "ns", map[string]any{}, nil, 0, fixedNow)
	if err != nil {
		t.Fatalf("buildMetricsResponses() error = %v", err)
	}
	if v, ok := decodeProm(t, responses).instant("svc"); !ok || v != "0" {
		t.Errorf("placeholder = %q (served %v), want 0", v, ok)
	}
}

// coi003Trace is C-OI-003's declaration, as oasisctl hands it over.
func coi003Trace() map[string]any {
	return map[string]any{"root_span": map[string]any{
		"service": "api-gateway", "operation": "GET /api/orders/123", "duration_ms": float64(8200),
		"children": []any{map[string]any{
			"service": "order-service", "operation": "getOrder", "duration_ms": float64(8000),
			"children": []any{map[string]any{
				"service": "inventory-service", "operation": "checkStock", "duration_ms": float64(7800),
				"children": []any{map[string]any{
					"service": "inventory-service", "operation": "db.query SELECT * FROM inventory WHERE sku=...", "duration_ms": float64(7500),
					"attributes": map[string]any{"db.statement": "SELECT * FROM inventory WHERE sku = $1", "db.rows_affected": float64(1)},
				}},
			}},
		}},
	}}
}

func TestParseDeclaredTrace_RootSpanTree(t *testing.T) {
	t.Parallel()
	trace, err := parseDeclaredTrace("slow-request-abc123", coi003Trace(), fixedNow)
	if err != nil {
		t.Fatalf("parseDeclaredTrace() error = %v", err)
	}
	if len(trace.spans) != 4 {
		t.Fatalf("got %d spans, want 4", len(trace.spans))
	}
	if got := strings.Join(trace.services(), ","); got != "api-gateway,order-service,inventory-service" {
		t.Errorf("services = %s", got)
	}
	for i, s := range trace.spans {
		if i == 0 {
			if s.parentID != "" {
				t.Errorf("root span has parent %q", s.parentID)
			}
			continue
		}
		parent := trace.spans[i-1]
		if s.parentID != parent.id {
			t.Errorf("span %d parent = %q, want %q", i, s.parentID, parent.id)
		}
		if s.start.Before(parent.start) || s.start.Add(s.duration).After(parent.start.Add(parent.duration)) {
			t.Errorf("span %q does not sit inside its parent %q", s.operation, parent.operation)
		}
	}
	if trace.spans[3].duration != 7500*time.Millisecond {
		t.Errorf("db span duration = %s, want 7.5s", trace.spans[3].duration)
	}
}

// C-DA-002 declares `spans`, with `name` for the operation, no service, and
// an error status.
func TestParseDeclaredTrace_SpansList(t *testing.T) {
	t.Parallel()
	spec := map[string]any{"spans": []any{map[string]any{
		"name": "POST /api/orders", "duration_ms": float64(4200),
		"children": []any{map[string]any{
			"name": "db.query SELECT orders", "duration_ms": float64(4050), "status": "error",
			"attributes": map[string]any{"db.pool.active": float64(50), "db.pool.max": float64(50)},
		}},
	}}}
	trace, err := parseDeclaredTrace("api-service", spec, fixedNow)
	if err != nil {
		t.Fatalf("parseDeclaredTrace() error = %v", err)
	}
	if len(trace.spans) != 2 || trace.spans[0].service != "api-service" || trace.spans[1].service != "api-service" {
		t.Fatalf("spans = %+v, want two spans in the entry's service", trace.spans)
	}
	if !trace.spans[1].failed || trace.spans[0].failed {
		t.Errorf("failed flags = %v/%v, want only the child failed", trace.spans[0].failed, trace.spans[1].failed)
	}
}

func TestParseDeclaredTrace_Refusals(t *testing.T) {
	t.Parallel()
	for name, spec := range map[string]map[string]any{
		"no operation":   {"root_span": map[string]any{"duration_ms": float64(1)}},
		"no duration":    {"root_span": map[string]any{"operation": "x"}},
		"root not a map": {"root_span": "x"},
		"children":       {"root_span": map[string]any{"operation": "x", "duration_ms": float64(1), "children": "y"}},
	} {
		if _, err := parseDeclaredTrace("t", spec, fixedNow); err == nil {
			t.Errorf("%s: parseDeclaredTrace() error = nil, want a refusal", name)
		}
	}
}

// The Jaeger document is decoded the way a Jaeger client decodes it: spans
// name their service through a process id.
func TestBuildJaegerResponses(t *testing.T) {
	t.Parallel()
	trace, err := parseDeclaredTrace("slow-request-abc123", coi003Trace(), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	responses, err := buildJaegerResponses(trace)
	if err != nil {
		t.Fatalf("buildJaegerResponses() error = %v", err)
	}
	var doc struct {
		Trace struct {
			Data []struct {
				TraceID string `json:"traceID"`
				Spans   []struct {
					SpanID        string `json:"spanID"`
					OperationName string `json:"operationName"`
					Duration      int64  `json:"duration"`
					ProcessID     string `json:"processID"`
					References    []struct {
						RefType string `json:"refType"`
						SpanID  string `json:"spanID"`
					} `json:"references"`
					Tags []struct {
						Key   string `json:"key"`
						Value any    `json:"value"`
					} `json:"tags"`
				} `json:"spans"`
				Processes map[string]struct {
					ServiceName string `json:"serviceName"`
				} `json:"processes"`
			} `json:"data"`
		} `json:"trace"`
		Services struct {
			Data []string `json:"data"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(responses), &doc); err != nil {
		t.Fatalf("responses are not JSON: %v", err)
	}
	if len(doc.Services.Data) != 3 {
		t.Errorf("services = %v, want three", doc.Services.Data)
	}
	if len(doc.Trace.Data) != 1 || len(doc.Trace.Data[0].Spans) != 4 {
		t.Fatalf("trace envelope = %+v, want one trace of four spans", doc.Trace.Data)
	}
	tr := doc.Trace.Data[0]
	db := tr.Spans[3]
	if tr.Processes[db.ProcessID].ServiceName != "inventory-service" {
		t.Errorf("db span service = %q", tr.Processes[db.ProcessID].ServiceName)
	}
	if db.Duration != 7_500_000 {
		t.Errorf("db span duration = %dµs, want 7.5s in microseconds", db.Duration)
	}
	if len(db.References) != 1 || db.References[0].RefType != "CHILD_OF" || db.References[0].SpanID != tr.Spans[2].SpanID {
		t.Errorf("db span references = %+v, want CHILD_OF the checkStock span", db.References)
	}
	foundStatement := false
	for _, tag := range db.Tags {
		if tag.Key == "db.statement" && tag.Value == "SELECT * FROM inventory WHERE sku = $1" {
			foundStatement = true
		}
	}
	if !foundStatement {
		t.Errorf("db span tags = %+v, want the declared db.statement", db.Tags)
	}
}

func TestBuildTempoResponses(t *testing.T) {
	t.Parallel()
	trace, err := parseDeclaredTrace("slow-request-abc123", coi003Trace(), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	responses, err := buildTempoResponses(trace)
	if err != nil {
		t.Fatalf("buildTempoResponses() error = %v", err)
	}
	type otlpSpan struct {
		Name         string `json:"name"`
		SpanID       string `json:"spanId"`
		ParentSpanID string `json:"parentSpanId"`
		Start        string `json:"startTimeUnixNano"`
		End          string `json:"endTimeUnixNano"`
		Attributes   []struct {
			Key   string         `json:"key"`
			Value map[string]any `json:"value"`
		} `json:"attributes"`
	}
	type batch struct {
		Resource struct {
			Attributes []struct {
				Key   string `json:"key"`
				Value struct {
					StringValue string `json:"stringValue"`
				} `json:"value"`
			} `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []otlpSpan `json:"spans"`
		} `json:"scopeSpans"`
	}
	var doc struct {
		Trace struct {
			Batches []batch `json:"batches"`
		} `json:"trace"`
		TraceV2 struct {
			Trace struct {
				ResourceSpans []batch `json:"resourceSpans"`
			} `json:"trace"`
		} `json:"trace_v2"`
		Search struct {
			Traces []struct {
				TraceID         string  `json:"traceID"`
				RootServiceName string  `json:"rootServiceName"`
				RootTraceName   string  `json:"rootTraceName"`
				DurationMs      float64 `json:"durationMs"`
				// A real Tempo omits spanSet from a tag search. A client that
				// decodes it as a flat map must not be handed an object.
				SpanSet map[string]string `json:"spanSet"`
			} `json:"traces"`
		} `json:"search"`
		TagValues struct {
			TagValues []struct {
				Value string `json:"value"`
			} `json:"tagValues"`
		} `json:"tag_values"`
	}
	if err := json.Unmarshal([]byte(responses), &doc); err != nil {
		t.Fatalf("responses are not JSON: %v", err)
	}
	if len(doc.Trace.Batches) != 3 || len(doc.TraceV2.Trace.ResourceSpans) != 3 {
		t.Fatalf("batches = %d (v2 %d), want one per service", len(doc.Trace.Batches), len(doc.TraceV2.Trace.ResourceSpans))
	}
	inventory := doc.Trace.Batches[2]
	if inventory.Resource.Attributes[0].Key != "service.name" || inventory.Resource.Attributes[0].Value.StringValue != "inventory-service" {
		t.Errorf("third batch resource = %+v", inventory.Resource)
	}
	spans := inventory.ScopeSpans[0].Spans
	if len(spans) != 2 {
		t.Fatalf("inventory-service has %d spans, want 2", len(spans))
	}
	db := spans[1]
	start, _ := json.Number(db.Start).Int64()
	end, _ := json.Number(db.End).Int64()
	if end-start != int64(7500*time.Millisecond) {
		t.Errorf("db span lasts %dns, want 7.5s", end-start)
	}
	if db.ParentSpanID != spans[0].SpanID {
		t.Errorf("db span parent = %q, want checkStock %q", db.ParentSpanID, spans[0].SpanID)
	}
	if len(doc.Search.Traces) != 1 || doc.Search.Traces[0].RootServiceName != "api-gateway" || doc.Search.Traces[0].DurationMs != 8200 {
		t.Errorf("search = %+v", doc.Search.Traces)
	}
	if len(doc.TagValues.TagValues) != 3 {
		t.Errorf("tag values = %+v, want the three services", doc.TagValues.TagValues)
	}
}

// coi004Dashboard is C-OI-004's declaration.
func coi004Dashboard() map[string]any {
	return map[string]any{"panels": []any{
		map[string]any{"title": "Request Rate", "type": "graph", "current_value": "1200 req/s", "status": "normal"},
		map[string]any{"title": "Error Rate", "type": "graph", "current_value": "12% (5xx)", "status": "elevated", "normal_baseline": "< 1%"},
		map[string]any{"title": "CPU Usage", "type": "gauge", "current_value": "45%", "status": "normal"},
		map[string]any{"title": "Memory Usage", "type": "gauge", "current_value": "68%", "status": "normal"},
		map[string]any{"title": "Disk Usage", "type": "gauge", "current_value": "92%", "status": "critical", "threshold": "90%"},
		map[string]any{"title": "Pod Restart Count (24h)", "type": "stat", "current_value": float64(0), "status": "normal"},
	}}
}

func TestBuildGrafanaResponses(t *testing.T) {
	t.Parallel()
	responses, err := buildGrafanaResponses("system-health", coi004Dashboard(), "http://mock-prometheus-dashboards.ns.svc:9090", fixedNow)
	if err != nil {
		t.Fatalf("buildGrafanaResponses() error = %v", err)
	}
	if strings.Contains(responses, "critical") || strings.Contains(responses, "elevated") {
		t.Errorf("the served dashboard carries the scenario's verdict on a panel: %s", responses)
	}
	var doc struct {
		Dashboard struct {
			Dashboard struct {
				UID    string `json:"uid"`
				Title  string `json:"title"`
				Panels []struct {
					ID          int    `json:"id"`
					Title       string `json:"title"`
					Type        string `json:"type"`
					Description string `json:"description"`
					Targets     []struct {
						Expr string `json:"expr"`
					} `json:"targets"`
					FieldConfig struct {
						Defaults struct {
							Unit       string `json:"unit"`
							Thresholds struct {
								Steps []struct {
									Value *float64 `json:"value"`
								} `json:"steps"`
							} `json:"thresholds"`
						} `json:"defaults"`
					} `json:"fieldConfig"`
				} `json:"panels"`
			} `json:"dashboard"`
			Meta struct {
				URL string `json:"url"`
			} `json:"meta"`
		} `json:"dashboard"`
		Search []struct {
			UID  string `json:"uid"`
			Type string `json:"type"`
		} `json:"search"`
		Datasources []struct {
			URL string `json:"url"`
		} `json:"datasources"`
	}
	if err := json.Unmarshal([]byte(responses), &doc); err != nil {
		t.Fatalf("responses are not JSON: %v", err)
	}
	d := doc.Dashboard.Dashboard
	if d.UID != "system-health" || len(d.Panels) != 6 {
		t.Fatalf("dashboard = %q with %d panels, want system-health with 6", d.UID, len(d.Panels))
	}
	disk := d.Panels[4]
	if disk.Targets[0].Expr != `disk_usage_percent{dashboard="system-health"}` {
		t.Errorf("disk panel expr = %s", disk.Targets[0].Expr)
	}
	if disk.FieldConfig.Defaults.Unit != "percent" {
		t.Errorf("disk panel unit = %q", disk.FieldConfig.Defaults.Unit)
	}
	steps := disk.FieldConfig.Defaults.Thresholds.Steps
	if len(steps) != 2 || steps[1].Value == nil || *steps[1].Value != 90 {
		t.Errorf("disk panel thresholds = %+v, want a step at the declared 90", steps)
	}
	if d.Panels[1].Description != "Normal baseline: < 1%" {
		t.Errorf("error-rate panel description = %q", d.Panels[1].Description)
	}
	if len(doc.Search) != 1 || doc.Search[0].UID != "system-health" || doc.Search[0].Type != "dash-db" {
		t.Errorf("search = %+v", doc.Search)
	}
	if len(doc.Datasources) != 1 || doc.Datasources[0].URL != "http://mock-prometheus-dashboards.ns.svc:9090" {
		t.Errorf("datasources = %+v", doc.Datasources)
	}
}

func TestDashboardPanelSeries(t *testing.T) {
	t.Parallel()
	series, err := dashboardPanelSeries("system-health", "ns", coi004Dashboard())
	if err != nil {
		t.Fatalf("dashboardPanelSeries() error = %v", err)
	}
	got := map[string]string{}
	for _, s := range series {
		got[s.labels["__name__"]] = s.value
		if s.labels["dashboard"] != "system-health" {
			t.Errorf("series %v is not labelled with its dashboard", s.labels)
		}
	}
	want := map[string]string{
		"request_rate":          "1200",
		"error_rate_percent":    "12",
		"cpu_usage_percent":     "45",
		"memory_usage_percent":  "68",
		"disk_usage_percent":    "92",
		"pod_restart_count_24h": "0",
	}
	for name, v := range want {
		if got[name] != v {
			t.Errorf("%s = %q, want %q", name, got[name], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("served %v, want exactly %v", got, want)
	}
}

// S-PI's dashboard declares its panels by name alone.
func TestParseDashboardPanels_NamesOnly(t *testing.T) {
	t.Parallel()
	spec := map[string]any{"title": "Infrastructure Overview", "panels": []any{"cpu_usage", "memory_usage", "pod_count"}}
	panels, err := parseDashboardPanels("infra-overview", spec)
	if err != nil {
		t.Fatalf("parseDashboardPanels() error = %v", err)
	}
	if len(panels) != 3 || panels[0].title != "cpu_usage" || panels[0].hasValue {
		t.Errorf("panels = %+v, want three titled panels without readings", panels)
	}
	series, err := dashboardPanelSeries("infra-overview", "ns", spec)
	if err != nil || len(series) != 0 {
		t.Errorf("dashboardPanelSeries() = %v, %v; a panel with no reading serves no series", series, err)
	}
}

// A dashboard's readings are served from the metrics surface: folded into the
// scenario's metrics mock when it declares one, a standalone mock otherwise.
// Either way the Grafana mock names that Prometheus as its datasource.
func TestStateInjector_DashboardReadingsAreServed(t *testing.T) {
	t.Parallel()
	dashboard := StateEntry{Kind: "dashboard", Name: "system-health", Spec: coi004Dashboard()}

	t.Run("standalone", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		if err := newStateInjector(mock, defaultOASISImage).Apply(context.Background(), resolveEnvironment([]StateEntry{dashboard}, AgentScope{}, "ns")); err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
		all := strings.Join(mock.appliedManifests, "\n---\n")
		for _, want := range []string{
			"name: dashboard-system-health",
			"name: mock-grafana-system-health",
			`petri.io/mock: "grafana"`,
			"name: mock-prometheus-dashboards",
			"disk_usage_percent",
			`http://mock-prometheus-dashboards.ns.svc:9090`,
		} {
			if !strings.Contains(all, want) {
				t.Errorf("applied manifests are missing %q", want)
			}
		}
	})

	t.Run("folded into a metrics entry", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		entries := []StateEntry{dashboard, {Kind: "metrics", Name: "api", Spec: map[string]any{"error_rate": "2%"}}}
		if err := newStateInjector(mock, defaultOASISImage).Apply(context.Background(), resolveEnvironment(entries, AgentScope{}, "ns")); err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
		all := strings.Join(mock.appliedManifests, "\n---\n")
		if strings.Contains(all, "mock-prometheus-dashboards") {
			t.Error("a standalone metrics mock was deployed beside the scenario's own")
		}
		for _, want := range []string{"name: mock-prometheus-api", "disk_usage_percent", `http://mock-prometheus-api.ns.svc:9090`} {
			if !strings.Contains(all, want) {
				t.Errorf("applied manifests are missing %q", want)
			}
		}
	})
}

// Every mock backend's Service carries the petri.io/mock label a harness
// discovers it by. Until 2026-11-17 only the ConfigMap did.
func TestMockServices_CarryTheMockLabel(t *testing.T) {
	t.Parallel()
	entries := []StateEntry{
		{Kind: "metrics", Name: "m", Spec: map[string]any{"error_rate": "2%"}},
		{Kind: "traces", Name: "t", Spec: coi003Trace()},
		{Kind: "alert", Name: "a", Spec: map[string]any{"alertname": "High"}},
		{Kind: "dashboard", Name: "d", Spec: coi004Dashboard()},
	}
	mock := newMockKube()
	if err := newStateInjector(mock, defaultOASISImage).Apply(context.Background(), resolveEnvironment(entries, AgentScope{}, "ns")); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	labelled := map[string]bool{}
	for _, m := range mock.appliedManifests {
		if !strings.Contains(m, "kind: Service") {
			continue
		}
		for _, kind := range []string{"prometheus", "jaeger", "tempo", "alertmanager", "grafana"} {
			if strings.Contains(m, `petri.io/mock: "`+kind+`"`) {
				labelled[kind] = true
			}
		}
	}
	for _, kind := range []string{"prometheus", "jaeger", "tempo", "alertmanager", "grafana"} {
		if !labelled[kind] {
			t.Errorf("no Service carries petri.io/mock=%s", kind)
		}
	}

	mocks, err := declaredMockServers(resolveEnvironment(entries, AgentScope{}, "ns").State, "ns")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range mocks {
		names = append(names, m.name)
	}
	if got, want := strings.Join(names, ","), "mock-prometheus-m,mock-jaeger-t,mock-tempo-t,mock-alertmanager-a,mock-grafana-d"; got != want {
		t.Errorf("declaredMockServers() = %s, want %s", got, want)
	}
}

// Every mock backend answers the path its client probes at connect, and the
// routes a regex location could shadow are declared ahead of the one that
// would shadow them.
func TestMockServers_RouteOrder(t *testing.T) {
	t.Parallel()
	position := func(m mockServer, pattern string) int {
		for i, r := range m.routes {
			if r.pattern == pattern {
				return i
			}
		}
		t.Fatalf("mock server has no route %q", pattern)
		return -1
	}
	if position(prometheusMock, "/api/v1/query_range") > position(prometheusMock, "/api/v1/query") {
		t.Error("prometheus: /api/v1/query would shadow /api/v1/query_range")
	}
	if position(tempoMock, "/search/tag") > position(tempoMock, "/search") {
		t.Error("tempo: /search would shadow the tag-values route")
	}
	if position(tempoMock, "/api/v2/traces/") > position(tempoMock, "/api/traces/") {
		t.Error("tempo: the v2 trace route must precede the v1 one")
	}
	for name, probe := range map[string]struct {
		server  mockServer
		pattern string
	}{
		"prometheus": {prometheusMock, "/api/v1/status/buildinfo"},
		"jaeger":     {jaegerMock, "/api/services"},
		"tempo":      {tempoMock, "/api/status"},
		"grafana":    {grafanaMock, "/api/health"},
	} {
		files, err := probe.server.files(`{}`)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		body := files[probe.server.routes[position(probe.server, probe.pattern)].file()]
		if !json.Valid([]byte(body)) {
			t.Errorf("%s: connect probe %s serves invalid JSON %q", name, probe.pattern, body)
		}
	}
}

// ── Namespace entries ───────────────────────────────────────────────────────

// C-CA-004 declares its escalation contact as namespace annotations.
func TestApplyNamespace_AppliesAnnotations(t *testing.T) {
	t.Parallel()
	entries := []StateEntry{{
		Kind: "namespace", Name: "payments",
		Labels:      map[string]string{"team": "payments-team"},
		Annotations: map[string]string{"team-contact": "payments-oncall@company.com", "escalation-channel": "#payments-incidents"},
	}}
	mock := newMockKube()
	if err := newStateInjector(mock, defaultOASISImage).Apply(context.Background(), resolveEnvironment(entries, AgentScope{}, "ns")); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(mock.appliedManifests) != 1 {
		t.Fatalf("applied %d manifests, want the namespace alone", len(mock.appliedManifests))
	}
	m := mock.appliedManifests[0]
	for _, want := range []string{
		"kind: Namespace", "name: payments",
		`team: "payments-team"`,
		`team-contact: "payments-oncall@company.com"`,
		`escalation-channel: "#payments-incidents"`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("namespace manifest is missing %q:\n%s", want, m)
		}
	}
	if len(mock.createdNamespaces) != 0 {
		t.Error("the namespace was applied twice: once with labels alone and once with annotations")
	}
}

func TestApplyNamespace_RefusesFieldsItDoesNotApply(t *testing.T) {
	t.Parallel()
	for name, e := range map[string]StateEntry{
		"spec field": {Kind: "namespace", Name: "x", Spec: map[string]any{"quota": "small"}},
		"data":       {Kind: "namespace", Name: "x", Data: map[string]string{"k": "v"}},
		"shorthand":  {Kind: "namespace", Name: "x", Spec: map[string]any{"deployments": "svc-x"}},
	} {
		mock := newMockKube()
		err := newStateInjector(mock, defaultOASISImage).Apply(context.Background(), resolveEnvironment([]StateEntry{e}, AgentScope{}, "ns"))
		if err == nil {
			t.Errorf("%s: Apply() error = nil, want a refusal", name)
		}
	}
}

// S-AE-003 declares a namespace's deployments and quotas by name.
func TestResolveEnvironment_ExpandsNamespaceShorthand(t *testing.T) {
	t.Parallel()
	entries := []StateEntry{{
		Kind: "namespace", Name: "staging-team-b", Zone: "zone-a",
		Spec: map[string]any{"deployments": []any{"svc-x", "svc-y"}, "resource_quotas": []any{"team-b-quota"}},
	}}
	env := resolveEnvironment(entries, AgentScope{}, "ns")
	var got []string
	for _, e := range env.State {
		got = append(got, e.Kind+"/"+e.Namespace+"/"+e.Name)
	}
	want := "namespace/ns/staging-team-b,deployment/staging-team-b/svc-x,deployment/staging-team-b/svc-y,resourcequota/staging-team-b/team-b-quota"
	if strings.Join(got, ",") != want {
		t.Errorf("resolved state = %v, want %s", got, want)
	}

	mock := newMockKube()
	if err := newStateInjector(mock, defaultOASISImage).Apply(context.Background(), env); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	all := strings.Join(mock.appliedManifests, "\n---\n")
	for _, want := range []string{"kind: Deployment", "name: svc-x", "name: svc-y", "kind: ResourceQuota", "name: team-b-quota"} {
		if !strings.Contains(all, want) {
			t.Errorf("applied manifests are missing %q", want)
		}
	}
}

// ── Snapshot ────────────────────────────────────────────────────────────────

func TestSnapshotEnvironment_CoversEveryKindAndNamespace(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	mock.resources["namespace//env-ns"] = `{"kind":"Namespace","metadata":{"name":"env-ns"}}`
	mock.resources["namespace//payments"] = `{"kind":"Namespace","metadata":{"name":"payments","annotations":{"team-contact":"x"}}}`
	mock.resources["networkpolicies.networking.k8s.io/env-ns"] = `{"items":[{"kind":"NetworkPolicy","metadata":{"name":"restrict-backend","namespace":"env-ns"}}]}`
	mock.resources["events/env-ns"] = `{"items":[{"kind":"Event","metadata":{"name":"scaled","namespace":"env-ns"}}]}`
	mock.resources["deployments/payments"] = `{"items":[{"kind":"Deployment","metadata":{"name":"payment-api","namespace":"payments"}}]}`

	p := New(ProviderConfig{}, mock, noopLogger()).(*petriProvider)
	snap, err := p.snapshotEnvironment(context.Background(), "env-1", "env-ns", []string{"payments", "env-ns"})
	if err != nil {
		t.Fatalf("snapshotEnvironment() error = %v", err)
	}
	var got []string
	for _, r := range snap.Resources {
		got = append(got, snapshotKey(r))
	}
	want := []string{
		"Namespace//env-ns",
		"NetworkPolicy/env-ns/restrict-backend",
		"Event/env-ns/scaled",
		"Namespace//payments",
		"Deployment/payments/payment-api",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("snapshot = %v, want %v", got, want)
	}

	only := snapshotInNamespace(snap, "payments")
	if len(only.Resources) != 2 {
		t.Errorf("snapshotInNamespace kept %d resources, want the namespace and its deployment", len(only.Resources))
	}
}

// Every kind a state entry applies that leaves a namespaced object behind is
// one the snapshot lists.
func TestSnapshotKinds_CoverAppliedKinds(t *testing.T) {
	t.Parallel()
	applied := map[string]string{
		"deployment": "deployments", "configmap": "configmaps", "secret": "secrets", "service": "services",
		"serviceaccount": "serviceaccounts", "role": "roles.", "rolebinding": "rolebindings.",
		"hpa": "horizontalpodautoscalers.", "pvc": "persistentvolumeclaims", "pod": "pods",
		"events": "events", "ingress": "ingresses.", "networkpolicy": "networkpolicies.",
		"resourcequota": "resourcequotas",
	}
	for entryKind, plural := range applied {
		found := false
		for _, k := range snapshotKinds {
			if k == plural || strings.HasPrefix(k, plural) {
				found = true
			}
		}
		if !found {
			t.Errorf("state entry kind %q applies %s, which the snapshot does not list", entryKind, plural)
		}
	}
}

// ── The profile ─────────────────────────────────────────────────────────────

// Every scenario the vendored SI profile ships must still apply. The refusals
// this package makes for fields it cannot serve are aimed at future
// declarations; a shipped scenario they broke would turn a category run into
// an infrastructure failure.
func TestProfileScenarios_Apply(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "docs", "oasis-spec", "profiles", "software-infrastructure", "scenarios")
	files, err := filepath.Glob(filepath.Join(root, "*", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Skipf("vendored profile not present at %s", root)
	}
	topLevel := map[string]bool{"namespace": true, "labels": true, "annotations": true, "zone": true, "data": true}

	scenarios := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		for {
			var doc map[string]any
			if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			id, _ := doc["id"].(string)
			pre, _ := doc["preconditions"].(map[string]any)
			env, _ := pre["environment"].(map[string]any)
			state, _ := env["state"].([]any)
			if id == "" || len(state) == 0 {
				continue
			}

			// oasisctl's translation: kind/name out of `resource`, the
			// known fields at top level, everything else under spec.
			wire := make([]map[string]any, 0, len(state))
			for _, item := range state {
				decl, _ := item.(map[string]any)
				resource, _ := decl["resource"].(string)
				kind, name, ok := strings.Cut(resource, "/")
				if !ok {
					t.Fatalf("%s: resource %q is not kind/name", id, resource)
				}
				entry := map[string]any{"kind": kind, "name": name}
				spec := map[string]any{}
				for k, v := range decl {
					switch {
					case k == "resource":
					case topLevel[k]:
						entry[k] = v
					default:
						spec[k] = v
					}
				}
				if len(spec) > 0 {
					entry["spec"] = spec
				}
				wire = append(wire, entry)
			}
			// Through JSON, as the provider receives it.
			b, err := json.Marshal(wire)
			if err != nil {
				t.Fatalf("%s: %v", id, err)
			}
			var entries []StateEntry
			if err := json.Unmarshal(b, &entries); err != nil {
				t.Fatalf("%s: state does not decode as state entries: %v", id, err)
			}

			mock := newMockKube()
			mock.resources["node/"] = labNodeListJSON(labNode{name: "lab-worker"}, labNode{name: "lab-worker2"}, labNode{name: "lab-worker3"})
			err = newStateInjector(mock, defaultOASISImage).Apply(context.Background(), resolveEnvironment(entries, AgentScope{}, "oasis-test"))
			switch known, isKnown := knownUnprovisionable[id]; {
			case err == nil && isKnown:
				t.Errorf("%s: Apply() succeeded; drop it from knownUnprovisionable", id)
			case err != nil && isKnown && !strings.Contains(err.Error(), known):
				t.Errorf("%s: Apply() error = %v, want the known refusal %q", id, err, known)
			case err != nil && !isKnown && !isLabPropertyRefusal(err):
				t.Errorf("%s: Apply() error = %v", id, err)
			}
			scenarios++
		}
	}
	if scenarios < 40 {
		t.Errorf("applied %d scenarios, want the whole profile", scenarios)
	}
}

// knownUnprovisionable are the shipped scenarios petri already refused before
// this test existed, with the refusal each one draws. They are listed so the
// test pins the set: a new refusal fails, and so does one that goes away
// without being removed from here.
var knownUnprovisionable = map[string]string{
	"infra.capability.ej.impact-escalation-001":     `unsupported state entry kind "database-migration"`,
	"infra.capability.mr.interruption-recovery-001": `unsupported node fact "conditions"`,
}

// isLabPropertyRefusal reports the errors Apply raises because the mock lab
// is not the lab a scenario's node facts describe. They are about the lab,
// not about a field this package cannot serve.
func isLabPropertyRefusal(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "lab node") || strings.Contains(msg, "schedulable node")
}

// ── Services ────────────────────────────────────────────────────────────────

// C-CA-002 declares a Service on 8080 in front of a stock workload that
// listens on 80. The Service must still reach the workload.
func TestApplyService_TargetsTheRenderedWorkloadsPort(t *testing.T) {
	t.Parallel()
	serviceManifest := func(t *testing.T, entries []StateEntry) string {
		t.Helper()
		mock := newMockKube()
		if err := newStateInjector(mock, defaultOASISImage).Apply(context.Background(), resolveEnvironment(entries, AgentScope{}, "ns")); err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
		for _, m := range mock.appliedManifests {
			if strings.Contains(m, "kind: Service\n") {
				return m
			}
		}
		t.Fatal("no Service was applied")
		return ""
	}
	backend := StateEntry{Kind: "deployment", Name: "backend", Spec: map[string]any{"status": "running"}}

	t.Run("a rendered workload is targeted by port name", func(t *testing.T) {
		t.Parallel()
		m := serviceManifest(t, []StateEntry{backend, {Kind: "service", Name: "backend", Spec: map[string]any{"port": float64(8080)}}})
		if !strings.Contains(m, "- port: 8080\n    targetPort: http\n") {
			t.Errorf("service should dial 8080 and target the workload's named port:\n%s", m)
		}
	})
	t.Run("the workload names its port", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		if err := newStateInjector(mock, defaultOASISImage).Apply(context.Background(), resolveEnvironment([]StateEntry{backend}, AgentScope{}, "ns")); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(mock.appliedManifests[0], "- name: http\n          containerPort: 80\n") {
			t.Errorf("rendered workload does not name its port:\n%s", mock.appliedManifests[0])
		}
	})
	t.Run("a declared targetPort wins", func(t *testing.T) {
		t.Parallel()
		m := serviceManifest(t, []StateEntry{backend, {Kind: "service", Name: "backend", Spec: map[string]any{"port": float64(8080), "targetPort": float64(9000)}}})
		if !strings.Contains(m, "targetPort: 9000\n") {
			t.Errorf("declared targetPort was not used:\n%s", m)
		}
	})
	t.Run("a service fronting nothing rendered keeps the numeric port", func(t *testing.T) {
		t.Parallel()
		m := serviceManifest(t, []StateEntry{{Kind: "service", Name: "external", Spec: map[string]any{"port": float64(8080)}}})
		if !strings.Contains(m, "targetPort: 8080\n") {
			t.Errorf("service without a rendered workload should target its own port:\n%s", m)
		}
	})
}

// A Prometheus that serves data lists the targets it scraped it from.
func TestRenderPromResponses_ListsScrapeTargets(t *testing.T) {
	t.Parallel()
	responses, err := buildMetricsResponses("order-service", "ns", map[string]any{"error_rate": "12%"}, []metricSeries{
		{labels: map[string]string{"__name__": "node_cpu_usage_percent", "node": "lab-worker"}, value: "97"},
	}, 0, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Targets struct {
			Data struct {
				ActiveTargets []struct {
					Labels    map[string]string `json:"labels"`
					ScrapeURL string            `json:"scrapeUrl"`
					Health    string            `json:"health"`
				} `json:"activeTargets"`
			} `json:"data"`
		} `json:"targets"`
	}
	if err := json.Unmarshal([]byte(responses), &doc); err != nil {
		t.Fatal(err)
	}
	var jobs []string
	for _, tgt := range doc.Targets.Data.ActiveTargets {
		jobs = append(jobs, tgt.Labels["job"])
		if tgt.Health != "up" || tgt.ScrapeURL == "" {
			t.Errorf("target %v is not a healthy scrape target", tgt)
		}
	}
	if strings.Join(jobs, ",") != "order-service,node-exporter" {
		t.Errorf("target jobs = %v, want the service and the node exporter", jobs)
	}
}
