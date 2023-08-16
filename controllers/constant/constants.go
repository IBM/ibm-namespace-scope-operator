//
// Copyright 2022 IBM Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package constant

const (
	NamespaceScopeManagedPrefix     = "nss-managed-role-from-"
	NamespaceScopeConfigmapName     = "namespace-scope"
	NamespaceScopeFinalizer         = "finalizer.nss.operator.ibm.com"
	NamespaceScopeLabel             = "managedby-namespace-scope"
	DefaultRestartLabelsKey         = "intent"
	DefaultRestartLabelsValue       = "projected"
	NamespaceScopeServiceAccount    = "ibm-namespace-scope-operator"
	InjectorMark                    = "nss.operator.ibm.com/managed-operators"
	WebhookMark                     = "nss.operator.ibm.com/managed-webhooks"
	NamespaceScopeConfigmapLabelKey = "namespace-scope-configmap"
	NamespaceScopeRuntimePrefix     = "nss-runtime-managed-role-from-"
	MatchExpressionsKey             = "kubernetes.io/metadata.name"
)
