//
// Copyright 2020 IBM Corporation
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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// NamespaceScopeSpec defines the desired state of NamespaceScope
type NamespaceScopeSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Namespaces that are part of this scope
	NamespaceMembers []string `json:"namespaceMembers,omitempty"`

	// ServiceAccountMembers are extra service accounts will be bond the roles from other namespaces
	ServiceAccountMembers []string `json:"serviceAccountMembers,omitempty"`

	// ConfigMap name that will contain the list of namespaces to be watched
	ConfigmapName string `json:"configmapName,omitempty"`

	// Restart pods with the following labels when the namspace list changes
	RestartLabels map[string]string `json:"restartLabels,omitempty"`

	// Set the following to true to manaually manage permissions for the NamespaceScope operator to extend control over other namespaces
	// The operator may fail when trying to extend permissions to other namespaces, but the cluster administrator can correct this using the
	// authorize-namespace command.
	ManualManagement bool `json:"manualManagement,omitempty"`
}

// NamespaceScopeStatus defines the observed state of NamespaceScope
type NamespaceScopeStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=namespacescopes,shortName=nss,scope=Namespaced

// NamespaceScope is the Schema for the namespacescopes API
type NamespaceScope struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NamespaceScopeSpec   `json:"spec,omitempty"`
	Status NamespaceScopeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NamespaceScopeList contains a list of NamespaceScope
type NamespaceScopeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NamespaceScope `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NamespaceScope{}, &NamespaceScopeList{})
}
