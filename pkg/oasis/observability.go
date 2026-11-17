package oasis

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

// This file renders the observability facts a scenario declares — metric
// series, a trace tree, dashboard panels — into the responses the mock
// backends serve. Every builder here is pure.
//
// The rule they share: a declared fact is served through the API an agent
// would query it by, or provision fails. A fact nothing serves reads, in the
// run's evidence, exactly like an agent that did not look.

// ── Metrics ─────────────────────────────────────────────────────────────────

// metricsReservedFields are the metrics spec fields with a meaning of their
// own. Every other field is a declared metric fact.
var metricsReservedFields = map[string]bool{
	"metric_name":        true,
	"value":              true,
	"labels":             true,
	"memory_usage_trend": true,
	"last_oom_kill":      true,
	"series":             true,
}

// rangeResolution is roughly how many samples a declared time series is
// served with. A scenario declares a handful of points over hours; a real
// backend returns a regular series, so the gaps are filled linearly.
const rangeResolution = 24

// declaredMetricFacts renders every metric fact a metrics entry declares
// beyond the reserved forms:
//
//   - `series: {<name>: [{t, value}, ...]}` — named time series (C-OI-001);
//   - `<name>: [{t, value}, ...]` — a time series named by its field (C-CA-003's
//     error_rate);
//   - `<name>: <scalar>` — an instant gauge (C-DA-002's p99_latency_ms).
//
// Values carry their unit in the scenario's own spelling and are served in a
// base unit with the conventional suffix: "12%" is `<name>_percent 12`,
// "800ms" is `<name>_seconds 0.8`, "1.6Gi" is bytes. A fact that cannot be
// read is an error, not a field quietly left unserved.
func declaredMetricFacts(name, namespace string, spec map[string]any, now time.Time) ([]metricSeries, error) {
	facts := make(map[string]any)
	for k, v := range spec {
		if !metricsReservedFields[k] {
			facts[k] = v
		}
	}
	if raw, ok := spec["series"]; ok {
		named, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("metrics %q: series must map a metric name to its points", name)
		}
		for k, v := range named {
			if _, dup := facts[k]; dup {
				return nil, fmt.Errorf("metrics %q: %q is declared both as a field and under series", name, k)
			}
			facts[k] = v
		}
	}

	keys := make([]string, 0, len(facts))
	for k := range facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []metricSeries
	for _, k := range keys {
		labels := map[string]string{"namespace": namespace, "service": name}
		if points, ok := facts[k].([]any); ok {
			s, err := timeSeriesFact(k, points, labels, now)
			if err != nil {
				return nil, fmt.Errorf("metrics %q: %w", name, err)
			}
			out = append(out, s)
			continue
		}
		value, suffix, err := parseMetricValue(facts[k])
		if err != nil {
			return nil, fmt.Errorf("metrics %q: %s: %w", name, k, err)
		}
		labels["__name__"] = metricName(k, suffix)
		out = append(out, metricSeries{labels: labels, value: formatSample(value)})
	}
	return out, nil
}

// timeSeriesFact renders one declared `[{t, value}, ...]` series.
func timeSeriesFact(field string, points []any, labels map[string]string, now time.Time) (metricSeries, error) {
	type point struct {
		at    time.Time
		value float64
	}
	if len(points) == 0 {
		return metricSeries{}, fmt.Errorf("%s: declares no points", field)
	}
	var (
		parsed []point
		suffix string
	)
	for i, raw := range points {
		p, ok := raw.(map[string]any)
		if !ok {
			return metricSeries{}, fmt.Errorf("%s: point %d must be {t, value}", field, i)
		}
		at, err := parseRelativeTime(p["t"], now)
		if err != nil {
			return metricSeries{}, fmt.Errorf("%s: point %d: %w", field, i, err)
		}
		value, unit, err := parseMetricValue(p["value"])
		if err != nil {
			return metricSeries{}, fmt.Errorf("%s: point %d: %w", field, i, err)
		}
		if i > 0 && unit != suffix {
			return metricSeries{}, fmt.Errorf("%s: points mix units", field)
		}
		suffix = unit
		parsed = append(parsed, point{at: at, value: value})
	}
	sort.SliceStable(parsed, func(i, j int) bool { return parsed[i].at.Before(parsed[j].at) })

	window := parsed[len(parsed)-1].at.Sub(parsed[0].at)
	var samples []metricSample
	for i, p := range parsed {
		samples = append(samples, metricSample{at: p.at, value: formatSample(p.value)})
		if i == len(parsed)-1 || window <= 0 {
			continue
		}
		next := parsed[i+1]
		gap := next.at.Sub(p.at)
		fill := int(float64(rangeResolution)*float64(gap)/float64(window)+0.5) - 1
		for j := 1; j <= fill; j++ {
			frac := float64(j) / float64(fill+1)
			samples = append(samples, metricSample{
				at:    p.at.Add(time.Duration(float64(gap) * frac)),
				value: formatSample(p.value + (next.value-p.value)*frac),
			})
		}
	}

	labels["__name__"] = metricName(field, suffix)
	return metricSeries{
		labels:       labels,
		value:        samples[len(samples)-1].value,
		rangeSamples: samples,
	}, nil
}

// formatSample renders a sample value the way Prometheus does: plain decimal,
// never an exponent. A memory series in bytes is read by people and by agents.
func formatSample(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// metricName appends the unit suffix a value was normalised to, unless the
// declared name already says its unit.
func metricName(field, suffix string) string {
	name := sanitizeMetricName(field)
	if suffix == "" || strings.HasSuffix(name, suffix) {
		return name
	}
	for _, unit := range []string{"_ms", "_seconds", "_bytes", "_percent", "_ratio"} {
		if strings.HasSuffix(name, unit) {
			return name
		}
	}
	return name + suffix
}

var metricNameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_:]+`)

// sanitizeMetricName folds a declared field or panel title into a legal
// Prometheus metric name.
func sanitizeMetricName(s string) string {
	name := strings.Trim(metricNameUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "_"), "_")
	if name == "" {
		return "metric"
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}

var leadingNumber = regexp.MustCompile(`^[-+]?[0-9]*\.?[0-9]+`)

// parseMetricValue reads a declared metric value into a base-unit number and
// the name suffix that unit conventionally carries: a percentage stays a
// percentage (`_percent`, matching node_cpu_usage_percent), a duration
// becomes seconds, a Kubernetes quantity becomes its plain number (bytes for
// a memory size). A bare number has no suffix.
func parseMetricValue(v any) (float64, string, error) {
	switch x := v.(type) {
	case float64:
		return x, "", nil
	case int:
		return float64(x), "", nil
	case int64:
		return float64(x), "", nil
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, "", fmt.Errorf("empty value")
		}
		if num, ok := strings.CutSuffix(s, "%"); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
			if err != nil {
				return 0, "", fmt.Errorf("cannot parse %q as a percentage", x)
			}
			return f, "_percent", nil
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, "", nil
		}
		for _, unit := range []struct {
			suffix string
			scale  float64
		}{{"ms", 0.001}, {"s", 1}} {
			if num, ok := strings.CutSuffix(s, unit.suffix); ok {
				if f, err := strconv.ParseFloat(strings.TrimSpace(num), 64); err == nil {
					return f * unit.scale, "_seconds", nil
				}
			}
		}
		if q, err := resource.ParseQuantity(s); err == nil {
			return q.AsApproximateFloat64(), "", nil
		}
		return 0, "", fmt.Errorf("cannot parse %q as a metric value", x)
	default:
		return 0, "", fmt.Errorf("unsupported type %T for a metric value", v)
	}
}

// parseRelativeTime reads a declared sample time: "now", or an offset into
// the past such as "-6h" or "-30m".
func parseRelativeTime(v any, now time.Time) (time.Time, error) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, fmt.Errorf("t must be %q or an offset like %q", "now", "-6h")
	}
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "now") {
		return now, nil
	}
	offset, ok := strings.CutPrefix(s, "-")
	if !ok {
		return time.Time{}, fmt.Errorf("t %q must be %q or an offset into the past like %q", s, "now", "-6h")
	}
	d, err := time.ParseDuration(offset)
	if err != nil {
		return time.Time{}, fmt.Errorf("t %q: %w", s, err)
	}
	return now.Add(-d), nil
}

// ── Traces ──────────────────────────────────────────────────────────────────

// declaredSpan is one span of a declared trace, flattened in tree order.
type declaredSpan struct {
	id, parentID string
	service      string
	operation    string
	start        time.Time
	duration     time.Duration
	failed       bool
	attributes   map[string]any
}

// declaredTrace is a scenario's trace: an id and its spans, root first.
type declaredTrace struct {
	id    string
	spans []declaredSpan
}

// traceAge is how long before the Apply the served trace starts.
const traceAge = 5 * time.Minute

// parseDeclaredTrace reads a traces entry. Two spellings of the tree are
// recognised — `root_span: {...}` (C-OI-003) and `spans: [{...}]` (C-DA-002)
// — each span carrying `operation` or `name`, `duration_ms`, optionally
// `service`, `status`, `attributes` and `children`. A span with no service
// inherits its parent's; a root with none takes the first of `services`, or
// the entry's name. An entry declaring no tree at all gets a single root
// span, as it always has.
func parseDeclaredTrace(name string, spec map[string]any, now time.Time) (declaredTrace, error) {
	trace := declaredTrace{id: name}
	if v, ok := spec["trace_id"].(string); ok && v != "" {
		trace.id = v
	}
	rootService := name
	if list, ok := spec["services"].([]any); ok && len(list) > 0 {
		if s, ok := list[0].(string); ok && s != "" {
			rootService = s
		}
	}

	var roots []map[string]any
	if raw, ok := spec["root_span"]; ok {
		root, ok := raw.(map[string]any)
		if !ok {
			return declaredTrace{}, fmt.Errorf("traces %q: root_span must be a span", name)
		}
		roots = append(roots, root)
	}
	if raw, ok := spec["spans"]; ok {
		list, ok := raw.([]any)
		if !ok {
			return declaredTrace{}, fmt.Errorf("traces %q: spans must be a list of spans", name)
		}
		for i, item := range list {
			span, ok := item.(map[string]any)
			if !ok {
				return declaredTrace{}, fmt.Errorf("traces %q: spans[%d] must be a span", name, i)
			}
			roots = append(roots, span)
		}
	}
	if len(roots) == 0 {
		roots = []map[string]any{{"operation": "root", "duration_ms": float64(1000)}}
	}

	start := now.Add(-traceAge)
	for _, root := range roots {
		index := len(trace.spans)
		if err := trace.walk(name, root, "", rootService, start); err != nil {
			return declaredTrace{}, err
		}
		start = start.Add(trace.spans[index].duration)
	}
	return trace, nil
}

// walk appends raw and its children, depth first. Children are laid end to
// end inside their parent, centred in whatever time the parent has to spare,
// so every child span sits within the span that called it.
func (t *declaredTrace) walk(entry string, raw map[string]any, parentID, inheritedService string, start time.Time) error {
	span := declaredSpan{
		id:         spanID(t.id, len(t.spans)),
		parentID:   parentID,
		service:    inheritedService,
		start:      start,
		attributes: map[string]any{},
	}
	if v, ok := raw["service"].(string); ok && v != "" {
		span.service = v
	}
	for _, key := range []string{"operation", "name"} {
		if v, ok := raw[key].(string); ok && v != "" {
			span.operation = v
			break
		}
	}
	if span.operation == "" {
		return fmt.Errorf("traces %q: a span declares neither operation nor name", entry)
	}
	ms, _, err := parseMetricValue(raw["duration_ms"])
	if err != nil {
		return fmt.Errorf("traces %q: span %q: duration_ms: %w", entry, span.operation, err)
	}
	span.duration = time.Duration(ms * float64(time.Millisecond))
	if v, ok := raw["status"].(string); ok && strings.EqualFold(v, "error") {
		span.failed = true
	}
	if attrs, ok := raw["attributes"].(map[string]any); ok {
		span.attributes = attrs
	}
	index := len(t.spans)
	t.spans = append(t.spans, span)

	var children []map[string]any
	if raw, ok := raw["children"]; ok {
		list, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("traces %q: span %q: children must be a list of spans", entry, span.operation)
		}
		for _, item := range list {
			child, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("traces %q: span %q: children must be a list of spans", entry, span.operation)
			}
			children = append(children, child)
		}
	}
	var childTotal time.Duration
	for _, child := range children {
		ms, _, err := parseMetricValue(child["duration_ms"])
		if err != nil {
			continue // reported by the child's own walk
		}
		childTotal += time.Duration(ms * float64(time.Millisecond))
	}
	childStart := start
	if slack := span.duration - childTotal; slack > 0 {
		childStart = start.Add(slack / 2)
	}
	for _, child := range children {
		if err := t.walk(entry, child, t.spans[index].id, span.service, childStart); err != nil {
			return err
		}
		childStart = childStart.Add(t.lastDurationOf(t.spans[index].id))
	}
	return nil
}

// lastDurationOf returns the duration of the most recently added direct
// child of parentID.
func (t *declaredTrace) lastDurationOf(parentID string) time.Duration {
	for i := len(t.spans) - 1; i >= 0; i-- {
		if t.spans[i].parentID == parentID {
			return t.spans[i].duration
		}
	}
	return 0
}

// spanID derives a stable 16-hex-digit span id from the trace id and the
// span's position in the tree.
func spanID(traceID string, index int) string {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s/%d", traceID, index) // hash.Hash.Write never errors
	return fmt.Sprintf("%016x", h.Sum64())
}

// services returns the trace's distinct services, in tree order.
func (t declaredTrace) services() []string {
	var out []string
	seen := make(map[string]bool)
	for _, s := range t.spans {
		if !seen[s.service] {
			seen[s.service] = true
			out = append(out, s.service)
		}
	}
	return out
}

// sortedAttributeKeys returns a span's attribute keys in a stable order.
func sortedAttributeKeys(attrs map[string]any) []string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// buildJaegerResponses renders the trace as Jaeger's query API returns it:
// `services` for /api/services, and `trace` for both /api/traces/{id} and the
// /api/traces search, which share one envelope.
func buildJaegerResponses(trace declaredTrace) (string, error) {
	services := trace.services()
	processes := make(map[string]any, len(services))
	processOf := make(map[string]string, len(services))
	for i, svc := range services {
		pid := fmt.Sprintf("p%d", i+1)
		processOf[svc] = pid
		processes[pid] = map[string]any{"serviceName": svc, "tags": []any{}}
	}

	spans := make([]any, 0, len(trace.spans))
	for _, s := range trace.spans {
		references := []any{}
		if s.parentID != "" {
			references = append(references, map[string]any{"refType": "CHILD_OF", "traceID": trace.id, "spanID": s.parentID})
		}
		tags := []any{}
		for _, k := range sortedAttributeKeys(s.attributes) {
			tags = append(tags, jaegerTag(k, s.attributes[k]))
		}
		if s.failed {
			tags = append(tags, map[string]any{"key": "error", "type": "bool", "value": true})
		}
		spans = append(spans, map[string]any{
			"traceID":       trace.id,
			"spanID":        s.id,
			"operationName": s.operation,
			"references":    references,
			"startTime":     s.start.UnixMicro(),
			"duration":      s.duration.Microseconds(),
			"tags":          tags,
			"logs":          []any{},
			"processID":     processOf[s.service],
			"warnings":      nil,
		})
	}

	doc := map[string]any{
		"trace": map[string]any{
			"data": []any{map[string]any{
				"traceID":   trace.id,
				"spans":     spans,
				"processes": processes,
				"warnings":  nil,
			}},
			"total": 0, "limit": 0, "offset": 0, "errors": nil,
		},
		"services": map[string]any{"data": services, "total": len(services), "limit": 0, "offset": 0, "errors": nil},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshaling jaeger responses: %w", err)
	}
	return string(b), nil
}

func jaegerTag(key string, value any) map[string]any {
	switch v := value.(type) {
	case bool:
		return map[string]any{"key": key, "type": "bool", "value": v}
	case float64:
		if v == float64(int64(v)) {
			return map[string]any{"key": key, "type": "int64", "value": int64(v)}
		}
		return map[string]any{"key": key, "type": "float64", "value": v}
	case int:
		return map[string]any{"key": key, "type": "int64", "value": v}
	default:
		return map[string]any{"key": key, "type": "string", "value": fmt.Sprintf("%v", v)}
	}
}

// buildTempoResponses renders the trace as Tempo's HTTP API returns it:
// `trace` (OTLP batches) for /api/traces/{id}, `trace_v2` for the v2 route's
// envelope, `search` for /api/search, and `tag_values` for the service-name
// tag lookup.
func buildTempoResponses(trace declaredTrace) (string, error) {
	services := trace.services()
	batches := make([]any, 0, len(services))
	for _, svc := range services {
		var spans []any
		for _, s := range trace.spans {
			if s.service != svc {
				continue
			}
			attributes := []any{}
			for _, k := range sortedAttributeKeys(s.attributes) {
				attributes = append(attributes, map[string]any{"key": k, "value": otlpValue(s.attributes[k])})
			}
			span := map[string]any{
				"traceId":           trace.id,
				"spanId":            s.id,
				"name":              s.operation,
				"kind":              "SPAN_KIND_SERVER",
				"startTimeUnixNano": strconv.FormatInt(s.start.UnixNano(), 10),
				"endTimeUnixNano":   strconv.FormatInt(s.start.Add(s.duration).UnixNano(), 10),
				"attributes":        attributes,
				"status":            map[string]any{},
			}
			if s.parentID != "" {
				span["parentSpanId"] = s.parentID
				span["kind"] = "SPAN_KIND_INTERNAL"
			}
			if s.failed {
				span["status"] = map[string]any{"code": "STATUS_CODE_ERROR"}
			}
			spans = append(spans, span)
		}
		batches = append(batches, map[string]any{
			"resource": map[string]any{"attributes": []any{
				map[string]any{"key": "service.name", "value": map[string]any{"stringValue": svc}},
			}},
			"scopeSpans": []any{map[string]any{"scope": map[string]any{}, "spans": spans}},
		})
	}

	root := trace.spans[0]
	tagValues := make([]any, 0, len(services))
	for _, svc := range services {
		tagValues = append(tagValues, map[string]any{"type": "string", "value": svc})
	}
	doc := map[string]any{
		"trace":    map[string]any{"batches": batches},
		"trace_v2": map[string]any{"trace": map[string]any{"resourceSpans": batches}},
		"search": map[string]any{
			"traces": []any{map[string]any{
				"traceID":           trace.id,
				"rootServiceName":   root.service,
				"rootTraceName":     root.operation,
				"startTimeUnixNano": strconv.FormatInt(root.start.UnixNano(), 10),
				"durationMs":        root.duration.Milliseconds(),
			}},
			"metrics": map[string]any{"inspectedTraces": 1, "inspectedBytes": "0", "completedJobs": 1, "totalJobs": 1},
		},
		"tag_values": map[string]any{"tagValues": tagValues},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshaling tempo responses: %w", err)
	}
	return string(b), nil
}

func otlpValue(value any) map[string]any {
	switch v := value.(type) {
	case bool:
		return map[string]any{"boolValue": v}
	case float64:
		if v == float64(int64(v)) {
			return map[string]any{"intValue": strconv.FormatInt(int64(v), 10)}
		}
		return map[string]any{"doubleValue": v}
	case int:
		return map[string]any{"intValue": strconv.Itoa(v)}
	default:
		return map[string]any{"stringValue": fmt.Sprintf("%v", v)}
	}
}

// ── Dashboards ──────────────────────────────────────────────────────────────

// dashboardPanel is one declared panel, read for serving.
type dashboardPanel struct {
	id          int
	title       string
	kind        string
	metric      string
	value       string
	hasValue    bool
	unit        string
	threshold   *float64
	description string
}

// dashboardDatasourceUID is the uid the served dashboard's panels and its
// datasource listing agree on.
const dashboardDatasourceUID = "prometheus"

// parseDashboardPanels reads a dashboard entry's panels. A panel's
// `current_value` is what a viewer would see on it; it is served as the
// instant value of the metric the panel queries, so the reading is reachable
// the way a dashboard's readings are — through its datasource — and the panel
// JSON stays what Grafana's is: a query, a unit and thresholds. `status` is
// the scenario's verdict on the panel and is deliberately not served.
func parseDashboardPanels(name string, spec map[string]any) ([]dashboardPanel, error) {
	raw, ok := spec["panels"]
	if !ok {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("dashboard %q: panels must be a list", name)
	}
	seen := make(map[string]bool)
	var out []dashboardPanel
	for i, item := range list {
		// A panel declared by name alone (`panels: [cpu_usage]`) is a
		// panel with a title and no reading.
		if title, ok := item.(string); ok {
			item = map[string]any{"title": title}
		}
		p, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("dashboard %q: panels[%d] must be a panel", name, i)
		}
		panel := dashboardPanel{id: i + 1, kind: "timeseries", unit: "none"}
		if v, ok := p["title"].(string); ok {
			panel.title = v
		}
		if panel.title == "" {
			panel.title = fmt.Sprintf("Panel %d", i+1)
		}
		if v, ok := p["type"].(string); ok && v != "" {
			panel.kind = v
		}
		suffix := ""
		if v, ok := p["current_value"]; ok {
			number, unit, err := parsePanelReading(v)
			if err != nil {
				return nil, fmt.Errorf("dashboard %q: panel %q: current_value: %w", name, panel.title, err)
			}
			panel.value, panel.hasValue, panel.unit = formatSample(number), true, unit
			if unit == "percent" {
				suffix = "_percent"
			}
		}
		panel.metric = metricName(panel.title, suffix)
		if seen[panel.metric] {
			return nil, fmt.Errorf("dashboard %q: two panels are titled %q", name, panel.title)
		}
		seen[panel.metric] = true
		if v, ok := p["threshold"]; ok {
			number, _, err := parsePanelReading(v)
			if err != nil {
				return nil, fmt.Errorf("dashboard %q: panel %q: threshold: %w", name, panel.title, err)
			}
			panel.threshold = &number
		}
		if v, ok := p["normal_baseline"]; ok {
			panel.description = fmt.Sprintf("Normal baseline: %v", v)
		}
		out = append(out, panel)
	}
	return out, nil
}

// parsePanelReading reads a panel value as a viewer sees it — "92%",
// "1200 req/s", "12% (5xx)", 0 — into its leading number and a Grafana unit.
func parsePanelReading(v any) (float64, string, error) {
	switch x := v.(type) {
	case float64:
		return x, "none", nil
	case int:
		return float64(x), "none", nil
	case string:
		s := strings.TrimSpace(x)
		num := leadingNumber.FindString(s)
		if num == "" {
			return 0, "", fmt.Errorf("%q does not start with a number", x)
		}
		f, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return 0, "", fmt.Errorf("cannot parse %q as a number", x)
		}
		rest := strings.TrimSpace(s[len(num):])
		switch {
		case strings.HasPrefix(rest, "%"):
			return f, "percent", nil
		case strings.HasPrefix(rest, "req/s"):
			return f, "reqps", nil
		}
		return f, "none", nil
	default:
		return 0, "", fmt.Errorf("unsupported type %T", v)
	}
}

// dashboardPanelSeries returns the metric series behind a dashboard's panels:
// one instant gauge per panel that declares a reading.
func dashboardPanelSeries(name, namespace string, spec map[string]any) ([]metricSeries, error) {
	panels, err := parseDashboardPanels(name, spec)
	if err != nil {
		return nil, err
	}
	uid := dashboardUID(name, spec)
	var out []metricSeries
	for _, p := range panels {
		if !p.hasValue {
			continue
		}
		out = append(out, metricSeries{
			labels: map[string]string{"__name__": p.metric, "namespace": namespace, "dashboard": uid},
			value:  p.value,
		})
	}
	return out, nil
}

func dashboardUID(name string, spec map[string]any) string {
	if v, ok := spec["uid"].(string); ok && v != "" {
		return v
	}
	return name
}

func dashboardTitle(name string, spec map[string]any) string {
	if v, ok := spec["title"].(string); ok && v != "" {
		return v
	}
	return name
}

// buildGrafanaResponses renders a dashboard entry as Grafana's HTTP API
// returns it: `dashboard` for /api/dashboards/uid/{uid}, `search` for
// /api/search, and `datasources` naming the Prometheus the panels query.
// datasourceURL is that Prometheus as the cluster addresses it.
func buildGrafanaResponses(name string, spec map[string]any, datasourceURL string, now time.Time) (string, error) {
	panels, err := parseDashboardPanels(name, spec)
	if err != nil {
		return "", err
	}
	uid, title := dashboardUID(name, spec), dashboardTitle(name, spec)
	datasource := map[string]any{"type": "prometheus", "uid": dashboardDatasourceUID}

	rendered := make([]any, 0, len(panels))
	for i, p := range panels {
		steps := []any{map[string]any{"color": "green", "value": nil}}
		if p.threshold != nil {
			steps = append(steps, map[string]any{"color": "red", "value": *p.threshold})
		}
		rendered = append(rendered, map[string]any{
			"id":          p.id,
			"title":       p.title,
			"type":        p.kind,
			"description": p.description,
			"datasource":  datasource,
			"gridPos":     map[string]any{"h": 8, "w": 12, "x": 12 * (i % 2), "y": 8 * (i / 2)},
			"targets": []any{map[string]any{
				"refId":      "A",
				"datasource": datasource,
				"expr":       fmt.Sprintf("%s{dashboard=%q}", p.metric, uid),
			}},
			"fieldConfig": map[string]any{"defaults": map[string]any{
				"unit":       p.unit,
				"thresholds": map[string]any{"mode": "absolute", "steps": steps},
			}},
		})
	}

	slug := sanitizeMetricName(title)
	path := "/d/" + uid + "/" + strings.ReplaceAll(slug, "_", "-")
	doc := map[string]any{
		"dashboard": map[string]any{
			"dashboard": map[string]any{
				"id":            1,
				"uid":           uid,
				"title":         title,
				"tags":          []any{},
				"timezone":      "browser",
				"schemaVersion": 39,
				"version":       1,
				"panels":        rendered,
			},
			"meta": map[string]any{
				"type":    "db",
				"slug":    strings.ReplaceAll(slug, "_", "-"),
				"url":     path,
				"updated": now.UTC().Format(time.RFC3339),
				"created": now.UTC().Format(time.RFC3339),
				"version": 1,
			},
		},
		"search": []any{map[string]any{
			"id":    1,
			"uid":   uid,
			"title": title,
			"uri":   "db/" + strings.ReplaceAll(slug, "_", "-"),
			"url":   path,
			"type":  "dash-db",
			"tags":  []any{},
		}},
		"datasources": []any{map[string]any{
			"id":        1,
			"uid":       dashboardDatasourceUID,
			"name":      "Prometheus",
			"type":      "prometheus",
			"access":    "proxy",
			"url":       datasourceURL,
			"isDefault": true,
		}},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshaling grafana responses: %w", err)
	}
	return string(b), nil
}
