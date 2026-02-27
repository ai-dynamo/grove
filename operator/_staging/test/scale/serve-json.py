#!/usr/bin/env python3
"""
Simple HTTP server to serve metrics JSON for Grafana JSON datasource.
Compatible with Grafana's SimpleJson datasource plugin.
"""

from http.server import HTTPServer, BaseHTTPRequestHandler
import json
import os

class MetricsHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        """Handle GET requests."""
        if self.path == '/':
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Access-Control-Allow-Origin', '*')
            self.end_headers()
            self.wfile.write(json.dumps({'status': 'ok'}).encode())

        elif self.path == '/search' or self.path.startswith('/search?'):
            # Return list of available metrics
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Access-Control-Allow-Origin', '*')
            self.end_headers()

            metrics = [
                'grove_pclq_to_pod_creation_duration_seconds_sum',
                'grove_pcs_to_child_creation_duration_seconds_sum',
                'grove_pcsg_to_pclq_creation_duration_seconds_sum',
                'controller_runtime_reconcile_total'
            ]
            self.wfile.write(json.dumps(metrics).encode())

        elif self.path == '/query' or self.path.startswith('/query?'):
            # Return timeseries data
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Access-Control-Allow-Origin', '*')
            self.end_headers()

            # Load the timeseries data
            try:
                with open('results/metrics-timeseries.json', 'r') as f:
                    data = json.load(f)
                self.wfile.write(json.dumps(data).encode())
            except Exception as e:
                self.wfile.write(json.dumps({'error': str(e)}).encode())

        elif self.path == '/metrics':
            # Serve the full metrics file
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Access-Control-Allow-Origin', '*')
            self.end_headers()

            try:
                with open('results/metrics-dashboard.json', 'r') as f:
                    data = json.load(f)
                self.wfile.write(json.dumps(data).encode())
            except Exception as e:
                self.wfile.write(json.dumps({'error': str(e)}).encode())

        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        """Handle POST requests (required by Grafana)."""
        content_length = int(self.headers.get('Content-Length', 0))
        body = self.rfile.read(content_length)

        if self.path == '/query':
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Access-Control-Allow-Origin', '*')
            self.end_headers()

            try:
                with open('results/metrics-timeseries.json', 'r') as f:
                    data = json.load(f)
                self.wfile.write(json.dumps(data).encode())
            except Exception as e:
                self.wfile.write(json.dumps({'error': str(e)}).encode())
        else:
            self.send_response(404)
            self.end_headers()

    def do_OPTIONS(self):
        """Handle OPTIONS for CORS."""
        self.send_response(200)
        self.send_header('Access-Control-Allow-Origin', '*')
        self.send_header('Access-Control-Allow-Methods', 'GET, POST, OPTIONS')
        self.send_header('Access-Control-Allow-Headers', 'Content-Type')
        self.end_headers()

    def log_message(self, format, *args):
        """Custom log format."""
        print(f"[{self.log_date_time_string()}] {format % args}")

def main():
    port = 8000
    server_address = ('', port)

    print("=" * 60)
    print("Grove Metrics JSON Server for Grafana")
    print("=" * 60)
    print(f"\nServer running on: http://localhost:{port}")
    print("\nAvailable endpoints:")
    print(f"  http://localhost:{port}/              - Health check")
    print(f"  http://localhost:{port}/metrics        - Dashboard JSON")
    print(f"  http://localhost:{port}/search         - Available metrics")
    print(f"  http://localhost:{port}/query          - Timeseries data")
    print("\nGrafana JSON Datasource URL:")
    print(f"  http://localhost:{port}")
    print("\nPress Ctrl+C to stop")
    print("=" * 60)
    print()

    httpd = HTTPServer(server_address, MetricsHandler)
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\n\nShutting down server...")
        httpd.shutdown()

if __name__ == '__main__':
    main()
