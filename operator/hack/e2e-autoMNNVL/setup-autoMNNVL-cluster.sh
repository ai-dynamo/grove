#!/bin/bash
# setup-autoMNNVL-cluster.sh - Set up a k3d cluster for autoMNNVL e2e testing
#
# This script handles cluster creation, fake GPU operator installation,
# and Grove operator deployment with configurable options.
#
# Usage: ./hack/e2e-autoMNNVL/setup-autoMNNVL-cluster.sh [options]
#
# Options:
#   --skip-cluster-create    Reuse existing cluster (default: false)
#   --shutdown               Delete cluster and exit (no other args)
#   --with-fake-gpu          Install fake GPU operator (default)
#   --without-fake-gpu       Skip fake GPU operator installation
#   --mnnvl-enabled          Enable MNNVL feature in Grove (default)
#   --mnnvl-disabled         Disable MNNVL feature in Grove
#   --build                  Build images with docker (default)
#   --skip-build             Skip image build, use existing local images
#   --skip-operator-wait     Don't wait for operator pod readiness
#   --image <tag>            Use existing image with specified tag
#   --help                   Show this help message

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OPERATOR_DIR="$(dirname "$(dirname "$SCRIPT_DIR")")"

# Configuration
CLUSTER_NAME="mnnvl-test-cluster"
K3S_VERSION="v1.34.2-k3s1"
REGISTRY_NAME="mnnvl-registry"
REGISTRY_PORT="5001"
IMAGE_TAG="mnnvl-test"
GROVE_NAMESPACE="grove-system"

# Default options
SKIP_CLUSTER_CREATE=false
INSTALL_FAKE_GPU=true
MNNVL_ENABLED=true
BUILD_IMAGES=true
SKIP_OPERATOR_WAIT=false
CUSTOM_IMAGE_TAG=""
SEEN_WITH_FAKE_GPU=false
SEEN_WITHOUT_FAKE_GPU=false
SEEN_MNNVL_ENABLED=false
SEEN_MNNVL_DISABLED=false
SEEN_BUILD=false
SEEN_IMAGE=false
SEEN_SHUTDOWN=false
SEEN_OTHER_ARGS=false
SEEN_SKIP_BUILD=false

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

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --skip-cluster-create) SKIP_CLUSTER_CREATE=true; SEEN_OTHER_ARGS=true; shift ;;
        --shutdown) SEEN_SHUTDOWN=true; shift ;;
        --with-fake-gpu) INSTALL_FAKE_GPU=true; SEEN_WITH_FAKE_GPU=true; SEEN_OTHER_ARGS=true; shift ;;
        --without-fake-gpu) INSTALL_FAKE_GPU=false; SEEN_WITHOUT_FAKE_GPU=true; SEEN_OTHER_ARGS=true; shift ;;
        --mnnvl-enabled) MNNVL_ENABLED=true; SEEN_MNNVL_ENABLED=true; SEEN_OTHER_ARGS=true; shift ;;
        --mnnvl-disabled) MNNVL_ENABLED=false; SEEN_MNNVL_DISABLED=true; SEEN_OTHER_ARGS=true; shift ;;
        --build) BUILD_IMAGES=true; SEEN_BUILD=true; SEEN_OTHER_ARGS=true; shift ;;
        --skip-build) BUILD_IMAGES=false; SEEN_SKIP_BUILD=true; SEEN_OTHER_ARGS=true; shift ;;
        --skip-operator-wait) SKIP_OPERATOR_WAIT=true; SEEN_OTHER_ARGS=true; shift ;;
        --image)
            BUILD_IMAGES=false
            CUSTOM_IMAGE_TAG="$2"
            SEEN_IMAGE=true
            SEEN_OTHER_ARGS=true
            shift 2
            ;;
        --help) show_help ;;
        *) log_error "Unknown option: $1"; show_help ;;
    esac
done

# Validate conflicting arguments
if [ "$SEEN_SHUTDOWN" = true ] && [ "$SEEN_OTHER_ARGS" = true ]; then
    log_error "Conflicting arguments: --shutdown must be the only argument"
    exit 1
fi
if [ "$SEEN_WITH_FAKE_GPU" = true ] && [ "$SEEN_WITHOUT_FAKE_GPU" = true ]; then
    log_error "Conflicting arguments: --with-fake-gpu and --without-fake-gpu"
    exit 1
fi
if [ "$SEEN_MNNVL_ENABLED" = true ] && [ "$SEEN_MNNVL_DISABLED" = true ]; then
    log_error "Conflicting arguments: --mnnvl-enabled and --mnnvl-disabled"
    exit 1
fi
if [ "$SEEN_BUILD" = true ] && [ "$SEEN_IMAGE" = true ]; then
    log_error "Conflicting arguments: --build and --image"
    exit 1
fi
if [ "$SEEN_BUILD" = true ] && [ "$SEEN_SKIP_BUILD" = true ]; then
    log_error "Conflicting arguments: --build and --skip-build"
    exit 1
fi
if [ "$SEEN_SKIP_BUILD" = true ] && [ "$SEEN_IMAGE" = true ]; then
    log_error "Conflicting arguments: --skip-build and --image"
    exit 1
fi

# Cleanup function
cleanup_cluster() {
    log_info "Cleaning up existing k3d cluster..."
    k3d cluster delete "$CLUSTER_NAME" 2>/dev/null || true
    # Also clean up any orphaned registries
    docker stop "k3d-${REGISTRY_NAME}" 2>/dev/null || true
    docker rm "k3d-${REGISTRY_NAME}" 2>/dev/null || true
}

if [ "$SEEN_SHUTDOWN" = true ]; then
    cleanup_cluster
    exit 0
fi

# Pre-pull images to avoid timeout issues
prepull_images() {
    log_info "Pre-pulling required images..."
    docker pull "rancher/k3s:${K3S_VERSION}" || true
    docker pull "ghcr.io/k3d-io/k3d-proxy:5.8.3" || true
    docker pull "alpine:latest" || true
}

# Create k3d cluster
create_cluster() {
    log_info "Creating k3d cluster '${CLUSTER_NAME}' with k3s ${K3S_VERSION}..."
    
    k3d cluster create "$CLUSTER_NAME" \
        --servers 1 \
        --agents 2 \
        --image "rancher/k3s:${K3S_VERSION}" \
        --registry-create "${REGISTRY_NAME}:0.0.0.0:${REGISTRY_PORT}" \
        --port "6550:6443@loadbalancer" \
        --wait
    
    log_success "Cluster created successfully"
    
    # Verify cluster is running
    kubectl cluster-info
    kubectl get nodes
}

# Install fake GPU operator
install_fake_gpu_operator() {
    log_info "Installing fake GPU operator with ComputeDomain support..."
    
    # Clean up any conflicting resources from previous installations
    log_info "Cleaning up any conflicting resources..."
    kubectl delete runtimeclass nvidia 2>/dev/null || true
    helm uninstall fake-gpu-operator -n gpu-operator 2>/dev/null || true
    
    helm upgrade -i fake-gpu-operator \
        oci://ghcr.io/run-ai/fake-gpu-operator/fake-gpu-operator \
        --namespace gpu-operator \
        --create-namespace \
        --devel \
        --version 0.0.72 \
        --set computeDomainDraPlugin.enabled=true \
        --wait
    
    log_info "Waiting for fake GPU operator to be ready..."
    kubectl wait --for=condition=Ready pods -l app.kubernetes.io/name=fake-gpu-operator \
        -n gpu-operator --timeout=120s || true
    
    # Verify ComputeDomain CRD is installed
    if kubectl get crd computedomains.resource.nvidia.com &>/dev/null; then
        log_success "ComputeDomain CRD installed"
    else
        log_error "ComputeDomain CRD not found!"
        return 1
    fi
}

# Uninstall fake GPU operator
uninstall_fake_gpu_operator() {
    log_info "Ensuring fake GPU operator is not installed..."
    helm uninstall fake-gpu-operator -n gpu-operator 2>/dev/null || true
    kubectl delete runtimeclass nvidia 2>/dev/null || true
    kubectl delete crd computedomains.resource.nvidia.com 2>/dev/null || true
    log_success "Fake GPU operator removed (if it existed)"
}

# Build Grove images locally
build_images() {
    log_info "Building Grove operator images..."
    cd "$OPERATOR_DIR"
    
    # Prepare Helm charts
    ./hack/prepare-charts.sh
    
    # Build images for amd64 (k3d runs x86_64)
    GOARCH=amd64 PLATFORM=linux/amd64 DOCKER_BUILD_ADDITIONAL_ARGS="--load" ./hack/docker-build.sh
}

# Push existing local images to k3d registry
push_images_to_registry() {
    log_info "Pushing images to local registry..."
    
    docker tag grove-operator:latest "localhost:${REGISTRY_PORT}/grove-operator:${IMAGE_TAG}"
    docker tag grove-initc:latest "localhost:${REGISTRY_PORT}/grove-initc:${IMAGE_TAG}"
    docker push "localhost:${REGISTRY_PORT}/grove-operator:${IMAGE_TAG}"
    docker push "localhost:${REGISTRY_PORT}/grove-initc:${IMAGE_TAG}"
    
    # Also push alpine for test workloads
    docker tag alpine:latest "localhost:${REGISTRY_PORT}/alpine:latest"
    docker push "localhost:${REGISTRY_PORT}/alpine:latest"
    
    log_success "Images pushed to registry"
}

# Install Grove operator
install_grove_operator() {
    local mnnvl_flag="$1"
    
    log_info "Installing Grove operator (MNNVL=$mnnvl_flag)..."
    cd "$OPERATOR_DIR"
    
    # Determine image repository and tag
    local image_repo="${REGISTRY_NAME}:${REGISTRY_PORT}/grove-operator"
    local image_tag="${IMAGE_TAG}"
    local initc_image="${REGISTRY_NAME}:${REGISTRY_PORT}/grove-initc:${IMAGE_TAG}"
    
    if [ -n "$CUSTOM_IMAGE_TAG" ]; then
        image_tag="$CUSTOM_IMAGE_TAG"
        # When using custom image, assume it's from a public registry
        image_repo="grove-operator"
        initc_image="grove-initc:${CUSTOM_IMAGE_TAG}"
    fi
    
    # Uninstall existing if present
    helm uninstall grove-operator -n "$GROVE_NAMESPACE" 2>/dev/null || true
    # Wait for old pods to be fully removed before installing the new deployment;
    # otherwise kubectl wait may match both old (terminating) and new pods.
    kubectl wait --for=delete pods -l app.kubernetes.io/name=grove-operator \
        -n "$GROVE_NAMESPACE" --timeout=60s 2>/dev/null || true
    
    # Install with specified MNNVL setting
    helm install grove-operator ./charts \
        --namespace "$GROVE_NAMESPACE" \
        --create-namespace \
        --set "image.repository=${image_repo}" \
        --set "image.tag=${image_tag}" \
        --set "deployment.env[0].name=GROVE_INIT_CONTAINER_IMAGE" \
        --set "deployment.env[0].value=${initc_image}" \
        --set "config.network.autoMNNVLEnabled=${mnnvl_flag}" \
        --set "config.leaderElection.enabled=false" \
        --set "replicaCount=1"
    
    if [ "$SKIP_OPERATOR_WAIT" = true ]; then
        log_warning "Skipping operator readiness check (--skip-operator-wait)"
        # Give the pod a moment to be created
        sleep 5
    else
        log_info "Waiting for Grove operator to be ready..."
        kubectl wait --for=condition=Ready pods -l app.kubernetes.io/name=grove-operator \
            -n "$GROVE_NAMESPACE" --timeout=120s
    fi
    
    # Verify MNNVL configuration
    log_info "Verifying MNNVL configuration..."
    local config_value
    config_value=$(kubectl get configmap -n "$GROVE_NAMESPACE" -l app.kubernetes.io/component=operator-configmap \
        -o jsonpath='{.items[0].data.config\.yaml}' 2>/dev/null || echo "")
    
    if echo "$config_value" | grep -q "autoMNNVLEnabled: ${mnnvl_flag}"; then
        log_success "MNNVL is set to ${mnnvl_flag}"
    else
        log_warning "Could not verify MNNVL setting"
    fi
    
    # Show operator logs (first 20 lines)
    log_info "Operator startup logs:"
    kubectl logs -n "$GROVE_NAMESPACE" deployment/grove-operator --tail=20 || true
}

# Print configuration summary
print_config() {
    log_info "=========================================="
    log_info "MNNVL Cluster Setup"
    log_info "=========================================="
    log_info "Configuration:"
    log_info "  Skip cluster create: $SKIP_CLUSTER_CREATE"
    log_info "  Install fake GPU:    $INSTALL_FAKE_GPU"
    log_info "  MNNVL enabled:       $MNNVL_ENABLED"
    log_info "  Build images:        $BUILD_IMAGES"
    log_info "  Skip operator wait:  $SKIP_OPERATOR_WAIT"
    if [ -n "$CUSTOM_IMAGE_TAG" ]; then
        log_info "  Custom image tag:    $CUSTOM_IMAGE_TAG"
    fi
    log_info "=========================================="
}

# Main execution
main() {
    print_config
    
    cd "$OPERATOR_DIR"
    
    # Step 1: Cluster creation
    if [ "$SKIP_CLUSTER_CREATE" = false ]; then
        cleanup_cluster
        prepull_images
        create_cluster
    else
        log_warning "Skipping cluster creation (using existing cluster)"
    fi
    
    # Step 2: Fake GPU operator
    if [ "$INSTALL_FAKE_GPU" = true ]; then
        install_fake_gpu_operator
    else
        uninstall_fake_gpu_operator
    fi
    
    # Step 3: Build images (if requested)
    if [ "$BUILD_IMAGES" = true ]; then
        build_images
    else
        log_warning "Skipping image build (using existing images)"
    fi
    
    # Step 3b: Push images to registry
    # Always push when a new cluster was created (fresh registry has no images).
    # When reusing an existing cluster, push only if we just built new images.
    if [ "$SKIP_CLUSTER_CREATE" = false ] || [ "$BUILD_IMAGES" = true ]; then
        push_images_to_registry
    else
        log_info "Skipping image push (reusing existing cluster with existing images)"
    fi
    
    # Step 4: Install Grove operator
    install_grove_operator "$MNNVL_ENABLED"
    
    log_info "=========================================="
    log_success "Cluster setup complete!"
    log_info "  Fake GPU:      $INSTALL_FAKE_GPU"
    log_info "  MNNVL:         $MNNVL_ENABLED"
    log_info "=========================================="
}

main "$@"
