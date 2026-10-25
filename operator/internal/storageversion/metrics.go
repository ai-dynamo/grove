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

package storageversion

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	migrationLabels = []string{"crd", "version"}

	objectsRewrittenTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "grove_storage_migration_objects_rewritten_total",
		Help: "Number of objects rewritten at the storage version of their CRD.",
	}, migrationLabels)
	objectsRejectedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "grove_storage_migration_objects_rejected_total",
		Help: "Number of objects that were rejected when rewritten at the storage version of their CRD.",
	}, migrationLabels)
	migrated = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "grove_storage_migration_complete",
		Help: "1 when every object of the CRD is stored at the storage version and status.storedVersions lists only it, 0 otherwise.",
	}, migrationLabels)
)

func init() {
	ctrlmetrics.Registry.MustRegister(objectsRewrittenTotal, objectsRejectedTotal, migrated)
}

func setMigrated(crd, version string, done bool) {
	value := 0.0
	if done {
		value = 1
	}
	migrated.WithLabelValues(crd, version).Set(value)
}
