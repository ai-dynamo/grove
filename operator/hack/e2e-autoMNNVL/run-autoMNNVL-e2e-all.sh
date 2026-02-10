#!/bin/bash
# run-autoMNNVL-e2e-all.sh - Run autoMNNVL e2e tests with all 4 configurations
#
# This script runs the MNNVL e2e tests with all possible configurations:
#   1. Feature enabled + CRD supported (fake GPU installed)
#   2. Feature disabled + CRD supported (fake GPU installed)
#   3. Feature enabled + CRD unsupported (no fake GPU)
#   4. Feature disabled + CRD unsupported (no fake GPU)
#
# Usage: ./hack/e2e-autoMNNVL/run-autoMNNVL-e2e-all.sh [options]
#
# Options:
#   --skip-build    Build images once at start, reuse for all configs
#   --keep-cluster  Keep cluster after all configs (no shutdown)
#   --help          Show this help message

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OPERATOR_DIR="$(dirname "$(dirname "$SCRIPT_DIR")")"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warning() { echo -e "${YELLOW}[WARNING]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
log_header() { echo -e "${CYAN}[CONFIG]${NC} $1"; }

show_help() {
    head -15 "$0" | tail -11
    exit 0
}

# Parse arguments
SKIP_BUILD=false
KEEP_CLUSTER=false
for arg in "$@"; do
    case $arg in
        --skip-build) SKIP_BUILD=true ;;
        --keep-cluster) KEEP_CLUSTER=true ;;
        --help) show_help ;;
        *) log_error "Unknown option: $arg"; show_help ;;
    esac
done

# Track results using simple variables
RESULT_CONFIG1="NOT_RUN"
RESULT_CONFIG2="NOT_RUN"
RESULT_CONFIG3="NOT_RUN"
RESULT_CONFIG4="NOT_RUN"
OVERALL_SUCCESS=true

# Run a single configuration
run_config() {
    local config_num="$1"
    local config_name="$2"
    local fake_gpu_flag="$3"
    local mnnvl_flag="$4"
    shift 4
    local extra_flags=("$@")
    
    log_header "=========================================="
    log_header "Running: $config_name"
    log_header "  Fake GPU: $fake_gpu_flag"
    log_header "  MNNVL:    $mnnvl_flag"
    log_header "=========================================="
    
    if "$SCRIPT_DIR/run-autoMNNVL-e2e.sh" "$fake_gpu_flag" "$mnnvl_flag" "${extra_flags[@]}"; then
        eval "RESULT_CONFIG${config_num}=PASS"
        log_success "$config_name: PASSED"
    else
        eval "RESULT_CONFIG${config_num}=FAIL"
        log_error "$config_name: FAILED"
        OVERALL_SUCCESS=false
    fi
    
    echo ""
}

# Print final summary
print_summary() {
    echo ""
    log_info "=========================================="
    log_info "MNNVL E2E Test Summary"
    log_info "=========================================="
    
    local configs=("Config1_SupportedAndEnabled" "Config2_SupportedButDisabled" "Config3_UnsupportedButEnabled" "Config4_UnsupportedAndDisabled")
    local results=("$RESULT_CONFIG1" "$RESULT_CONFIG2" "$RESULT_CONFIG3" "$RESULT_CONFIG4")
    
    for i in 0 1 2 3; do
        local config="${configs[$i]}"
        local result="${results[$i]}"
        if [ "$result" = "PASS" ]; then
            log_success "$config: $result"
        elif [ "$result" = "FAIL" ]; then
            log_error "$config: $result"
        else
            log_warning "$config: $result"
        fi
    done
    
    log_info "=========================================="
    
    if [ "$OVERALL_SUCCESS" = true ]; then
        log_success "All configurations PASSED!"
        return 0
    else
        log_error "Some configurations FAILED!"
        return 1
    fi
}

# Main execution
main() {
    log_info "=========================================="
    log_info "MNNVL E2E Full Test Matrix"
    log_info "=========================================="
    log_info "This will run all 4 configurations:"
    log_info "  1. Feature enabled + CRD supported"
    log_info "  2. Feature disabled + CRD supported"
    log_info "  3. Feature enabled + CRD unsupported"
    log_info "  4. Feature disabled + CRD unsupported"
    log_info "=========================================="
    echo ""
    
    cd "$OPERATOR_DIR"
    
    # Build images once if not skipping
    if [ "$SKIP_BUILD" = false ]; then
        log_info "Building images once for all configurations..."
        ./hack/prepare-charts.sh
        GOARCH=amd64 PLATFORM=linux/amd64 DOCKER_BUILD_ADDITIONAL_ARGS="--load" ./hack/docker-build.sh
    else
        log_warning "Skipping initial build (--skip-build)"
    fi
    
    # Config 1: CRD supported + Feature enabled (fake GPU installed, MNNVL on)
    run_config 1 "Config1_SupportedAndEnabled" \
        "--with-fake-gpu" \
        "--mnnvl-enabled" \
        "--skip-build"
    
    # Config 2: CRD supported + Feature disabled (fake GPU installed, MNNVL off)
    run_config 2 "Config2_SupportedButDisabled" \
        "--with-fake-gpu" \
        "--mnnvl-disabled" \
        "--skip-cluster-create"
    
    # Config 3: CRD unsupported + Feature enabled (no fake GPU, MNNVL on)
    # The operator will intentionally exit (preflight failure) in this invalid
    # configuration. Use --skip-operator-wait so the setup doesn't block
    # waiting for a pod that will never become Ready; the e2e test itself
    # validates the expected failure behaviour.
    run_config 3 "Config3_UnsupportedButEnabled" \
        "--without-fake-gpu" \
        "--mnnvl-enabled" \
        "--skip-build" "--skip-operator-wait"
    
    # Config 4: CRD unsupported + Feature disabled (no fake GPU, MNNVL off)
    run_config 4 "Config4_UnsupportedAndDisabled" \
        "--without-fake-gpu" \
        "--mnnvl-disabled" \
        "--skip-cluster-create"
    
    # Shutdown cluster unless requested to keep it
    if [ "$KEEP_CLUSTER" = false ]; then
        log_info "Shutting down cluster..."
        "$SCRIPT_DIR/setup-autoMNNVL-cluster.sh" --shutdown
    else
        log_warning "Keeping cluster (--keep-cluster)"
    fi

    # Print summary
    print_summary
}

main "$@"
