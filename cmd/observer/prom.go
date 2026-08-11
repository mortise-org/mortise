package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Prometheus text exposition for the observer. Hand-rolled writer instead of
// a client library: every series here is a point-in-time gauge or a simple
// monotonic counter over state the collectors already maintain, and the
// observer binary deliberately stays free of the operator's dependency tree.
//
// The metric set is public API (SPEC §5.11b). Names follow Prometheus
// conventions: base units (cores, bytes, seconds), _total suffix on
// counters, HELP + TYPE on every family.

type resourceKey struct {
	project, app, env, pod string
}

type pvcKey struct {
	project, app, env, pvc string
}

type trafficKey struct {
	project, app, env string
}

type resourceSample struct {
	cpu      float64
	memory   int64
	restarts int64
	seen     time.Time
}

type pvcSample struct {
	capacity int64
	used     int64
	seen     time.Time
}

type trafficTotals struct {
	requests  int64
	status2xx int64
	status3xx int64
	status4xx int64
	status5xx int64
	bytesIn   int64
	bytesOut  int64
	// Latest completed bucket's percentiles, exposed as gauges.
	latencyP50 float64
	latencyP95 float64
	latencyP99 float64
	seen       time.Time
}

// promState is the scrape-time view the collectors feed. Samples older than
// staleAfter drop out of the exposition (a deleted pod must not export
// forever); counter resets on observer restart are normal Prometheus
// semantics and rate() handles them.
type promState struct {
	mu        sync.Mutex
	resources map[resourceKey]resourceSample
	pvcs      map[pvcKey]pvcSample
	traffic   map[trafficKey]*trafficTotals
}

const promStaleAfter = 5 * time.Minute

func newPromState() *promState {
	return &promState{
		resources: make(map[resourceKey]resourceSample),
		pvcs:      make(map[pvcKey]pvcSample),
		traffic:   make(map[trafficKey]*trafficTotals),
	}
}

func (p *promState) SetResource(project, app, env, pod string, cpu float64, memory, restarts int64) {
	p.mu.Lock()
	p.resources[resourceKey{project, app, env, pod}] = resourceSample{
		cpu: cpu, memory: memory, restarts: restarts, seen: time.Now(),
	}
	p.mu.Unlock()
}

func (p *promState) SetPVC(project, app, env, pvc string, capacity, used int64) {
	p.mu.Lock()
	p.pvcs[pvcKey{project, app, env, pvc}] = pvcSample{
		capacity: capacity, used: used, seen: time.Now(),
	}
	p.mu.Unlock()
}

func (p *promState) AddTraffic(project, app, env string, e TrafficEntry) {
	p.mu.Lock()
	k := trafficKey{project, app, env}
	t := p.traffic[k]
	if t == nil {
		t = &trafficTotals{}
		p.traffic[k] = t
	}
	t.requests += e.Requests
	t.status2xx += e.Status2xx
	t.status3xx += e.Status3xx
	t.status4xx += e.Status4xx
	t.status5xx += e.Status5xx
	t.bytesIn += e.BytesIn
	t.bytesOut += e.BytesOut
	t.latencyP50 = e.LatencyP50 / 1000.0 // ms → seconds
	t.latencyP95 = e.LatencyP95 / 1000.0
	t.latencyP99 = e.LatencyP99 / 1000.0
	t.seen = time.Now()
	p.mu.Unlock()
}

func (p *promState) sweep(now time.Time) {
	for k, v := range p.resources {
		if now.Sub(v.seen) > promStaleAfter {
			delete(p.resources, k)
		}
	}
	for k, v := range p.pvcs {
		if now.Sub(v.seen) > promStaleAfter {
			delete(p.pvcs, k)
		}
	}
	for k, v := range p.traffic {
		if now.Sub(v.seen) > promStaleAfter {
			delete(p.traffic, k)
		}
	}
}

// promEscape escapes a label value per the exposition format.
func promEscape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return strings.ReplaceAll(v, "\n", `\n`)
}

type promWriter struct {
	b strings.Builder
}

func (w *promWriter) family(name, help, typ string) {
	fmt.Fprintf(&w.b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func (w *promWriter) sample(name string, labels [][2]string, value float64) {
	w.b.WriteString(name)
	if len(labels) > 0 {
		w.b.WriteByte('{')
		for i, kv := range labels {
			if i > 0 {
				w.b.WriteByte(',')
			}
			fmt.Fprintf(&w.b, `%s="%s"`, kv[0], promEscape(kv[1]))
		}
		w.b.WriteByte('}')
	}
	fmt.Fprintf(&w.b, " %g\n", value)
}

func appEnvLabels(project, app, env string) [][2]string {
	return [][2]string{{"project", project}, {"app", app}, {"environment", env}}
}

// renderMetrics produces the full exposition document. Series are emitted in
// sorted label order so scrapes are deterministic (and testable).
func renderMetrics(state *promState, health *HealthTracker) string {
	state.mu.Lock()
	state.sweep(time.Now())

	resKeys := make([]resourceKey, 0, len(state.resources))
	for k := range state.resources {
		resKeys = append(resKeys, k)
	}
	sort.Slice(resKeys, func(i, j int) bool {
		return fmt.Sprint(resKeys[i]) < fmt.Sprint(resKeys[j])
	})
	pvcKeys := make([]pvcKey, 0, len(state.pvcs))
	for k := range state.pvcs {
		pvcKeys = append(pvcKeys, k)
	}
	sort.Slice(pvcKeys, func(i, j int) bool {
		return fmt.Sprint(pvcKeys[i]) < fmt.Sprint(pvcKeys[j])
	})
	trafKeys := make([]trafficKey, 0, len(state.traffic))
	for k := range state.traffic {
		trafKeys = append(trafKeys, k)
	}
	sort.Slice(trafKeys, func(i, j int) bool {
		return fmt.Sprint(trafKeys[i]) < fmt.Sprint(trafKeys[j])
	})

	w := &promWriter{}

	w.family("mortise_app_cpu_cores", "Current CPU usage of an app pod in cores.", "gauge")
	for _, k := range resKeys {
		w.sample("mortise_app_cpu_cores", append(appEnvLabels(k.project, k.app, k.env), [2]string{"pod", k.pod}), state.resources[k].cpu)
	}
	w.family("mortise_app_memory_bytes", "Current memory usage of an app pod in bytes.", "gauge")
	for _, k := range resKeys {
		w.sample("mortise_app_memory_bytes", append(appEnvLabels(k.project, k.app, k.env), [2]string{"pod", k.pod}), float64(state.resources[k].memory))
	}
	w.family("mortise_app_pod_restarts_total", "Container restarts of an app pod as reported by the kubelet.", "counter")
	for _, k := range resKeys {
		w.sample("mortise_app_pod_restarts_total", append(appEnvLabels(k.project, k.app, k.env), [2]string{"pod", k.pod}), float64(state.resources[k].restarts))
	}

	w.family("mortise_app_pvc_capacity_bytes", "Capacity of an app PVC's filesystem in bytes.", "gauge")
	for _, k := range pvcKeys {
		w.sample("mortise_app_pvc_capacity_bytes", append(appEnvLabels(k.project, k.app, k.env), [2]string{"pvc", k.pvc}), float64(state.pvcs[k].capacity))
	}
	w.family("mortise_app_pvc_used_bytes", "Used bytes on an app PVC's filesystem.", "gauge")
	for _, k := range pvcKeys {
		w.sample("mortise_app_pvc_used_bytes", append(appEnvLabels(k.project, k.app, k.env), [2]string{"pvc", k.pvc}), float64(state.pvcs[k].used))
	}

	w.family("mortise_app_http_requests_total", "HTTP requests routed to an app since observer start.", "counter")
	for _, k := range trafKeys {
		w.sample("mortise_app_http_requests_total", appEnvLabels(k.project, k.app, k.env), float64(state.traffic[k].requests))
	}
	w.family("mortise_app_http_responses_total", "HTTP responses by status class since observer start.", "counter")
	for _, k := range trafKeys {
		t := state.traffic[k]
		base := appEnvLabels(k.project, k.app, k.env)
		for _, c := range []struct {
			class string
			v     int64
		}{{"2xx", t.status2xx}, {"3xx", t.status3xx}, {"4xx", t.status4xx}, {"5xx", t.status5xx}} {
			w.sample("mortise_app_http_responses_total", append(base, [2]string{"class", c.class}), float64(c.v))
		}
	}
	w.family("mortise_app_http_request_bytes_total", "HTTP request body bytes received since observer start.", "counter")
	for _, k := range trafKeys {
		w.sample("mortise_app_http_request_bytes_total", appEnvLabels(k.project, k.app, k.env), float64(state.traffic[k].bytesIn))
	}
	w.family("mortise_app_http_response_bytes_total", "HTTP response body bytes sent since observer start.", "counter")
	for _, k := range trafKeys {
		w.sample("mortise_app_http_response_bytes_total", appEnvLabels(k.project, k.app, k.env), float64(state.traffic[k].bytesOut))
	}
	w.family("mortise_app_http_latency_seconds", "HTTP latency quantiles over the most recent completed traffic bucket.", "gauge")
	for _, k := range trafKeys {
		t := state.traffic[k]
		base := appEnvLabels(k.project, k.app, k.env)
		for _, q := range []struct {
			q string
			v float64
		}{{"0.5", t.latencyP50}, {"0.95", t.latencyP95}, {"0.99", t.latencyP99}} {
			w.sample("mortise_app_http_latency_seconds", append(base, [2]string{"quantile", q.q}), q.v)
		}
	}
	state.mu.Unlock()

	snap := health.Snapshot()
	sort.Slice(snap.Collectors, func(i, j int) bool {
		return snap.Collectors[i].Collector < snap.Collectors[j].Collector
	})
	w.family("mortise_observer_collector_up", "1 when the collector's most recent cycle succeeded.", "gauge")
	for _, c := range snap.Collectors {
		up := 0.0
		if c.LastTick > 0 && c.LastTick == c.LastSuccess {
			up = 1.0
		}
		w.sample("mortise_observer_collector_up", [][2]string{{"collector", c.Collector}}, up)
	}
	w.family("mortise_observer_collector_last_success_timestamp_seconds", "Unix time of the collector's last successful cycle.", "gauge")
	for _, c := range snap.Collectors {
		w.sample("mortise_observer_collector_last_success_timestamp_seconds", [][2]string{{"collector", c.Collector}}, float64(c.LastSuccess))
	}
	gaugeNames := make([]string, 0, len(snap.Gauges))
	for name := range snap.Gauges {
		gaugeNames = append(gaugeNames, name)
	}
	sort.Strings(gaugeNames)
	w.family("mortise_observer_log_tailers", "Active pod log tailers.", "gauge")
	w.family("mortise_observer_log_tailers_skipped", "Pods not tailed because of the max-log-pods cap.", "gauge")
	w.family("mortise_observer_log_lines_dropped_total", "Log lines dropped because the ingest channel was full, since observer start.", "counter")
	for _, name := range gaugeNames {
		switch name {
		case "logTailers":
			w.sample("mortise_observer_log_tailers", nil, float64(snap.Gauges[name]))
		case "logTailersSkipped":
			w.sample("mortise_observer_log_tailers_skipped", nil, float64(snap.Gauges[name]))
		case "logLinesDropped":
			w.sample("mortise_observer_log_lines_dropped_total", nil, float64(snap.Gauges[name]))
		}
	}

	return w.b.String()
}

func (s *ObserverServer) handlePromMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprint(w, renderMetrics(s.promState, s.health))
}
