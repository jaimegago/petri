# Observability

## Petri Metrics

When `observability.metrics_enabled` is `true` in config, Petri exposes Prometheus metrics at `http://localhost:<metrics_port>/metrics` (default port 9090).

### Counters

| Metric | Labels | Description |
|--------|--------|-------------|
| `petri_labs_created_total` | company, level, provider | Total labs created |
| `petri_labs_destroyed_total` | company, level, provider, reason | Total labs destroyed (reason: manual, ttl_expired, error) |

### Gauges

| Metric | Description |
|--------|-------------|
| `petri_labs_active` | Current number of active labs |

### Histograms

| Metric | Labels | Buckets (seconds) | Description |
|--------|--------|--------------------|-------------|
| `petri_lab_create_duration_seconds` | company, level, provider | 10, 30, 60, 120, 300, 600, 1200 | Lab creation duration |
| `petri_lab_destroy_duration_seconds` | company, level, provider | 5, 15, 30, 60, 120, 300 | Lab destruction duration |

### Health Endpoint

`/healthz` returns HTTP 200 with body `ok`.

## Structured Logs

Petri outputs structured JSON logs to stdout via zerolog:

```json
{
  "level": "info",
  "time": "2024-03-01T10:30:45Z",
  "lab_id": "abc123",
  "company": "acme",
  "phase": "terraform_apply",
  "duration_ms": 45231,
  "msg": "EKS cluster created"
}
```

Log level configurable via `~/.petri/config.yaml` (`observability.log_level`) or `PETRI_LOG_LEVEL` env var.
