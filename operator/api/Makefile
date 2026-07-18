# /*
# Copyright 2025 The Grove Authors.
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
REPO_ROOT       := $(shell dirname $(shell dirname "$(MODULE_ROOT)"))
REPO_HACK_DIR   := $(REPO_ROOT)/hack

.PHONY: tidy
tidy:
	@go mod tidy

# Include tools targets
include $(REPO_HACK_DIR)/tools.mk

# Generates code and CRDs.
.PHONY: generate
generate: $(CONTROLLER_GEN)
	@$(MODULE_HACK_DIR)/generate.sh

# Lints the codebase using GOLANGCI_LINT.
.PHONY: lint
lint: $(GOLANGCI_LINT)
	@$(GOLANGCI_LINT) run -c $(REPO_ROOT)/.golangci.yaml ./...
