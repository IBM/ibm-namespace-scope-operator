# Copyright 2022 IBM Corporation
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

.DEFAULT_GOAL:=help

# Dependence tools
KUBECTL ?= $(shell which kubectl)
OPERATOR_SDK ?= $(shell which operator-sdk)
CONTROLLER_GEN ?= $(shell which controller-gen)
KUSTOMIZE ?= $(shell which kustomize)
YQ_VERSION=v4.27.3
KUSTOMIZE_VERSION=v3.8.7
OPERATOR_SDK_VERSION=v1.32.0
OPENSHIFT_VERSIONS ?= v4.12-v4.17

GOPATH=$(HOME)/go/bin/

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Specify whether this repo is build locally or not, default values is '1';
# If set to 1, then you need to also set 'DOCKER_USERNAME' and 'DOCKER_PASSWORD'
# environment variables before build the repo.
BUILD_LOCALLY ?= 1

VCS_REF ?= $(shell git rev-parse HEAD)
BUILD_VERSION ?= $(shell git describe --exact-match 2> /dev/null || \
                git describe --match=$(git rev-parse --short=8 HEAD) --always --dirty --abbrev=8)
RELEASE_VERSION ?= $(shell cat ./version/version.go | grep "Version =" | awk '{ print $$3}' | tr -d '"')
LATEST_VERSION ?= latest

LOCAL_OS := $(shell uname)
ifeq ($(LOCAL_OS),Linux)
    TARGET_OS ?= linux
    XARGS_FLAGS="-r"
	STRIP_FLAGS=
else ifeq ($(LOCAL_OS),Darwin)
    TARGET_OS ?= darwin
    XARGS_FLAGS=
	STRIP_FLAGS="-x"
else
    $(error "This system's OS $(LOCAL_OS) isn't recognized/supported")
endif

ARCH := $(shell uname -m)
# Auto-detect LOCAL_ARCH only when the caller hasn't provided one.
ifeq ($(origin LOCAL_ARCH), undefined)
LOCAL_ARCH := amd64
ifeq ($(ARCH),x86_64)
    LOCAL_ARCH := amd64
else ifeq ($(ARCH),ppc64le)
    LOCAL_ARCH := ppc64le
else ifeq ($(ARCH),s390x)
    LOCAL_ARCH := s390x
else ifeq ($(ARCH),arm64)
    LOCAL_ARCH := arm64
else
    $(error "This system's ARCH $(ARCH) isn't recognized/supported")
endif
endif

# Default image repo
QUAY_REGISTRY ?= quay.io/opencloudio
ICR_REIGSTRY ?= icr.io/cpopen

ifeq ($(BUILD_LOCALLY),0)
DOCKER_REGISTRY ?= "docker-na-public.artifactory.swg-devops.com/hyc-cloud-private-integration-docker-local/ibmcom"
else
DOCKER_REGISTRY ?= "docker-na-public.artifactory.swg-devops.com/hyc-cloud-private-scratch-docker-local/ibmcom"
endif

BUILDX_BUILDER ?= ibm-namespace-scope-operator-builder

RELEASE_IMAGE ?= $(DOCKER_REGISTRY)/$(OPERATOR_IMAGE_NAME):$(BUILD_VERSION)
RELEASE_IMAGE_ARCH ?= $(DOCKER_REGISTRY)/$(OPERATOR_IMAGE_NAME)-$(LOCAL_ARCH):$(BUILD_VERSION)
LOCAL_ARCH_IMAGE ?= $(OPERATOR_IMAGE_NAME)-$(LOCAL_ARCH):$(BUILD_VERSION)

# Current Operator image name
OPERATOR_IMAGE_NAME ?= ibm-namespace-scope-operator
# Current Operator bundle image name
BUNDLE_IMAGE_NAME ?= ibm-namespace-scope-operator-bundle

# Options for 'bundle-build'
CHANNELS ?= v4.3
DEFAULT_CHANNEL ?= v4.3
ifneq ($(origin CHANNELS), undefined)
BUNDLE_CHANNELS := --channels=$(CHANNELS)
endif
ifneq ($(origin DEFAULT_CHANNEL), undefined)
BUNDLE_DEFAULT_CHANNEL := --default-channel=$(DEFAULT_CHANNEL)
endif
BUNDLE_METADATA_OPTS ?= $(BUNDLE_CHANNELS) $(BUNDLE_DEFAULT_CHANNEL)

# Generate CRDs using v1, which is the recommended version for Kubernetes 1.16+.
CRD_OPTIONS ?= "crd:crdVersions=v1"

include common/Makefile.common.mk

##@ Development

code-dev: ## Run the default dev commands which are the go tidy, fmt, vet then execute the $ make code-gen
	@echo Running the common required commands for developments purposes
	- make generate
	- make manifests
	- make code-tidy
	- make code-fmt
	- make code-vet
	- make lint-all

check: ## Check code lint error
	make lint-all

build: ## Build manager binary
	go build -o bin/manager main.go

run: generate code-fmt code-vet manifests check ## Run against the configured Kubernetes cluster in ~/.kube/config
	OPERATOR_NAMESPACE=ibm-common-services go run ./main.go

install: manifests ## Install CRDs into a cluster
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

uninstall: manifests ## Uninstall CRDs from a cluster
	$(KUSTOMIZE) build config/crd | kubectl delete -f -

deploy: manifests ## Deploy controller in the configured Kubernetes cluster in ~/.kube/config
	cd config/manager && $(KUSTOMIZE) edit set image icr.io/cpopen/ibm-namespace-scope-operator=$(QUAY_REGISTRY)/$(OPERATOR_IMAGE_NAME):$(RELEASE_VERSION)
	$(KUSTOMIZE) build config/default | kubectl apply -f -

undeploy: ## Undeploy controller in the configured Kubernetes cluster in ~/.kube/config
	cd config/manager && $(KUSTOMIZE) edit set image icr.io/cpopen/ibm-namespace-scope-operator=$(QUAY_REGISTRY)/$(OPERATOR_IMAGE_NAME):$(RELEASE_VERSION)
	$(KUSTOMIZE) build config/default | kubectl delete -f -

kustomize: ## Install kustomize
ifeq (, $(shell which kustomize 2>/dev/null))
	@{ \
	set -e ;\
	mkdir -p bin ;\
	echo "Downloading kustomize ...";\
	curl -sSLo - https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize/$(KUSTOMIZE_VERSION)/kustomize_$(KUSTOMIZE_VERSION)_$(LOCAL_OS)_$(LOCAL_ARCH).tar.gz | tar xzf - -C bin/ ;\
	}
KUSTOMIZE=$(realpath ./bin/kustomize)
else
KUSTOMIZE=$(shell which kustomize)
endif

operator-sdk:
ifneq ($(shell operator-sdk version | cut -d ',' -f1 | cut -d ':' -f2 | tr -d '"' | xargs | cut -d '.' -f1), v1)
	@{ \
	if [ "$(shell ./bin/operator-sdk version | cut -d ',' -f1 | cut -d ':' -f2 | tr -d '"' | xargs)" != $(OPERATOR_SDK_VERSION) ]; then \
		set -e ; \
		mkdir -p bin ;\
		echo "Downloading operator-sdk..." ;\
		curl -sSLo ./bin/operator-sdk "https://github.com/operator-framework/operator-sdk/releases/download/$(OPERATOR_SDK_VERSION)/operator-sdk_$(LOCAL_OS)_$(LOCAL_ARCH)" ;\
		chmod +x ./bin/operator-sdk ;\
	fi ;\
	}
OPERATOR_SDK=$(realpath ./bin/operator-sdk)
else
OPERATOR_SDK=$(shell which operator-sdk)
endif

yq: ## Install yq, a yaml processor
ifneq ($(shell yq -V | cut -d ' ' -f 3 | cut -d '.' -f 1 ), 4)
	@{ \
	if [ v$(shell ./bin/yq --version | cut -d ' ' -f3) != $(YQ_VERSION) ]; then\
		set -e ;\
		mkdir -p bin ;\
		echo "Downloading yq ...";\
		curl -sSLO https://github.com/mikefarah/yq/releases/download/$(YQ_VERSION)/yq_$(LOCAL_OS)_$(LOCAL_ARCH);\
		mv yq_$(LOCAL_OS)_$(LOCAL_ARCH) ./bin/yq ;\
		chmod +x ./bin/yq ;\
	fi;\
	}
YQ=$(realpath ./bin/yq)
else
YQ=$(shell which yq)
endif

clis: yq kustomize operator-sdk controller-gen

##@ Generate code and manifests
manifests: controller-gen ## Generate manifests e.g. CRD, RBAC etc.
	$(CONTROLLER_GEN) $(CRD_OPTIONS) rbac:roleName=ibm-namespace-scope-operator webhook paths="./..." output:crd:artifacts:config=config/crd/bases

generate: controller-gen ## Generate code e.g. API etc.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

generate-csv-manifests: operator-sdk ## Generate CSV manifests
	$(OPERATOR_SDK) generate kustomize manifests

bundle: clis generate manifests generate-csv-manifests ## Generate bundle manifests	
	# Generate bundle manifests
	cd config/manager && $(KUSTOMIZE) edit set image icr.io/cpopen/ibm-namespace-scope-operator=$(ICR_REIGSTRY)/$(OPERATOR_IMAGE_NAME):$(RELEASE_VERSION)
	$(KUSTOMIZE) build config/manifests | $(OPERATOR_SDK) generate bundle \
	-q --version $(RELEASE_VERSION) $(BUNDLE_METADATA_OPTS)
	- sed -i '$$a\\n# OpenShift annotations.\n  com.redhat.openshift.versions: $(OPENSHIFT_VERSIONS)' bundle/metadata/annotations.yaml
	- $(YQ) eval-all -i '.spec.relatedImages = load("config/manifests/bases/ibm-namespace-scope-operator.clusterserviceversion.yaml").spec.relatedImages' bundle/manifests/ibm-namespace-scope-operator.clusterserviceversion.yaml
	- $(OPERATOR_SDK) bundle validate ./bundle

##@ Test

test: ## Run unit test on prow
	@echo good

e2e-test: ## Run e2e test
	@echo "Running e2e tests for the controllers."
	@USE_EXISTING_CLUSTER=true \
	OPERATOR_NAME=ibm-namespace-scope-operator \
	OPERATOR_NAMESPACE=ibm-common-services \
	go test ./controllers/... -coverprofile cover.out

##@ Build

.PHONY: prepare-buildx
prepare-buildx:
	@docker buildx inspect $(BUILDX_BUILDER) >/dev/null 2>&1 || docker buildx create --name $(BUILDX_BUILDER) --driver docker-container --use
	@docker buildx use $(BUILDX_BUILDER)
	@docker run --privileged --rm tonistiigi/binfmt --install all >/dev/null

build-operator-image: config-docker prepare-buildx ## Build the operator image.
	@echo "Building the $(OPERATOR_IMAGE_NAME) docker image for $(LOCAL_ARCH)..."
	@$(CONTAINER_TOOL) buildx build \
		--builder $(BUILDX_BUILDER) \
		--platform linux/$(LOCAL_ARCH) \
		--build-arg VCS_REF=$(VCS_REF) \
		--build-arg RELEASE_VERSION=$(RELEASE_VERSION) \
		--build-arg TARGETOS=linux \
		--build-arg TARGETARCH=$(LOCAL_ARCH) \
		-t $(LOCAL_ARCH_IMAGE) \
		--load \
		-f Dockerfile .
	@$(CONTAINER_TOOL) tag $(LOCAL_ARCH_IMAGE) $(RELEASE_IMAGE_ARCH)

build-push-image: config-docker build-operator-image  ## Build and push the operator images.
	@echo "Pushing $(OPERATOR_IMAGE_NAME) docker image for $(LOCAL_ARCH)..."
	$(MAKE) docker-push IMG=$(RELEASE_IMAGE_ARCH)

.PHONY: docker-push
docker-push:
	docker push $(IMG)

##@ Release

multiarch-image: config-docker  ## Generate multiarch images for operator image.
	@MAX_PULLING_RETRY=20 RETRY_INTERVAL=30 common/scripts/multiarch_image.sh $(DOCKER_REGISTRY) $(OPERATOR_IMAGE_NAME) $(BUILD_VERSION) $(RELEASE_VERSION)

build-bundle-image:
	docker build -f bundle.Dockerfile -t $(QUAY_REGISTRY)/$(BUNDLE_IMAGE_NAME):$(BUILD_VERSION) .
	docker push $(QUAY_REGISTRY)/$(BUNDLE_IMAGE_NAME):$(BUILD_VERSION)

build-catalog-source:
	opm -u docker index add --bundles $(QUAY_REGISTRY)/$(BUNDLE_IMAGE_NAME):$(BUILD_VERSION) --tag $(QUAY_REGISTRY)/$(OPERATOR_IMAGE_NAME)-catalog:$(BUILD_VERSION)
	docker push $(QUAY_REGISTRY)/$(OPERATOR_IMAGE_NAME)-catalog:$(BUILD_VERSION)

run-bundle:
	$(OPERATOR_SDK) run bundle $(QUAY_REGISTRY)/$(BUNDLE_IMAGE_NAME):$(BUILD_VERSION) --install-mode OwnNamespace

cleanup-bundle:
	$(OPERATOR_SDK) cleanup ibm-namespace-scope-operator

##@ Help
help: ## Display this help
	@echo "Usage:\n  make \033[36m<target>\033[0m"
	@awk 'BEGIN {FS = ":.*##"}; \
		/^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
