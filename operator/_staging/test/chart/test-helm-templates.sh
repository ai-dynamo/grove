#!/usr/bin/env bash
# Helm template tests for Grove operator chart.
# Validates pprof port, service labels, and deployment ports render correctly.
#
# Usage: ./test-helm-templates.sh
# Prerequisites: helm binary in PATH

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="${SCRIPT_DIR}/../../charts"

PASS=0
FAIL=0

pass() {
  PASS=$((PASS + 1))
  echo "  PASS: $1"
}

fail() {
  FAIL=$((FAIL + 1))
  echo "  FAIL: $1"
}

assert_contains() {
  local output="$1" pattern="$2" msg="$3"
  if echo "$output" | grep -q "$pattern"; then
    pass "$msg"
  else
    fail "$msg (expected to find '$pattern')"
  fi
}

assert_not_contains() {
  local output="$1" pattern="$2" msg="$3"
  if echo "$output" | grep -q "$pattern"; then
    fail "$msg (unexpected '$pattern' found)"
  else
    pass "$msg"
  fi
}

# Extract a single YAML document by Kind from helm template output.
extract_kind() {
  local output="$1" kind="$2"
  echo "$output" | awk -v kind="$kind" '
    /^---/ { doc=""; in_doc=1; next }
    in_doc { doc = doc "\n" $0 }
    END { }
    { if ($0 ~ "kind: " kind "$") { found=doc } }
    END { print found }
  '
}

# Extract YAML document by exact kind match (avoids Service matching ServiceAccount).
get_document() {
  local output="$1" kind="$2"
  echo "$output" | awk -v pat="kind: ${kind}" '
    BEGIN { RS="---" }
    {
      n=split($0, lines, "\n")
      for (i=1; i<=n; i++) {
        gsub(/[ \t]+$/, "", lines[i])
        if (lines[i] == pat) { print $0; next }
      }
    }
  '
}

if ! command -v helm &>/dev/null; then
  echo "SKIP: helm binary not found"
  exit 0
fi

echo "Running Helm template tests..."
echo "Chart directory: ${CHART_DIR}"
echo ""

# --- Test 1: Service uses correct labels ---
echo "Test: Service uses correct labels"
OUTPUT=$(helm template grove-operator "$CHART_DIR" 2>&1)
SVC=$(get_document "$OUTPUT" "Service")
assert_contains "$SVC" "operator-service" "Service has operator-service label"
assert_not_contains "$SVC" "operator-serviceaccount" "Service does not have operator-serviceaccount label"

# --- Test 2: Pprof port in Service when profiling enabled ---
echo "Test: Pprof port in Service when profiling enabled"
OUTPUT=$(helm template grove-operator "$CHART_DIR" --set config.debugging.enableProfiling=true 2>&1)
SVC=$(get_document "$OUTPUT" "Service")
assert_contains "$SVC" "name: pprof" "Service has pprof port"
assert_contains "$SVC" "port: 2753" "Service pprof port is 2753"

# --- Test 3: No pprof port in Service when profiling disabled ---
echo "Test: No pprof port in Service when profiling disabled"
OUTPUT=$(helm template grove-operator "$CHART_DIR" 2>&1)
SVC=$(get_document "$OUTPUT" "Service")
assert_not_contains "$SVC" "name: pprof" "Service has no pprof port when disabled"

# --- Test 4: Pprof port in Deployment when profiling enabled ---
echo "Test: Pprof port in Deployment when profiling enabled"
OUTPUT=$(helm template grove-operator "$CHART_DIR" --set config.debugging.enableProfiling=true 2>&1)
DEPLOY=$(get_document "$OUTPUT" "Deployment")
assert_contains "$DEPLOY" "name: pprof" "Deployment has pprof containerPort"

# --- Test 5: No pprof port in Deployment when profiling disabled ---
echo "Test: No pprof port in Deployment when profiling disabled"
OUTPUT=$(helm template grove-operator "$CHART_DIR" 2>&1)
DEPLOY=$(get_document "$OUTPUT" "Deployment")
assert_not_contains "$DEPLOY" "name: pprof" "Deployment has no pprof port when disabled"

# --- Test 6: Custom pprof port ---
echo "Test: Custom pprof port (6060)"
OUTPUT=$(helm template grove-operator "$CHART_DIR" \
  --set config.debugging.enableProfiling=true \
  --set config.debugging.pprofPort=6060 2>&1)
SVC=$(get_document "$OUTPUT" "Service")
assert_contains "$SVC" "port: 6060" "Service uses custom port 6060"
DEPLOY=$(get_document "$OUTPUT" "Deployment")
assert_contains "$DEPLOY" "containerPort: 6060" "Deployment uses custom containerPort 6060"

# --- Test 7: Pprof port in config YAML ---
echo "Test: Pprof port in ConfigMap config.yaml"
OUTPUT=$(helm template grove-operator "$CHART_DIR" \
  --set config.debugging.enableProfiling=true \
  --set config.debugging.pprofPort=6060 2>&1)
CM=$(echo "$OUTPUT" | awk 'BEGIN{RS="---";FS="\n"} /kind: ConfigMap/ && /config.yaml/ {print $0}')
assert_contains "$CM" "pprofPort: 6060" "ConfigMap contains pprofPort: 6060"

# --- Test 7b: Pyroscope annotations present when profiling enabled ---
echo "Test: Pyroscope annotations in Deployment pod template when profiling enabled"
OUTPUT=$(helm template grove-operator "$CHART_DIR" --set config.debugging.enableProfiling=true 2>&1)
DEPLOY=$(get_document "$OUTPUT" "Deployment")
assert_contains "$DEPLOY" "profiles.grafana.com/cpu.scrape" "Deployment has cpu.scrape annotation"
assert_contains "$DEPLOY" "profiles.grafana.com/cpu.port" "Deployment has cpu.port annotation"
assert_contains "$DEPLOY" "profiles.grafana.com/memory.scrape" "Deployment has memory.scrape annotation"
assert_contains "$DEPLOY" "profiles.grafana.com/memory.port" "Deployment has memory.port annotation"
assert_contains "$DEPLOY" "profiles.grafana.com/goroutine.scrape" "Deployment has goroutine.scrape annotation"
assert_contains "$DEPLOY" "profiles.grafana.com/goroutine.port" "Deployment has goroutine.port annotation"
assert_contains "$DEPLOY" "2753" "Deployment annotations use default pprof port 2753"

# --- Test 7c: Pyroscope annotations absent when profiling disabled ---
echo "Test: No Pyroscope annotations in Deployment when profiling disabled"
OUTPUT=$(helm template grove-operator "$CHART_DIR" 2>&1)
DEPLOY=$(get_document "$OUTPUT" "Deployment")
assert_not_contains "$DEPLOY" "profiles.grafana.com" "Deployment has no Pyroscope annotations when profiling disabled"

# --- Test 7d: Custom pprof port used in Pyroscope annotations ---
echo "Test: Pyroscope annotations use custom pprof port"
OUTPUT=$(helm template grove-operator "$CHART_DIR" \
  --set config.debugging.enableProfiling=true \
  --set config.debugging.pprofPort=6060 2>&1)
DEPLOY=$(get_document "$OUTPUT" "Deployment")
assert_contains "$DEPLOY" "cpu.port: \"6060\"" "Deployment cpu.port annotation uses custom port 6060"

# --- Test 8: Deployment always has metrics and webhooks ports ---
echo "Test: Deployment always has metrics and webhooks ports"
OUTPUT=$(helm template grove-operator "$CHART_DIR" 2>&1)
DEPLOY=$(get_document "$OUTPUT" "Deployment")
assert_contains "$DEPLOY" "name: metrics" "Deployment has metrics port"
assert_contains "$DEPLOY" "name: webhooks" "Deployment has webhooks port"

# --- Summary ---
echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
