/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// MagmaOrc8rSpec defines the desired state of MagmaOrc8r
type MagmaOrc8rSpec struct {
	// +optional
	ReleaseName string `json:"releaseName,omitempty"`
	// +optional
	ChartRepository string `json:"chartRepository,omitempty"`
	// +optional
	ChartRevision string `json:"chartRevision,omitempty"`
	// +optional
	ChartPath string `json:"chartPath,omitempty"`
	// +optional
	DomainName string `json:"domainName,omitempty"`
	// +optional
	ControllerHostname string `json:"controllerHostname,omitempty"`
	// +optional
	NMSNodePort *int32 `json:"nmsNodePort,omitempty"`
	// +optional
	NMSAdminEmail string `json:"nmsAdminEmail,omitempty"`
	// +optional
	NMSAdminPassword string `json:"nmsAdminPassword,omitempty"`
	// +optional
	Values map[string]string `json:"values,omitempty"`
}

// MagmaOrc8rStatus defines the observed state of MagmaOrc8r.
type MagmaOrc8rStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	ReleaseName string `json:"releaseName,omitempty"`
	// +optional
	ReleaseNamespace string `json:"releaseNamespace,omitempty"`
	// +optional
	NMSURL string `json:"nmsUrl,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// MagmaOrc8r is the Schema for the magmaorc8rs API
type MagmaOrc8r struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of MagmaOrc8r
	// +required
	Spec MagmaOrc8rSpec `json:"spec"`

	// status defines the observed state of MagmaOrc8r
	// +optional
	Status MagmaOrc8rStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MagmaOrc8rList contains a list of MagmaOrc8r
type MagmaOrc8rList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MagmaOrc8r `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &MagmaOrc8r{}, &MagmaOrc8rList{})
		return nil
	})
}
