import json

# Read current dashboard
with open('/tmp/current-dashboard.json', 'r') as f:
    data = json.load(f)

dashboard = data['dashboard']

# Find the highest panel ID and Y position
max_id = max([p['id'] for p in dashboard['panels']] + [0])
max_y = max([p['gridPos']['y'] + p['gridPos']['h'] for p in dashboard['panels']] + [0])

# Add new panels for scale test metrics
new_panels = [
    {
        "id": max_id + 1,
        "title": "Pod Creation Duration (Scale Test)",
        "type": "timeseries",
        "gridPos": {"h": 8, "w": 12, "x": 0, "y": max_y},
        "targets": [{
            "datasource": {"type": "simplejson", "uid": "bfdixexkc7qwwa"},
            "target": "grove_pclq_to_pod_creation_duration_seconds_sum",
            "refId": "A"
        }],
        "fieldConfig": {
            "defaults": {
                "unit": "s",
                "custom": {"drawStyle": "line"}
            }
        }
    },
    {
        "id": max_id + 2,
        "title": "PodCliqueSet to Child Creation (Scale Test)",
        "type": "timeseries",
        "gridPos": {"h": 8, "w": 12, "x": 12, "y": max_y},
        "targets": [{
            "datasource": {"type": "simplejson", "uid": "bfdixexkc7qwwa"},
            "target": "grove_pcs_to_child_creation_duration_seconds_sum",
            "refId": "A"
        }],
        "fieldConfig": {
            "defaults": {
                "unit": "s",
                "custom": {"drawStyle": "line"}
            }
        }
    },
    {
        "id": max_id + 3,
        "title": "ScalingGroup to PodClique Creation (Scale Test)",
        "type": "timeseries",
        "gridPos": {"h": 8, "w": 24, "x": 0, "y": max_y + 8},
        "targets": [{
            "datasource": {"type": "simplejson", "uid": "bfdixexkc7qwwa"},
            "target": "grove_pcsg_to_pclq_creation_duration_seconds_sum",
            "refId": "A"
        }],
        "fieldConfig": {
            "defaults": {
                "unit": "s",
                "custom": {"drawStyle": "line"}
            }
        }
    }
]

dashboard['panels'].extend(new_panels)

# Save updated dashboard
output = {
    "dashboard": dashboard,
    "overwrite": True
}

with open('/tmp/updated-dashboard.json', 'w') as f:
    json.dump(output, f, indent=2)

print(f"Added {len(new_panels)} panels to dashboard")
