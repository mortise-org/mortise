# Grafana + Prometheus for Mortise

Everything the built-in dashboard shows is also exported in Prometheus
exposition format, so power users get Grafana depth with zero Mortise
changes. Two scrape targets:

| Target | Endpoint | What it exports |
|---|---|---|
| Observer (`mortise-observer` Service, deps namespace, port 9091) | `GET /metrics` | Per-app resource, PVC, and HTTP traffic series + observer self-health |
| Operator (controller-manager metrics port) | `GET /metrics` | Build outcomes/durations, App phase, controller-runtime internals |

The full metric reference lives in SPEC §5.11b. Series carry
`{project, app, environment}` labels (plus `pod`, `pvc`, `class`,
`quantile` where applicable).

## Scraping with kube-prometheus-stack

Enable the bundled ServiceMonitor (off by default because the CRD only
exists once prometheus-operator is installed):

```bash
helm upgrade mortise mortise/mortise --reuse-values \
  --set observer.prometheus.serviceMonitor.enabled=true
```

If your Prometheus instance filters ServiceMonitors by label (the
kube-prometheus-stack default is `release: <stack-release-name>`), add it:

```bash
  --set observer.prometheus.serviceMonitor.labels.release=kps
```

## Scraping with plain Prometheus

The observer Service ships `prometheus.io/*` annotations by default, so an
annotation-based config picks it up as-is:

```yaml
scrape_configs:
  - job_name: mortise-observer
    kubernetes_sd_configs:
      - role: service
    relabel_configs:
      - source_labels: [__meta_kubernetes_service_annotation_prometheus_io_scrape]
        action: keep
        regex: "true"
      - source_labels: [__meta_kubernetes_service_annotation_prometheus_io_port]
        action: replace
        target_label: __address__
        source_labels: [__meta_kubernetes_service_name, __meta_kubernetes_namespace, __meta_kubernetes_service_annotation_prometheus_io_port]
        regex: (.+);(.+);(.+)
        replacement: $1.$2.svc:$3
```

Or point a static target at
`mortise-observer.<deps-namespace>.svc:9091/metrics`.

## Example dashboard

Import `docs/recipes/mortise-dashboard.json` (Grafana → Dashboards →
Import). It expects a Prometheus datasource and provides:

- **Cluster overview**: app count by phase, build success/failure rate,
  build duration p95, top apps by CPU and memory.
- **Per-app drilldown** (`project` / `app` / `environment` variables):
  CPU, memory, pod restarts, request rate, error ratio, latency
  quantiles, PVC usage vs capacity.

Useful queries to build on:

```promql
# Request rate per app
rate(mortise_app_http_requests_total[5m])

# Error ratio
sum by (project, app) (rate(mortise_app_http_responses_total{class=~"4xx|5xx"}[5m]))
  / sum by (project, app) (rate(mortise_app_http_requests_total[5m]))

# PVC fill fraction
mortise_app_pvc_used_bytes / mortise_app_pvc_capacity_bytes

# Build success ratio over a day
sum(increase(mortise_builds_total{result="success"}[1d]))
  / sum(increase(mortise_builds_total[1d]))

# Observer collector staleness (seconds since last successful cycle)
time() - mortise_observer_collector_last_success_timestamp_seconds
```

## Semantics worth knowing

- `mortise_app_http_*_total` counters reset when the observer restarts —
  normal Prometheus counter semantics; `rate()`/`increase()` handle it.
- `mortise_app_http_latency_seconds{quantile=...}` are gauges of the most
  recent completed traffic bucket, not true summaries — don't average
  them across long ranges; graph them raw.
- Series disappear from the exposition ~5 minutes after their pod/PVC
  goes away.
- The observer's own trustworthiness is visible at
  `mortise_observer_collector_up` — alert on `== 0` before trusting a
  flatline elsewhere.
