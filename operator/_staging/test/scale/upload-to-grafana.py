#!/usr/bin/env python3
"""
Grove Scale Test to Grafana Automation Script

Automates uploading scale test results to Grafana with cumulative data support.

Usage:
    python3 upload-to-grafana.py --username admin --password mypass
"""

import argparse
import json
import glob
import re
import sys
import subprocess
import time
from pathlib import Path
from datetime import datetime

try:
    import requests
except ImportError:
    print("Error: requests library not installed")
    print("Install with: pip3 install requests")
    sys.exit(1)


class GrafanaUploader:
    def __init__(self, grafana_url, username, password):
        self.grafana_url = grafana_url.rstrip('/')
        self.session = requests.Session()
        self.session.auth = (username, password)
        self.datasource_uid = None

    def test_connection(self):
        """Test Grafana connection."""
        try:
            resp = self.session.get(f"{self.grafana_url}/api/health", timeout=5)
            resp.raise_for_status()
            print(f"✓ Connected to Grafana at {self.grafana_url}")
            return True
        except Exception as e:
            print(f"✗ Failed to connect to Grafana: {e}")
            return False

    def ensure_datasource(self, datasource_name, json_url):
        """Add or update JSON datasource."""
        resp = self.session.get(f"{self.grafana_url}/api/datasources/name/{datasource_name}")

        datasource_config = {
            "name": datasource_name,
            "type": "simplejson",
            "url": json_url,
            "access": "proxy",
            "isDefault": False,
            "jsonData": {}
        }

        if resp.status_code == 200:
            existing = resp.json()
            self.datasource_uid = existing['uid']
            resp = self.session.put(
                f"{self.grafana_url}/api/datasources/{existing['id']}",
                json=datasource_config
            )
            print(f"✓ Updated datasource: {datasource_name} (UID: {self.datasource_uid})")
        else:
            resp = self.session.post(
                f"{self.grafana_url}/api/datasources",
                json=datasource_config
            )
            if resp.status_code == 200:
                result = resp.json()
                self.datasource_uid = result.get('datasource', {}).get('uid') or result.get('uid')
                print(f"✓ Created datasource: {datasource_name} (UID: {self.datasource_uid})")
            else:
                print(f"✗ Failed to create datasource: {resp.text}")
                return False
        return True

    def get_dashboard(self, dashboard_title):
        """Find and fetch dashboard by title."""
        resp = self.session.get(f"{self.grafana_url}/api/search?type=dash-db")
        if resp.status_code != 200:
            return None

        dashboards = resp.json()
        for dash in dashboards:
            if dash['title'] == dashboard_title:
                resp = self.session.get(f"{self.grafana_url}/api/dashboards/uid/{dash['uid']}")
                if resp.status_code == 200:
                    return resp.json()
        return None

    def add_scale_test_panels(self, dashboard):
        """Add or update scale test panels in dashboard."""
        panels = dashboard.get('panels', [])

        # Check if scale test panels already exist
        existing_titles = {p.get('title', '') for p in panels}
        scale_test_titles = {
            "Pod Creation Duration (Scale Test)",
            "PodCliqueSet → Child Creation (Scale Test)",
            "ScalingGroup → PodClique Creation (Scale Test)"
        }

        if scale_test_titles.issubset(existing_titles):
            # Update datasource UID in existing panels
            for panel in panels:
                if panel.get('title') in scale_test_titles:
                    for target in panel.get('targets', []):
                        if 'datasource' in target:
                            target['datasource']['uid'] = self.datasource_uid
            print("✓ Updated existing scale test panels")
            return dashboard

        # Add new panels
        max_id = max([p.get('id', 0) for p in panels] + [0])
        max_y = max([p.get('gridPos', {}).get('y', 0) + p.get('gridPos', {}).get('h', 0)
                    for p in panels] + [0])

        new_panels = [
            {
                "id": max_id + 1,
                "title": "Pod Creation Duration (Scale Test)",
                "type": "timeseries",
                "gridPos": {"h": 8, "w": 12, "x": 0, "y": max_y},
                "targets": [{
                    "datasource": {"type": "simplejson", "uid": self.datasource_uid},
                    "target": "grove_pclq_to_pod_creation_duration_seconds_sum",
                    "refId": "A"
                }],
                "fieldConfig": {
                    "defaults": {"unit": "s", "custom": {"drawStyle": "line", "fillOpacity": 10}}
                },
                "options": {"legend": {"displayMode": "list", "placement": "bottom"}}
            },
            {
                "id": max_id + 2,
                "title": "PodCliqueSet → Child Creation (Scale Test)",
                "type": "timeseries",
                "gridPos": {"h": 8, "w": 12, "x": 12, "y": max_y},
                "targets": [{
                    "datasource": {"type": "simplejson", "uid": self.datasource_uid},
                    "target": "grove_pcs_to_child_creation_duration_seconds_sum",
                    "refId": "A"
                }],
                "fieldConfig": {
                    "defaults": {"unit": "s", "custom": {"drawStyle": "line", "fillOpacity": 10}}
                }
            },
            {
                "id": max_id + 3,
                "title": "ScalingGroup → PodClique Creation (Scale Test)",
                "type": "timeseries",
                "gridPos": {"h": 8, "w": 24, "x": 0, "y": max_y + 8},
                "targets": [{
                    "datasource": {"type": "simplejson", "uid": self.datasource_uid},
                    "target": "grove_pcsg_to_pclq_creation_duration_seconds_sum",
                    "refId": "A"
                }],
                "fieldConfig": {
                    "defaults": {"unit": "s", "custom": {"drawStyle": "line", "fillOpacity": 10}}
                }
            }
        ]

        dashboard['panels'].extend(new_panels)
        print(f"✓ Added {len(new_panels)} new panels")
        return dashboard

    def update_dashboard(self, dashboard_title):
        """Update dashboard with scale test panels."""
        dashboard_data = self.get_dashboard(dashboard_title)
        if not dashboard_data:
            print(f"✗ Dashboard '{dashboard_title}' not found")
            return None

        dashboard = dashboard_data['dashboard']
        dashboard = self.add_scale_test_panels(dashboard)

        payload = {"dashboard": dashboard, "overwrite": True}
        resp = self.session.post(f"{self.grafana_url}/api/dashboards/db", json=payload)

        if resp.status_code == 200:
            result = resp.json()
            dashboard_url = f"{self.grafana_url}{result['url']}"
            print(f"✓ Dashboard updated: {result['slug']}")
            return dashboard_url
        else:
            print(f"✗ Failed to update dashboard: {resp.text}")
            return None


def parse_metric_line(line):
    """Parse a Prometheus metric line."""
    match = re.match(r'([^\{]+)\{([^\}]*)\}\s+(\S+)', line)
    if not match:
        return None

    metric_name, labels_str, value = match.groups()
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


def load_prometheus_metrics(results_dir):
    """Load all Prometheus metrics from results directory."""
    metric_files = sorted(glob.glob(f"{results_dir}/metrics-*.prom"))

    if not metric_files:
        print(f"✗ No metrics files found in {results_dir}")
        return []

    timeseries_map = {}

    for filename in metric_files:
        timestamp_match = re.search(r'metrics-(\d+)\.prom', filename)
        if not timestamp_match:
            continue
        timestamp_ms = int(timestamp_match.group(1)) * 1000

        with open(filename, 'r') as f:
            for line in f:
                if not line.startswith('grove_') and not line.startswith('controller_runtime'):
                    continue

                metric = parse_metric_line(line.strip())
                if not metric:
                    continue

                label_str = ','.join(f"{k}={v}" for k, v in sorted(metric['labels'].items()))
                series_key = f"{metric['metric']}{{{label_str}}}"

                if series_key not in timeseries_map:
                    timeseries_map[series_key] = {
                        'target': series_key,
                        'metric': metric['metric'],
                        'labels': metric['labels'],
                        'datapoints': []
                    }

                timeseries_map[series_key]['datapoints'].append([
                    metric['value'],
                    timestamp_ms
                ])

    print(f"✓ Loaded {len(metric_files)} metric snapshots")
    return list(timeseries_map.values())


def merge_timeseries(new_data, existing_file):
    """Merge new timeseries with existing data (cumulative mode)."""
    if not Path(existing_file).exists():
        return new_data

    with open(existing_file, 'r') as f:
        existing_data = json.load(f)

    existing_map = {ts['target']: ts for ts in existing_data}

    for new_series in new_data:
        target = new_series['target']
        if target in existing_map:
            existing_map[target]['datapoints'].extend(new_series['datapoints'])
            existing_map[target]['datapoints'] = list({
                dp[1]: dp for dp in existing_map[target]['datapoints']
            }.values())
            existing_map[target]['datapoints'].sort(key=lambda x: x[1])
        else:
            existing_map[target] = new_series

    merged = list(existing_map.values())
    total_datapoints = sum(len(ts['datapoints']) for ts in merged)
    print(f"✓ Merged data: {len(merged)} timeseries, {total_datapoints} datapoints")
    return merged


def save_json_files(timeseries_data, results_dir):
    """Save timeseries and dashboard JSON files."""
    timeseries_file = f"{results_dir}/metrics-timeseries.json"
    with open(timeseries_file, 'w') as f:
        json.dump(timeseries_data, f, indent=2)
    print(f"✓ Saved {timeseries_file}")

    dashboard_data = {
        'title': 'Grove Scale Test Metrics',
        'timestamp': datetime.now().isoformat(),
        'timeseries_count': len(timeseries_data)
    }
    dashboard_file = f"{results_dir}/metrics-dashboard.json"
    with open(dashboard_file, 'w') as f:
        json.dump(dashboard_data, f, indent=2)


def ensure_json_server(port):
    """Ensure JSON server is running."""
    try:
        resp = requests.get(f"http://localhost:{port}/", timeout=1)
        if resp.status_code == 200:
            print(f"✓ JSON server already running on port {port}")
            return True
    except:
        pass

    script_path = Path(__file__).parent / "serve-json.py"
    if not script_path.exists():
        print(f"✗ serve-json.py not found at {script_path}")
        return False

    print(f"Starting JSON server on port {port}...")
    subprocess.Popen(
        ['python3', str(script_path)],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL
    )

    for _ in range(10):
        time.sleep(0.5)
        try:
            resp = requests.get(f"http://localhost:{port}/", timeout=1)
            if resp.status_code == 200:
                print(f"✓ JSON server started on port {port}")
                return True
        except:
            continue

    print("✗ Failed to start JSON server")
    return False


def main():
    parser = argparse.ArgumentParser(
        description="Upload Grove scale test results to Grafana",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  %(prog)s --username admin --password admin
  %(prog)s --grafana-url http://grafana.example.com --username admin --password secret
  %(prog)s --username admin --password admin --dashboard "My Custom Dashboard"
        """
    )
    parser.add_argument('--grafana-url', default='http://localhost:3000',
                       help='Grafana URL (default: http://localhost:3000)')
    parser.add_argument('--username', required=True,
                       help='Grafana username')
    parser.add_argument('--password', required=True,
                       help='Grafana password')
    parser.add_argument('--dashboard', default='Grove Controller Monitoring',
                       help='Dashboard title (default: Grove Controller Monitoring)')
    parser.add_argument('--datasource', default='Grove Scale Test JSON',
                       help='Datasource name (default: Grove Scale Test JSON)')
    parser.add_argument('--results-dir', default='results',
                       help='Results directory (default: results)')
    parser.add_argument('--json-port', type=int, default=8000,
                       help='JSON server port (default: 8000)')
    parser.add_argument('--no-merge', action='store_true',
                       help='Do not merge with existing data')
    parser.add_argument('--open-browser', action='store_true',
                       help='Open dashboard in browser')

    args = parser.parse_args()

    print("=" * 70)
    print("Grove Scale Test → Grafana Uploader")
    print("=" * 70)
    print()

    # Load metrics
    print("Loading metrics...")
    new_metrics = load_prometheus_metrics(args.results_dir)
    if not new_metrics:
        sys.exit(1)

    # Merge with existing data
    timeseries_file = f"{args.results_dir}/metrics-timeseries.json"
    if args.no_merge:
        final_metrics = new_metrics
        print("✓ Using new metrics only (no merge)")
    else:
        final_metrics = merge_timeseries(new_metrics, timeseries_file)

    # Save JSON files
    save_json_files(final_metrics, args.results_dir)

    # Ensure JSON server is running
    print()
    if not ensure_json_server(args.json_port):
        sys.exit(1)

    json_url = f"http://host.docker.internal:{args.json_port}"

    # Upload to Grafana
    print()
    print("Uploading to Grafana...")
    uploader = GrafanaUploader(args.grafana_url, args.username, args.password)

    if not uploader.test_connection():
        sys.exit(1)

    if not uploader.ensure_datasource(args.datasource, json_url):
        sys.exit(1)

    dashboard_url = uploader.update_dashboard(args.dashboard)
    if not dashboard_url:
        sys.exit(1)

    # Summary
    print()
    print("=" * 70)
    print("✅ Success!")
    print("=" * 70)
    total_datapoints = sum(len(ts['datapoints']) for ts in final_metrics)
    print(f"Dashboard:       {dashboard_url}")
    print(f"Credentials:     {args.username} / {'*' * len(args.password)}")
    print(f"Timeseries:      {len(final_metrics)}")
    print(f"Total datapoints: {total_datapoints}")
    print()

    if args.open_browser:
        import webbrowser
        webbrowser.open(dashboard_url)
        print("✓ Opened in browser")


if __name__ == '__main__':
    main()
