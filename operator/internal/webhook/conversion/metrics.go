// Copyright 2026 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package conversion

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/runtime"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	ctrlconversion "sigs.k8s.io/controller-runtime/pkg/webhook/conversion"
)

var (
	conversionLabels = []string{"kind", "direction"}

	conversionRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "grove_conversion_requests_total",
		Help: "Number of objects converted by the CRD conversion webhook.",
	}, conversionLabels)
	conversionFailuresTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "grove_conversion_failures_total",
		Help: "Number of objects the CRD conversion webhook failed to convert.",
	}, conversionLabels)
	conversionDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "grove_conversion_duration_seconds",
		Help:    "Time taken to convert one object in the CRD conversion webhook.",
		Buckets: prometheus.ExponentialBuckets(0.0001, 4, 8),
	}, conversionLabels)
)

func init() {
	ctrlmetrics.Registry.MustRegister(conversionRequestsTotal, conversionFailuresTotal, conversionDurationSeconds)
}

// instrumentedConverter records the conversion metrics of one kind around the converter it wraps.
type instrumentedConverter struct {
	kind string
	next ctrlconversion.Converter
}

func newInstrumentedConverter(kind string, next ctrlconversion.Converter) ctrlconversion.Converter {
	return &instrumentedConverter{kind: kind, next: next}
}

// ConvertObject converts src into dst and records the request, its duration and any failure, labelled
// with the kind and the direction "<src version>_to_<dst version>".
func (c *instrumentedConverter) ConvertObject(ctx context.Context, src, dst runtime.Object) error {
	direction := src.GetObjectKind().GroupVersionKind().Version + "_to_" + dst.GetObjectKind().GroupVersionKind().Version
	start := time.Now()
	err := c.next.ConvertObject(ctx, src, dst)
	conversionDurationSeconds.WithLabelValues(c.kind, direction).Observe(time.Since(start).Seconds())
	conversionRequestsTotal.WithLabelValues(c.kind, direction).Inc()
	if err != nil {
		conversionFailuresTotal.WithLabelValues(c.kind, direction).Inc()
	}
	return err
}
