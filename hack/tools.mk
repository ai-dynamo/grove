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

SYSTEM_NAME       := $(shell uname -s | tr '[:upper:]' '[:lower:]')
SYSTEM_ARCH       := $(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
TOOLS_DIR         := $(REPO_HACK_DIR)/tools
TOOLS_BIN_DIR     := $(TOOLS_DIR)/bin
CONTROLLER_GEN    := $(TOOLS_BIN_DIR)/controller-gen
SETUP_ENVTEST     := $(TOOLS_BIN_DIR)/setup-envtest
KIND              := $(TOOLS_BIN_DIR)/kind
GOLANGCI_LINT     := $(TOOLS_BIN_DIR)/golangci-lint
GOIMPORTS_REVISER := $(TOOLS_BIN_DIR)/goimports-reviser
YQ                := $(TOOLS_BIN_DIR)/yq
GO_ADD_LICENSE    := $(TOOLS_BIN_DIR)/addlicense
SKAFFOLD          := $(TOOLS_BIN_DIR)/skaffold
CRD_REF_DOCS      := $(TOOLS_BIN_DIR)/crd-ref-docs
MDTOC			  := $(TOOLS_BIN_DIR)/mdtoc

# default tool versions
# -------------------------------------------------------------------------
CONTROLLER_GEN_VERSION    ?= $(call version_gomod,sigs.k8s.io/controller-tools)
KIND_VERSION              ?= v0.30.0
GOLANGCI_LINT_VERSION     ?= v2.6.1
GOIMPORTS_REVISER_VERSION ?= v3.10.0
YQ_VERSION                ?= v4.48.1
GO_ADD_LICENSE_VERSION    ?= v1.2.0
SKAFFOLD_VERSION          ?= v2.16.1
CRD_REF_DOCS_VERSION      ?= v0.2.0
MDTOC_VERSION             ?= latest

export PATH := $(abspath $(TOOLS_BIN_DIR)):$(PATH)

# Ensure the tools bin directory exists
$(shell mkdir -p $(TOOLS_BIN_DIR) > /dev/null)

# Common
# -------------------------------------------------------------------------
# Use this function to get the version of a go module from go.mod
version_gomod = $(shell go list -mod=mod -f '{{ .Version }}' -m $(1))

.PHONY: clean-tools-bin
clean-tools-bin:
	@rm -rf $(TOOLS_BIN_DIR)/*

# Tools
# -------------------------------------------------------------------------
$(CONTROLLER_GEN):
	GOBIN=$(abspath $(TOOLS_BIN_DIR)) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

$(SETUP_ENVTEST):
	GOBIN=$(abspath $(TOOLS_BIN_DIR)) go install sigs.k8s.io/controller-runtime/tools/setup-envtest

$(GOLANGCI_LINT):
	@# CGO_ENABLED has to be set to 1 in order for golangci-lint to be able to load plugins
	@# see https://github.com/golangci/golangci-lint/issues/1276
	GOBIN=$(abspath $(TOOLS_BIN_DIR)) CGO_ENABLED=1 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GOIMPORTS_REVISER):
	GOBIN=$(abspath $(TOOLS_BIN_DIR)) go install github.com/incu6us/goimports-reviser/v3@$(GOIMPORTS_REVISER_VERSION)

$(KIND):
	curl -Lo $(KIND) https://kind.sigs.k8s.io/dl/$(KIND_VERSION)/kind-$(SYSTEM_NAME)-$(SYSTEM_ARCH)
	chmod +x $(KIND)

$(YQ):
	curl -Lo $(YQ) https://github.com/mikefarah/yq/releases/download/$(YQ_VERSION)/yq_$(SYSTEM_NAME)_$(SYSTEM_ARCH)
	chmod +x $(YQ)

$(GO_ADD_LICENSE):
	GOBIN=$(abspath $(TOOLS_BIN_DIR)) go install github.com/google/addlicense@$(GO_ADD_LICENSE_VERSION)

$(SKAFFOLD):
	curl -Lo $(SKAFFOLD) https://storage.googleapis.com/skaffold/releases/$(SKAFFOLD_VERSION)/skaffold-$(SYSTEM_NAME)-$(SYSTEM_ARCH)
	chmod +x $(SKAFFOLD)

$(CRD_REF_DOCS):
	GOBIN=$(abspath $(TOOLS_BIN_DIR)) go install github.com/elastic/crd-ref-docs@$(CRD_REF_DOCS_VERSION)

$(MDTOC):
	GOBIN=$(abspath $(TOOLS_BIN_DIR)) go install sigs.k8s.io/mdtoc@latest