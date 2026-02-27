#!/usr/bin/env python3
"""
Convert Prometheus metrics to JSON format for Grafana JSON datasource.
"""

import glob
import re
import json
from collections import defaultdict
from datetime import datetime

def parse_metric_line(line):
    """Parse a Prometheus metric line."""
    # Match: metric_name{labels} value
    match = re.match(r'([^\{]+)\{([^\}]*)\}\s+(\S+)', line)
    if not match:
        return None

    metric_name, labels_str, value = match.groups()

    # Parse labels
    labels = {}
    for label in labels_str.split(','):
        if '=' in label:
            k, v = label.split('=', 1)
            labels[k.strip()] = v.strip('"')

    return {
        'metric': metric_name,
        'labels': labels,
        'value': float(value) if value != 'NaN' else 0
    }

def convert_metrics_to_timeseries():
    """Convert all metric files to time series JSON."""
    metric_files = sorted(glob.glob('results/metrics-*.prom'))

    # Group by metric name and label combination
    timeseries = defaultdict(lambda: {'datapoints': []})

    for filename in metric_files:
        # Extract timestamp from filename
        timestamp_match = re.search(r'metrics-(\d+)\.prom', filename)
        if not timestamp_match:
            continue
        timestamp_ms = int(timestamp_match.group(1)) * 1000  # Convert to milliseconds

        with open(filename, 'r') as f:
            for line in f:
                if not line.startswith('grove_'):
                    continue

                metric = parse_metric_line(line.strip())
                if not metric:
                    continue

                # Create a unique key for this time series
                label_str = ','.join(f"{k}={v}" for k, v in sorted(metric['labels'].items()))
                series_key = f"{metric['metric']}{{{label_str}}}"

                # Store metric info
                if 'metric' not in timeseries[series_key]:
                    timeseries[series_key]['metric'] = metric['metric']
                    timeseries[series_key]['labels'] = metric['labels']

                # Add datapoint [value, timestamp_ms]
                timeseries[series_key]['datapoints'].append([
                    metric['value'],
                    timestamp_ms
                ])

    # Convert to list and sort datapoints by timestamp
    result = []
    for series_key, data in timeseries.items():
        data['datapoints'].sort(key=lambda x: x[1])
        data['target'] = series_key
        result.append(data)

    return result

def create_dashboard_json(timeseries_data):
    """Create a simplified JSON structure for visualization."""
    dashboard = {
        'title': 'Grove Scale Test Metrics',
        'timestamp': datetime.now().isoformat(),
        'metrics': {}
    }

    # Group metrics by type
    for series in timeseries_data:
        metric_name = series['metric']

        if metric_name not in dashboard['metrics']:
            dashboard['metrics'][metric_name] = []

        dashboard['metrics'][metric_name].append({
            'labels': series['labels'],
            'datapoints': series['datapoints']
        })

    return dashboard

def main():
    print("Converting Prometheus metrics to JSON...")

    # Convert to time series format
    timeseries = convert_metrics_to_timeseries()
    print(f"Converted {len(timeseries)} time series")

    # Save Grafana-compatible format
    with open('results/metrics-timeseries.json', 'w') as f:
        json.dump(timeseries, f, indent=2)
    print("Created: results/metrics-timeseries.json")

    # Save simplified dashboard format
    dashboard = create_dashboard_json(timeseries)
    with open('results/metrics-dashboard.json', 'w') as f:
        json.dump(dashboard, f, indent=2)
    print("Created: results/metrics-dashboard.json")

    # Print summary
    print("\nMetric Summary:")
    metric_counts = defaultdict(int)
    for series in timeseries:
        metric_counts[series['metric']] += 1

    for metric, count in sorted(metric_counts.items()):
        print(f"  {metric}: {count} series")

    print("\nJSON files ready for Grafana JSON datasource!")

if __name__ == '__main__':
    main()
