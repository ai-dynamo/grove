#!/bin/bash
# run-autoMNNVL-e2e.sh - Run autoMNNVL e2e tests with a single configuration
#
# This script sets up the cluster (via setup-autoMNNVL-cluster.sh) and runs the e2e tests.
# All setup options are passed through to setup-autoMNNVL-cluster.sh.
#
# Usage: ./hack/e2e-autoMNNVL/run-autoMNNVL-e2e.sh [options]
#
# Options:
#   --skip-setup             Only run tests, skip cluster setup
#   --skip-cluster-create    Reuse existing cluster (passed to setup script)
#   --with-fake-gpu          Install fake GPU operator (default, passed to setup script)
#   --without-fake-gpu       Skip fake GPU operator (passed to setup script)
#   --mnnvl-enabled          Enable MNNVL feature (default, passed to setup script)
#   --mnnvl-disabled         Disable MNNVL feature (passed to setup script)
#   --build                  Build images (default, passed to setup script)
#   --skip-build             Skip image build (passed to setup script)
#   --skip-operator-wait     Don't wait for operator readiness (passed to setup script)
#   --image <tag>            Use existing image (passed to setup script)
#   --help                   Show this help message

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OPERATOR_DIR="$(dirname "$(dirname "$SCRIPT_DIR")")"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warning() { echo -e "${YELLOW}[WARNING]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

show_help() {
    head -20 "$0" | tail -16
    exit 0
}

# Parse arguments - separate our flags from setup flags
SKIP_SETUP=false
SETUP_ARGS=()

while [[ $# -gt 0 ]]; do
    case $1 in
        --skip-setup)
            SKIP_SETUP=true
            shift
            ;;
        --help)
            show_help
            ;;
        --image)
            # --image takes an argument, pass both
            SETUP_ARGS+=("$1" "$2")
            shift 2
            ;;
        *)
            # Pass all other arguments to setup script
            SETUP_ARGS+=("$1")
            shift
            ;;
    esac
done

# Run MNNVL e2e tests
run_e2e_tests() {
    log_info "Running MNNVL e2e tests..."
    cd "$OPERATOR_DIR"
    
    # Run tests with USE_EXISTING_CLUSTER=true and -count=1 to avoid caching
    USE_EXISTING_CLUSTER=true go test -tags=e2e -v -timeout=30m -count=1 \
        ./e2e/tests/auto-mnnvl/... 2>&1 | tee /tmp/mnnvl-e2e-results.log
    
    TEST_EXIT_CODE=${PIPESTATUS[0]}
    
    if [ $TEST_EXIT_CODE -eq 0 ]; then
        log_success "All MNNVL e2e tests passed!"
    else
        log_error "Some MNNVL e2e tests failed. See /tmp/mnnvl-e2e-results.log for details."
    fi
    
    return $TEST_EXIT_CODE
}

# Run cluster setup unless skipped
run_cluster_setup() {
    log_info "Running cluster setup with args: ${SETUP_ARGS[*]:-<none>}"
    if [ ${#SETUP_ARGS[@]} -eq 0 ]; then
        "$SCRIPT_DIR/setup-autoMNNVL-cluster.sh" || {
            log_error "Cluster setup failed!"
            exit 1
        }
    else
        "$SCRIPT_DIR/setup-autoMNNVL-cluster.sh" "${SETUP_ARGS[@]}" || {
            log_error "Cluster setup failed!"
            exit 1
        }
    fi
}

# Main execution
main() {
    log_info "=========================================="
    log_info "MNNVL E2E Test Runner"
    log_info "=========================================="
    
    cd "$OPERATOR_DIR"
    
    # Step 1: Setup cluster (unless skipped)
    if [ "$SKIP_SETUP" = false ]; then
        run_cluster_setup
    else
        log_warning "Skipping cluster setup (--skip-setup)"
    fi
    
    log_info "=========================================="
    log_info "Environment ready. Running e2e tests..."
    log_info "=========================================="
    
    # Step 2: Run tests
    run_e2e_tests
}

main "$@"
