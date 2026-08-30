# Monitoring with Prometheus and Grafana

Mortise pods emit standard Prometheus metrics on `/metrics` and structured
logs on stdout. This recipe shows how to set up monitoring with
kube-prometheus-stack.

## Install kube-prometheus-stack

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

helm install monitoring prometheus-community/kube-prometheus-stack \
  -n monitoring --create-namespace \
  --set grafana.adminPassword=changeme
```

This installs Prometheus, Grafana, Alertmanager, and node-exporter.

## ServiceMonitor for Mortise

Create a ServiceMonitor so Prometheus scrapes the Mortise operator:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: mortise
  namespace: mortise-system
  labels:
    release: monitoring   # must match the Helm release label selector
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: mortise
  endpoints:
    - port: api
      path: /metrics
      interval: 30s
```

```bash
kubectl apply -f servicemonitor.yaml
```

## Available metrics

Two scrape targets. The **operator** serves controller-runtime's standard
series (`controller_runtime_*`, `workqueue_*`, Go/process) plus control-plane
facts only it can see. The **observer** (`observer.enabled=true`, its own
ServiceMonitor in the chart) serves per-app resource, traffic, and volume
series it collects from the cluster. Names below are taken from the code;
everything uses the `mortise_` prefix.

### Operator

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `mortise_builds_total` | Counter | `project`, `app`, `result` | Completed builds by result. |
| `mortise_build_duration_seconds` | Histogram | `project`, `app`, `result` | Wall-clock duration of completed builds (buckets 5s–30m). Runs that failed before starting count in the total but not here. |
| `mortise_app_status_phase` | Gauge | `project`, `app`, `phase` | One series per App, value 1; the label carries the phase. |

### Observer — per app

All carry `project`, `app`, `env`, plus the label noted.

| Metric | Type | Extra label | Description |
|--------|------|-------------|-------------|
| `mortise_app_cpu_cores` | Gauge | `pod` | Current CPU usage of an app pod, in cores. |
| `mortise_app_memory_bytes` | Gauge | `pod` | Current memory usage of an app pod. |
| `mortise_app_pod_restarts_total` | Counter | `pod` | Container restarts as reported by the kubelet. |
| `mortise_app_pvc_capacity_bytes` | Gauge | `pvc` | Capacity of an app PVC's filesystem. |
| `mortise_app_pvc_used_bytes` | Gauge | `pvc` | Used bytes on an app PVC's filesystem. |
| `mortise_app_http_requests_total` | Counter | — | HTTP requests routed to the app since observer start. |
| `mortise_app_http_responses_total` | Counter | `class` | HTTP responses by status class (`2xx`…`5xx`) since observer start. |
| `mortise_app_http_request_bytes_total` | Counter | — | Request body bytes received since observer start. |
| `mortise_app_http_response_bytes_total` | Counter | — | Response body bytes sent since observer start. |
| `mortise_app_http_latency_seconds` | Gauge | `quantile` | Latency quantiles over the most recent completed traffic bucket. |

Traffic counters reset when the observer restarts; use `rate()`/`increase()`
and expect a counter reset there, not a gap in the app.

### Observer — self-health

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `mortise_observer_collector_up` | Gauge | `collector` | 1 when the collector's most recent cycle succeeded. |
| `mortise_observer_collector_last_success_timestamp_seconds` | Gauge | `collector` | Unix time of the collector's last successful cycle. |
| `mortise_observer_log_tailers` | Gauge | — | Active pod log tailers. |
| `mortise_observer_log_tailers_skipped` | Gauge | — | Pods not tailed because of the max-log-pods cap. |
| `mortise_observer_log_lines_dropped_total` | Counter | — | Log lines dropped because the ingest channel was full, since observer start. |

Alert on `mortise_observer_collector_up == 0` or a stale
`last_success_timestamp`: an observer that stopped collecting looks like an
app with flat graphs otherwise.

## Grafana dashboard

Import dashboard ID `mortise-overview` (shipped in `docs/grafana/`) or
create your own from the metrics above. A minimal dashboard includes:

- App count over time
- Reconcile latency (p50, p95, p99)
- Build success/failure rate
- Deploy frequency

## Logs

Mortise logs are structured JSON on stdout. Any log aggregator that reads
container stdout works: Loki, Fluentd, Vector, etc.

Example Loki + Promtail setup:

```bash
helm install loki grafana/loki-stack \
  -n monitoring \
  --set promtail.enabled=true \
  --set loki.persistence.enabled=true
```

Query Mortise logs in Grafana:

```logql
{namespace="mortise-system", app="mortise"}
```

## Further reading

- [kube-prometheus-stack chart](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack)
- [controller-runtime metrics](https://book.kubebuilder.io/reference/metrics-reference)
- [Loki documentation](https://grafana.com/docs/loki/latest/)
