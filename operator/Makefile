# /*
# Copyright 2024 The Grove Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
# */

MODULE_ROOT     := $(shell dirname "$(realpath $(lastword $(MAKEFILE_LIST)))")
MODULE_HACK_DIR := $(MODULE_ROOT)/hack
REPO_ROOT       := $(shell dirname "$(MODULE_ROOT)")
REPO_HACK_DIR   := $(REPO_ROOT)/hack
KUBECONFIG_PATH := $(if $(KUBECONFIG),$(KUBECONFIG),$(MODULE_HACK_DIR)/kind/kubeconfig)
# Path of the package that ld-flags writes to
LD_PACKAGE_PATH := "github.com/ai-dynamo/grove/operator/internal"
OPERATOR_NAME   := "grove-operator"
INITC_NAME      := "grove-initc"
K3S_IMAGE       ?= rancher/k3s:v1.34.2-k3s1

# Include tools targets
include $(REPO_HACK_DIR)/tools.mk

kind-up kind-down deploy deploy-dev deploy-debug undeploy deploy-addons: export KUBECONFIG = $(KUBECONFIG_PATH)

.PHONY: tidy
tidy:
	@go mod tidy

# Builds grove-operator for the current platform.
.PHONY: build-operator
build-operator:
	@PACKAGE_PATH=${LD_PACKAGE_PATH} PROGRAM_NAME=${OPERATOR_NAME} $(MODULE_HACK_DIR)/build-operator.sh

# Builds grove-operator for the specified platform.
# Usage: make build-operator-<os>-<arch>
# Example: make build-operator-linux-amd64, make build-operator-darwin-amd64, make build-operator-darwin-arm64, make build-operator-linux-arm64
.PHONY: build-operator-%
build-operator-%:
	@GOOS=$$(echo $* | cut -d- -f 1) GOARCH=$$(echo $* | cut -d- -f 2) PACKAGE_PATH=${LD_PACKAGE_PATH} PROGRAM_NAME=${OPERATOR_NAME} $(MODULE_HACK_DIR)/build-operator.sh

# Builds grove-initc for the current platform.
.PHONY: build-initc
build-initc:
	@PACKAGE_PATH=${LD_PACKAGE_PATH} PROGRAM_NAME=${INITC_NAME} $(MODULE_HACK_DIR)/build-initc.sh

# Builds grove-initc for the specified platform.
# Usage: make build-initc-<os>-<arch>
# Example: make build-initc-linux-amd64, make build-initc-darwin-amd64, make build-initc-darwin-arm64, make build-initc-linux-arm64
.PHONY: build-initc-%
build-initc-%:
	@GOOS=$$(echo $* | cut -d- -f 1) GOARCH=$$(echo $* | cut -d- -f 2) PACKAGE_PATH=${LD_PACKAGE_PATH} PROGRAM_NAME=${INITC_NAME} $(MODULE_HACK_DIR)/build-initc.sh

# Lints the codebase using GOLANGCI_LINT.
.PHONY: lint
lint: $(GOLANGCI_LINT)
	@$(GOLANGCI_LINT) run -c $(REPO_ROOT)/.golangci.yaml ./...

# Formats the codebase
.PHONY: format
format: $(GOIMPORTS_REVISER)
	@$(REPO_HACK_DIR)/format.sh ./api/ ./cmd/ ./initc/ ./internal/  ./test/

# Make targets for tests
# -------------------------------------------------------------

# Run all unit tests
.PHONY: test-unit
test-unit:
	@go test ./...

# Run all unit tests with code coverage
.PHONY: test-cover
test-cover:
	@go test ./... -coverprofile=coverage.out

# Generate HTML coverage report
.PHONY: cover-html
cover-html: test-cover
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated at coverage.html"

# Run e2e tests against an existing cluster
# Requires: E2E_USE_EXISTING_CLUSTER=true and a pre-created cluster
# Usage: make run-e2e [TEST_PATTERN=<pattern>] [DIAG_DIR=<path>] [DIAG_MODE=<mode>]
# Examples:
#   make run-e2e                              # Run all tests
#   make run-e2e TEST_PATTERN=Test_GS         # Run gang scheduling tests
#   make run-e2e TEST_PATTERN=Test_GS1        # Run specific test
#   make run-e2e TEST_PATTERN=Test_TAS        # Run topology tests
#   GROVE_E2E_DIAG_MODE=both make run-e2e     # Output to stdout and file
#   GROVE_E2E_DIAG_MODE=stdout make run-e2e   # Output to stdout only
#
# Environment variables:
#   GROVE_E2E_DIAG_MODE - Diagnostic output mode: stdout, file, both (default: file)
#   GROVE_E2E_DIAG_DIR  - Directory for diagnostic files (default: operator/)
.PHONY: run-e2e
run-e2e: export GROVE_E2E_DIAG_DIR = $(DIAG_DIR)
run-e2e: export GROVE_E2E_DIAG_MODE = $(DIAG_MODE)
run-e2e:
	@echo "> Preparing charts (copying CRDs)..."
	@$(MODULE_HACK_DIR)/prepare-charts.sh
	@echo "> Running e2e tests..."
	@cd e2e && go test -count=1 -tags=e2e ./tests/... -v -timeout 45m $(if $(TEST_PATTERN),-run $(TEST_PATTERN))

# Create a k3d cluster for e2e testing (with Grove and Kai scheduler deployed)
# This is optional - tests work with any Kubernetes cluster that has the required components
# Pass E2E_CREATE_FLAGS to add CLI flags (e.g. --skip-kai --skip-topology --skip-prepull)
.PHONY: e2e-cluster-up
e2e-cluster-up:
	@$(MODULE_HACK_DIR)/infra-manager.py $(E2E_CREATE_FLAGS)

# Delete the k3d e2e test cluster
.PHONY: e2e-cluster-down
e2e-cluster-down:
	@$(MODULE_HACK_DIR)/infra-manager.py --delete

# Full e2e test run: create k3d cluster, run tests, then cleanup
# Usage: make run-e2e-full [TEST_PATTERN=<pattern>]
# Examples:
#   make run-e2e-full                          # Run all tests
#   make run-e2e-full TEST_PATTERN=Test_GS     # Run gang scheduling tests only
.PHONY: run-e2e-full
run-e2e-full: e2e-cluster-up
	@echo "> Running e2e tests..."
	@E2E_REGISTRY_PORT=5001 $(MAKE) run-e2e TEST_PATTERN=$(TEST_PATTERN) || \
		(echo "Tests failed, cleaning up cluster..."; $(MAKE) e2e-cluster-down; exit 1)
	@echo "> Tests passed, cleaning up cluster..."
	@$(MAKE) e2e-cluster-down

# Run autoMNNVL e2e tests (all 4 configurations: supported/unsupported x enabled/disabled)
# Creates a lightweight k3d cluster (2 workers, no Kai/topology), runs all configurations
# sequentially via config-cluster.py, then cleans up. Uses the same e2e-cluster-up target
# with MNNVL-specific env overrides and flags.
# Usage: make run-e2e-mnnvl-full
.PHONY: run-e2e-mnnvl-full
run-e2e-mnnvl-full: export E2E_WORKER_NODES = 2
run-e2e-mnnvl-full: export E2E_K3S_IMAGE = $(K3S_IMAGE)
run-e2e-mnnvl-full: E2E_CREATE_FLAGS = --skip-prepull
run-e2e-mnnvl-full: e2e-cluster-up
	@echo "> Pushing alpine image to local registry..."
	@docker pull alpine:latest || true
	@docker tag alpine:latest localhost:5001/alpine:latest
	@docker push localhost:5001/alpine:latest
	@echo "> Running autoMNNVL e2e tests (all 4 configurations)..."
	@python3 $(MODULE_HACK_DIR)/e2e-autoMNNVL/run_autoMNNVL_e2e_all.py || \
		(echo "Tests failed, cleaning up cluster..."; $(MAKE) e2e-cluster-down; exit 1)
	@echo "> Tests passed, cleaning up cluster..."
	@$(MAKE) e2e-cluster-down


# Make targets for local development and testing
# -------------------------------------------------------------
# Starts a local k8s cluster using kind.
# Usage: make kind-up [FAKE_NODES=<count>]
# Example: make kind-up FAKE_NODES=20
.PHONY: kind-up
kind-up: $(KIND) $(YQ)
	@$(MODULE_HACK_DIR)/kind-up.sh $(if $(FAKE_NODES),--fake-nodes $(FAKE_NODES))

# Stops the local k8s cluster.
.PHONY: kind-down
kind-down: $(KIND)
	@$(MODULE_HACK_DIR)/kind-down.sh

# Make targets for docker images
# -------------------------------------------------------------

# Builds the grove-initc and grove-operator docker images for the specified platform.
# Usage: PLATFORM=linux/amd64 make docker-build
# Multiplatform builds: PLATFORM=linux/amd64,linux/arm64 make docker-build
.PHONY: docker-build
docker-build:
	@$(MODULE_HACK_DIR)/docker-build.sh

# Make targets for deploying grove-operator to a k8s cluster via skaffold
# -------------------------------------------------------------
# Deploys grove-operator to a k8s cluster using skaffold.

ifndef NAMESPACE
override NAMESPACE = default
endif

# Deploys grove-operator to a k8s cluster using skaffold.
.PHONY: deploy
deploy: $(SKAFFOLD)
	@$(MODULE_HACK_DIR)/prepare-charts.sh
	@PACKAGE_PATH=${LD_PACKAGE_PATH} PROGRAM_NAME=${OPERATOR_NAME} $(MODULE_HACK_DIR)/deploy.sh run -m grove-operator -n $(NAMESPACE)

# Deploys grove-operator to a k8s cluster using skaffold in dev mode.
.PHONY: deploy-dev
deploy-dev: $(SKAFFOLD)
	@$(MODULE_HACK_DIR)/prepare-charts.sh
	@PACKAGE_PATH=${LD_PACKAGE_PATH} PROGRAM_NAME=${OPERATOR_NAME} $(MODULE_HACK_DIR)/deploy.sh dev --cleanup=false --keep-running-on-failure -m grove-operator --trigger='manual' -n $(NAMESPACE)

# Deploys grove-operator to a k8s cluster using skaffold in debug mode.
.PHONY: deploy-debug
deploy-debug: $(SKAFFOLD)
	@$(MODULE_HACK_DIR)/prepare-charts.sh
	@PACKAGE_PATH=${LD_PACKAGE_PATH} PROGRAM_NAME=${OPERATOR_NAME} $(MODULE_HACK_DIR)/deploy.sh debug --cleanup=false -m grove-operator -p debug -n $(NAMESPACE)

# Undeploys grove-operator from a k8s cluster using skaffold.
.PHONY: undeploy
undeploy: $(SKAFFOLD)
	@$(SKAFFOLD) delete -m grove-operator -n $(NAMESPACE)

# Deploys all the add-ons to a k8s cluster.
.PHONY: deploy-addons
deploy-addons:
	@$(MODULE_HACK_DIR)/deploy-addons.sh --all

.PHONY: clean-chart-resources
clean-chart-resources:
	@rm -f $(MODULE_ROOT)/charts/crds/*.yaml
