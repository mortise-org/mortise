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

Mortise exposes standard controller-runtime metrics plus:

| Metric | Type | Description |
|--------|------|-------------|
| `mortise_apps_total` | Gauge | Total number of App resources |
| `mortise_reconcile_duration_seconds` | Histogram | Reconcile loop duration |
| `mortise_builds_total` | Counter | Build invocations (by status) |
| `mortise_deploys_total` | Counter | Deployment rollouts (by app, env) |

All metrics use the `mortise_` prefix.

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
