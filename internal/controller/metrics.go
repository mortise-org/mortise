/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
)

// Operator-side Prometheus metrics (SPEC §5.11b). These cover control-plane
// facts the observer cannot see without new RBAC — build outcomes and App
// phases — and deliberately do NOT duplicate the observer's resource/traffic
// series. Registered on controller-runtime's registry, so they ship on the
// operator's existing /metrics endpoint.

var (
	buildsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mortise_builds_total",
		Help: "Completed builds by result.",
	}, []string{"project", "app", "result"})

	buildDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "mortise_build_duration_seconds",
		Help: "Wall-clock duration of completed builds.",
		// Builds span seconds (cache hit) to tens of minutes (cold).
		Buckets: []float64{5, 15, 30, 60, 120, 300, 600, 1200, 1800},
	}, []string{"project", "app", "result"})
)

func init() {
	metrics.Registry.MustRegister(buildsTotal, buildDurationSeconds)
}

// recordBuildOutcome instruments a BuildRun's terminal transition. Duration
// is measured StartedAt→now; runs without StartedAt (failed before starting)
// count in the total but not the histogram.
func recordBuildOutcome(br *mortisev1alpha1.BuildRun, result string, finished time.Time) {
	project, ok := constants.ProjectFromControlNs(br.Namespace)
	if !ok {
		return
	}
	app := br.Spec.AppName
	buildsTotal.WithLabelValues(project, app, result).Inc()
	if br.Status.StartedAt != nil {
		buildDurationSeconds.WithLabelValues(project, app, result).
			Observe(finished.Sub(br.Status.StartedAt.Time).Seconds())
	}
}

// appPhaseCollector exports one series per App at scrape time:
// mortise_app_status_phase{project, app, phase} = 1 (kube-state-metrics
// style). Reads the manager's cache, so a scrape costs no apiserver call.
type appPhaseCollector struct {
	reader client.Reader
	desc   *prometheus.Desc
}

// NewAppPhaseCollector registers the App phase gauge backed by the given
// cache-backed reader. Call once from main after the manager is built.
func NewAppPhaseCollector(reader client.Reader) {
	metrics.Registry.MustRegister(&appPhaseCollector{
		reader: reader,
		desc: prometheus.NewDesc(
			"mortise_app_status_phase",
			"Current phase of an App (one series per app, value always 1).",
			[]string{"project", "app", "phase"}, nil,
		),
	})
}

func (c *appPhaseCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *appPhaseCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var apps mortisev1alpha1.AppList
	if err := c.reader.List(ctx, &apps); err != nil {
		return
	}
	for i := range apps.Items {
		app := &apps.Items[i]
		project, ok := constants.ProjectFromControlNs(app.Namespace)
		if !ok {
			continue
		}
		phase := string(app.Status.Phase)
		if phase == "" {
			phase = "Pending"
		}
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, 1, project, app.Name, phase)
	}
}
