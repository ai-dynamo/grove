# Grafana JSON Datasource Setup

## Quick Start

### 1. Start the JSON Server
```bash
cd /Users/rkahn/IdeaProjects/grove/operator/test/scale
python3 serve-json.py
```

The server will run on `http://localhost:8000`

### 2. Install Grafana JSON Datasource Plugin

In Grafana:
1. Go to **Configuration** → **Plugins**
2. Search for "JSON API" or "SimpleJson"
3. Install the plugin
4. Restart Grafana if needed

### 3. Add JSON Datasource

1. Go to **Configuration** → **Data Sources**
2. Click **Add data source**
3. Search for "JSON API" or "SimpleJson"
4. Configure:
   - **Name**: Grove Scale Test
   - **URL**: `http://localhost:8000`
   - Click **Save & Test**

### 4. Create Dashboard

#### Option A: Import Sample Dashboard

```json
{
  "title": "Grove Scale Test Metrics",
  "panels": [
    {
      "title": "Pod Creation Duration",
      "targets": [
        {
          "target": "grove_pclq_to_pod_creation_duration_seconds_sum"
        }
      ]
    }
  ]
}
```

#### Option B: Manual Panel Creation

1. Create new dashboard
2. Add panel
3. Select **Grove Scale Test** datasource
4. Query: `grove_pclq_to_pod_creation_duration_seconds_sum`
5. Visualization: Time series / Graph

## Available Metrics

- `grove_pclq_to_pod_creation_duration_seconds_sum` - Pod creation latency
- `grove_pcs_to_child_creation_duration_seconds_sum` - PodCliqueSet child creation
- `grove_pcsg_to_pclq_creation_duration_seconds_sum` - ScalingGroup to PodClique creation

## Direct JSON Access

View the JSON data directly:
- Full metrics: http://localhost:8000/metrics
- Timeseries: http://localhost:8000/query
- Available metrics: http://localhost:8000/search

## Troubleshooting

### Server not accessible from Grafana
- Ensure server is running: `python3 serve-json.py`
- Check firewall settings
- Use `0.0.0.0` instead of `localhost` if running in container

### No data showing in Grafana
- Verify JSON files exist: `ls results/*.json`
- Check server logs for errors
- Test endpoint: `curl http://localhost:8000/metrics`

### CORS errors
- The server includes CORS headers by default
- If issues persist, check browser console

## Alternative: File-based Approach

If you can't run a server, you can:

1. Use Grafana's **Infinity** datasource
2. Point it to the JSON files directly
3. Configure as "JSON" type
4. Use file path: `results/metrics-dashboard.json`
