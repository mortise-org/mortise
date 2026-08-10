# Observer pipeline reliability — audit and contract (obs-v2 / O1)

The audit behind the "shotty tracking" verdict, what each gap was, and what
now guarantees the contract. File references are to `cmd/observer/`.

## The reliability contract

1. **Gaps are visible.** Every collector cycle writes a heartbeat row
   (`collector_ticks`). A time window with a successful heartbeat but no
   series rows was *observed empty*; a window without one was *not observed*
   and is served as a gap (`coverage` array on `/v1/metrics` and `/v1/pvc`,
   `[bucket, 0|1]`). Consumers render 0-buckets as gaps and never interpolate
   across them.
2. **Restarts resume; they do not duplicate or silently lose.**
3. **Storage is bounded** by age-based retention plus downsampling.
4. **The observer reports its own health** (`/v1/health/collectors`), so a
   flatline can be labeled "metrics stale since X" instead of lying.

## Gap list (what was actually wrong)

| # | Gap | Where | Fix |
|---|-----|-------|-----|
| G1 | Namespace-list failure aborted the whole metrics tick with only a log line — cluster-wide silent series gap | `metrics_collector.go collect()` | Failed tick recorded; window renders as a gap |
| G2 | Per-namespace pod-metrics failure silently skipped the namespace | same | Partial-error recorded on the tick |
| G3 | A tick collecting zero entries wrote nothing — "no pods" indistinguishable from "collector dead" | same (`len(entries) > 0` guard) | Heartbeat row distinguishes observed-empty from not-observed |
| G4 | Query layer interpolated over unobserved windows (only buckets with rows exist; UIs connect the dots) | `store.go QueryMetrics` | `coverage` field; UI contract (O4) renders gaps |
| G5 | Log tailer (re)start used `TailLines=100`: every restart re-ingested up to 100 duplicate lines (no dedup) and lost anything beyond 100 written during downtime | `log_collector.go tailPod()` | Resume cursor: `SinceTime` from the last stored line per pod, truncated down to the second (bounded ≤1s re-read, never a skip); `TailLines` only on first-ever tail |
| G6 | Traffic accumulator was memory-only and shutdown did not flush — up to a full bucket of traffic died with every restart | `traffic_collector.go Run()` | Final `flushAll()` on shutdown drains all buckets including the open one |
| G7 | A failed traffic insert dropped the batch permanently (already deleted from the accumulator) | `traffic_collector.go flush()` | Failed batches move to `pending` and retry next flush — transient SQLite errors delay data instead of losing it |
| G8 | No retention beyond age cutoffs; raw per-poll-interval metric rows grew unbounded within the window | `store.go Trim()` | Policy: raw for 24h → 5-minute averages until the retention cutoff → deleted. Idempotent in-place downsampling each trim cycle |
| G9 | No collector self-health: last-success, tailer counts, and dropped-line counters existed internally but were invisible | all collectors | `/v1/health/collectors`: per-collector last tick/success/error + gauges (`logTailers`, `logTailersSkipped`, `logLinesDropped`) |
| G10 | `maxPods` cap silently un-tailed every pod beyond 100 — whole apps without logs, no signal | `log_collector.go startTailer()` | `logTailersSkipped` gauge; the cap is now visible |
| G11 | Log channel overflow dropped lines with only a log-line warning | `log_collector.go flushLoop()` | `logLinesDropped` cumulative gauge |
| G12 | Pod churn race: a new pod is invisible until the next sync tick (≤ poll interval) | by design | Documented contract bound, not a bug: worst-case blind window is one poll interval |

## PVC usage collection (#214)

`pvc_collector.go` samples each node's kubelet Summary API
(`nodes/proxy` → `stats/summary`) — the only interface reporting actual
per-volume filesystem usage. Series are keyed `(namespace, app, env, pvc)`
like every other series and served at `/v1/pvc` with the same coverage
contract. Where clusters block `nodes/proxy` (some managed offerings), the
collector records failed heartbeats and produces no series: metrics absent,
never error spam. RBAC addition (`nodes` get/list, `nodes/proxy` get,
read-only) is justified in `charts/mortise/templates/observer-rbac.yaml`.

## Retention policy (decided here)

| Data | Raw resolution | After 24h | Cutoff |
|------|----------------|-----------|--------|
| Metrics | poll interval (5s default) | 5-min averages | `-metrics-retention` (72h default) |
| Traffic | bucket size (5s default, pre-aggregated) | unchanged | `-traffic-retention` (48h) |
| Logs | per line | unchanged (not downsampleable) | `-log-retention` (48h) |
| Heartbeats, PVC | tick / poll | unchanged | metrics retention |

Long-horizon storage is explicitly out of scope for the observer: that is
the Prometheus surface's job (obs-v2 / O2).
