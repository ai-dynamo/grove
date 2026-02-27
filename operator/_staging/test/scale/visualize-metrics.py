#!/usr/bin/env python3
"""
Simple metrics visualization script for Grove scale test results.
Creates charts from collected Prometheus metrics.

Usage: python3 visualize-metrics.py
"""

import glob
import re
from collections import defaultdict
import sys

try:
    import matplotlib.pyplot as plt
    import matplotlib.dates as mdates
    from datetime import datetime
except ImportError:
    print("Error: matplotlib not installed")
    print("Install with: pip3 install matplotlib")
    sys.exit(1)


def parse_metric_file(filename):
    """Parse a Prometheus metrics file and extract Grove metrics."""
    metrics = defaultdict(list)
    timestamp = int(re.search(r'metrics-(\d+)\.prom', filename).group(1))

    with open(filename, 'r') as f:
        for line in f:
            if line.startswith('grove_'):
                # Extract metric name and value
                match = re.match(r'([^\{]+)\{([^\}]*)\}\s+(\S+)', line)
                if match:
                    metric_name, labels, value = match.groups()

                    # Parse labels
                    label_dict = {}
                    for label in labels.split(','):
                        if '=' in label:
                            k, v = label.split('=', 1)
                            label_dict[k.strip()] = v.strip('"')

                    metrics[metric_name].append({
                        'timestamp': timestamp,
                        'labels': label_dict,
                        'value': float(value)
                    })

    return metrics


def plot_pod_creation_latencies(all_metrics):
    """Plot PodClique to Pod creation duration over time."""
    metric_name = 'grove_pclq_to_pod_creation_duration_seconds_sum'

    if metric_name not in all_metrics:
        print(f"No data for {metric_name}")
        return

    # Group by PodClique name
    cliques = defaultdict(lambda: {'timestamps': [], 'values': []})

    for data in all_metrics[metric_name]:
        pclq_name = data['labels'].get('pclq_name', 'unknown')
        cliques[pclq_name]['timestamps'].append(
            datetime.fromtimestamp(data['timestamp'])
        )
        cliques[pclq_name]['values'].append(data['value'])

    # Plot top 10 PodCliques
    plt.figure(figsize=(14, 8))
    for i, (pclq_name, data) in enumerate(list(cliques.items())[:10]):
        plt.plot(data['timestamps'], data['values'],
                marker='o', label=pclq_name, linewidth=2)

    plt.xlabel('Time', fontsize=12)
    plt.ylabel('Creation Duration (seconds)', fontsize=12)
    plt.title('Pod Creation Latency by PodClique', fontsize=14, fontweight='bold')
    plt.legend(bbox_to_anchor=(1.05, 1), loc='upper left', fontsize=8)
    plt.grid(True, alpha=0.3)
    plt.tight_layout()
    plt.savefig('results/pod_creation_latency.png', dpi=150)
    print("Created: results/pod_creation_latency.png")


def plot_reconciliation_counts(all_metrics):
    """Plot controller reconciliation counts."""
    metric_name = 'controller_runtime_reconcile_total'

    if metric_name not in all_metrics:
        print(f"No data for {metric_name}")
        return

    # Group by controller
    controllers = defaultdict(lambda: {'timestamps': [], 'success': [], 'error': []})

    for data in all_metrics[metric_name]:
        controller = data['labels'].get('controller', 'unknown')
        result = data['labels'].get('result', 'unknown')

        if 'podclique' in controller or 'podgang' in controller:
            ts = datetime.fromtimestamp(data['timestamp'])

            if result == 'success':
                controllers[controller]['timestamps'].append(ts)
                controllers[controller]['success'].append(data['value'])
            elif result == 'error':
                if ts not in controllers[controller]['timestamps']:
                    controllers[controller]['timestamps'].append(ts)
                controllers[controller]['error'].append(data['value'])

    # Plot
    fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(14, 10))

    # Success reconciliations
    for controller, data in controllers.items():
        if data['success']:
            ax1.plot(data['timestamps'], data['success'],
                    marker='o', label=controller, linewidth=2)

    ax1.set_xlabel('Time', fontsize=12)
    ax1.set_ylabel('Successful Reconciliations', fontsize=12)
    ax1.set_title('Controller Reconciliation Success', fontsize=14, fontweight='bold')
    ax1.legend(fontsize=9)
    ax1.grid(True, alpha=0.3)

    # Timeline summary
    metric_counts = defaultdict(int)
    for controller_data in all_metrics.values():
        for data in controller_data:
            metric_counts[datetime.fromtimestamp(data['timestamp'])] += 1

    if metric_counts:
        timestamps = sorted(metric_counts.keys())
        counts = [metric_counts[ts] for ts in timestamps]
        ax2.bar(timestamps, counts, width=0.001, alpha=0.7)
        ax2.set_xlabel('Time', fontsize=12)
        ax2.set_ylabel('Total Metric Updates', fontsize=12)
        ax2.set_title('Metric Update Activity', fontsize=14, fontweight='bold')
        ax2.grid(True, alpha=0.3)

    plt.tight_layout()
    plt.savefig('results/reconciliation_counts.png', dpi=150)
    print("Created: results/reconciliation_counts.png")


def main():
    print("Grove Scale Test Metrics Visualization")
    print("=" * 50)

    # Find all metrics files
    metric_files = sorted(glob.glob('results/metrics-*.prom'))

    if not metric_files:
        print("Error: No metrics files found in results/")
        return

    print(f"Found {len(metric_files)} metric snapshots")

    # Parse all metrics
    all_metrics = defaultdict(list)
    for filename in metric_files:
        metrics = parse_metric_file(filename)
        for metric_name, data in metrics.items():
            all_metrics[metric_name].extend(data)

    print(f"Parsed {len(all_metrics)} unique metric types")
    print()

    # Generate visualizations
    print("Generating visualizations...")
    plot_pod_creation_latencies(all_metrics)
    plot_reconciliation_counts(all_metrics)

    print()
    print("=" * 50)
    print("Visualization complete!")
    print("View the generated PNG files in the results/ directory")


if __name__ == '__main__':
    main()
