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
YQ_VERSION=3.4.0
KUSTOMIZE_VERSION=v3.8.7
OPERATOR_SDK_VERSION=v1.32.0

GOPATH=$(HOME)/go/bin/

# Specify whether this repo is build locally or not, default values is '1';
# If set to 1, then you need to also set 'DOCKER_USERNAME' and 'DOCKER_PASSWORD'
# environment variables before build the repo.
BUILD_LOCALLY ?= 1

VCS_URL ?= https://github.com/IBM/ibm-namespace-scope-operator
VCS_REF ?= $(shell git rev-parse HEAD)
VERSION ?= $(shell git describe --exact-match 2> /dev/null || \
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
LOCAL_ARCH := "amd64"
ifeq ($(ARCH),x86_64)
    LOCAL_ARCH="amd64"
else ifeq ($(ARCH),ppc64le)
    LOCAL_ARCH="ppc64le"
else ifeq ($(ARCH),s390x)
    LOCAL_ARCH="s390x"
else
    $(error "This system's ARCH $(ARCH) isn't recognized/supported")
endif

# Default image repo
QUAY_REGISTRY ?= quay.io/opencloudio

ifeq ($(BUILD_LOCALLY),0)
ARTIFACTORYA_REGISTRY ?= "docker-na-public.artifactory.swg-devops.com/hyc-cloud-private-integration-docker-local/ibmcom"
else
ARTIFACTORYA_REGISTRY ?= "docker-na-public.artifactory.swg-devops.com/hyc-cloud-private-scratch-docker-local/ibmcom"
endif

# Current Operator image name
OPERATOR_IMAGE_NAME ?= ibm-namespace-scope-operator
# Current Operator bundle image name
BUNDLE_IMAGE_NAME ?= ibm-namespace-scope-operator-bundle
# Current Operator version
OPERATOR_VERSION ?= 4.2.12

# Options for 'bundle-build'
CHANNELS ?= v4.0
DEFAULT_CHANNEL ?= v4.0
ifneq ($(origin CHANNELS), undefined)
BUNDLE_CHANNELS := --channels=$(CHANNELS)
endif
ifneq ($(origin DEFAULT_CHANNEL), undefined)
BUNDLE_DEFAULT_CHANNEL := --default-channel=$(DEFAULT_CHANNEL)
endif
BUNDLE_METADATA_OPTS ?= $(BUNDLE_CHANNELS) $(BUNDLE_DEFAULT_CHANNEL)

# Produce CRDs that work back to Kubernetes 1.11 (no version conversion)
CRD_OPTIONS ?= "crd:trivialVersions=true"

ifeq ($(BUILD_LOCALLY),0)
    export CONFIG_DOCKER_TARGET = config-docker
endif

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
	cd config/manager && $(KUSTOMIZE) edit set image ibm-namespace-scope-operator=$(QUAY_REGISTRY)/$(OPERATOR_IMAGE_NAME):$(OPERATOR_VERSION)
	$(KUSTOMIZE) build config/default | kubectl apply -f -

undeploy: ## Undeploy controller in the configured Kubernetes cluster in ~/.kube/config
	cd config/manager && $(KUSTOMIZE) edit set image ibm-namespace-scope-operator=$(QUAY_REGISTRY)/$(OPERATOR_IMAGE_NAME):$(OPERATOR_VERSION)
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

bundle: clis generate manifests ## Generate bundle manifests
	# Generate bundle manifests
	- $(KUSTOMIZE) build config/manifests | $(OPERATOR_SDK) generate bundle \
	-q --version $(OPERATOR_VERSION) $(BUNDLE_METADATA_OPTS)
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

build-operator-image: $(CONFIG_DOCKER_TARGET) ## Build the operator image.
	@echo "Building the $(OPERATOR_IMAGE_NAME) docker image for $(LOCAL_ARCH)..."
	@docker build -t $(OPERATOR_IMAGE_NAME)-$(LOCAL_ARCH):$(VERSION) \
	--build-arg VCS_REF=$(VCS_REF) --build-arg VCS_URL=$(VCS_URL) \
	--build-arg GOARCH=$(LOCAL_ARCH) -f Dockerfile .

##@ Release

build-push-image: $(CONFIG_DOCKER_TARGET) build-operator-image  ## Build and push the operator images.
	@echo "Pushing the $(OPERATOR_IMAGE_NAME) docker image for $(LOCAL_ARCH)..."
	@docker tag $(OPERATOR_IMAGE_NAME)-$(LOCAL_ARCH):$(VERSION) $(ARTIFACTORYA_REGISTRY)/$(OPERATOR_IMAGE_NAME)-$(LOCAL_ARCH):$(VERSION)
	@docker push $(ARTIFACTORYA_REGISTRY)/$(OPERATOR_IMAGE_NAME)-$(LOCAL_ARCH):$(VERSION)

multiarch-image: $(CONFIG_DOCKER_TARGET)  ## Generate multiarch images for operator image.
	@MAX_PULLING_RETRY=20 RETRY_INTERVAL=30 common/scripts/multiarch_image.sh $(ARTIFACTORYA_REGISTRY) $(OPERATOR_IMAGE_NAME) $(VERSION) $(RELEASE_VERSION)

build-bundle-image:
	docker build -f bundle.Dockerfile -t $(QUAY_REGISTRY)/$(BUNDLE_IMAGE_NAME):$(VERSION) .
	docker push $(QUAY_REGISTRY)/$(BUNDLE_IMAGE_NAME):$(VERSION)

build-catalog-source:
	opm -u docker index add --bundles $(QUAY_REGISTRY)/$(BUNDLE_IMAGE_NAME):$(VERSION) --tag $(QUAY_REGISTRY)/$(OPERATOR_IMAGE_NAME)-catalog:$(VERSION)
	docker push $(QUAY_REGISTRY)/$(OPERATOR_IMAGE_NAME)-catalog:$(VERSION)

run-bundle:
	$(OPERATOR_SDK) run bundle $(QUAY_REGISTRY)/$(BUNDLE_IMAGE_NAME):$(VERSION) --install-mode OwnNamespace

cleanup-bundle:
	$(OPERATOR_SDK) cleanup ibm-namespace-scope-operator

##@ Help
help: ## Display this help
	@echo "Usage:\n  make \033[36m<target>\033[0m"
	@awk 'BEGIN {FS = ":.*##"}; \
		/^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
