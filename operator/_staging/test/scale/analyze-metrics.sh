#!/usr/bin/env bash

# Quick metrics analysis script for Grove scale test results

set -e

METRICS_FILE="${1:-results/metrics-*.prom}"

echo "==================================="
echo "Grove Scale Test Metrics Analysis"
echo "==================================="
echo ""

# Get the latest metrics file if wildcard used
if [[ "$METRICS_FILE" == *"*"* ]]; then
    METRICS_FILE=$(ls -t results/metrics-*.prom | head -1)
    echo "Using latest metrics: $METRICS_FILE"
    echo ""
fi

# Pod Creation Latencies
echo "=== Pod Creation Latencies (PodClique → Pod) ==="
echo "Showing sum and count for each PodClique:"
grep "grove_pclq_to_pod_creation_duration_seconds_sum" "$METRICS_FILE" | grep -v "^#" | head -10
echo ""

# PodCliqueSet to Child creation
echo "=== PodCliqueSet → Child Creation Times ==="
grep "grove_pcs_to_child_creation_duration_seconds" "$METRICS_FILE" | grep -v "^#" | grep "_sum"
echo ""

# PodCliqueScalingGroup metrics
echo "=== PodCliqueScalingGroup → PodClique Creation ==="
grep "grove_pcsg_to_pclq_creation_duration_seconds_sum" "$METRICS_FILE" | grep -v "^#" | head -10
echo ""

# Controller metrics
echo "=== Controller Reconciliation Totals ==="
grep "controller_runtime_reconcile_total" "$METRICS_FILE" | grep -v "^#" | grep -E "podclique|podgang" | head -10
echo ""

# Summary
echo "==================================="
echo "Metrics Summary:"
echo "  Total Grove metrics: $(grep -c "^grove_" "$METRICS_FILE")"
echo "  Snapshots available: $(ls results/metrics-*.prom | wc -l | tr -d ' ')"
echo ""
echo "Quick Commands:"
echo "  View all Grove metrics: grep '^grove_' $METRICS_FILE | less"
echo "  Count by type: grep '^grove_' $METRICS_FILE | cut -d'{' -f1 | sort | uniq -c"
echo "  Search specific: grep 'pclq_to_pod' $METRICS_FILE"
