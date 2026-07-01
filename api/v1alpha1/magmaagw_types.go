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

// MagmaAGWSpec defines the desired state of MagmaAGW
type MagmaAGWSpec struct {
	// +optional
	ReleaseName string `json:"releaseName,omitempty"`
	// +optional
	ChartRepository string `json:"chartRepository,omitempty"`
	// +optional
	ChartRevision string `json:"chartRevision,omitempty"`
	// +optional
	ChartPath string `json:"chartPath,omitempty"`
	// +optional
	AccessGatewayID string `json:"accessGatewayID,omitempty"`
	// +optional
	NetworkID string `json:"networkID,omitempty"`
	// +optional
	Orc8rNamespace string `json:"orc8rNamespace,omitempty"`
	// +optional
	Orc8rReleaseName string `json:"orc8rReleaseName,omitempty"`
	// +optional
	NMSAPIHost string `json:"nmsAPIHost,omitempty"`
	// +optional
	NMSAdminCertSecretName string `json:"nmsAdminCertSecretName,omitempty"`
	// +optional
	Identity MagmaAGWIdentitySpec `json:"identity,omitempty"`
	// +optional
	GatewayRegistration MagmaAGWGatewayRegistrationSpec `json:"gatewayRegistration,omitempty"`
	// +optional
	DeletionPolicy MagmaAGWDeletionPolicySpec `json:"deletionPolicy,omitempty"`
	// +optional
	AGWNodeSelector map[string]string `json:"agwNodeSelector,omitempty"`
	// +optional
	AGWNodeLabelSelector map[string]string `json:"agwNodeLabelSelector,omitempty"`
	// +optional
	EnableUERANSIM bool `json:"enableUERANSIM,omitempty"`
	// +optional
	// +kubebuilder:validation:Enum=AfterAGWReady;Immediate
	UERANSIMStartPolicy string `json:"ueransimStartPolicy,omitempty"`
	// +optional
	UERANSIMReadyConfigMap string `json:"ueransimReadyConfigMap,omitempty"`
	// +optional
	UERANSIMNodeSelector map[string]string `json:"ueransimNodeSelector,omitempty"`
	// +optional
	UERANSIMValidation MagmaAGWUERANSIMValidationSpec `json:"ueransimValidation,omitempty"`
	// +optional
	S1Interface string `json:"s1Interface,omitempty"`
	// +optional
	SGiInterface string `json:"sgiInterface,omitempty"`
	// +optional
	Datapath MagmaAGWDatapathSpec `json:"datapath,omitempty"`
	// +optional
	Values map[string]string `json:"values,omitempty"`
}

// MagmaAGWIdentitySpec defines the operator-managed AGW bootstrap identity.
type MagmaAGWIdentitySpec struct {
	// +optional
	SecretName string `json:"secretName,omitempty"`
	// +optional
	ImportSecretName string `json:"importSecretName,omitempty"`
	// +optional
	ImportSecretKey string `json:"importSecretKey,omitempty"`
	// +optional
	HardwareID string `json:"hardwareID,omitempty"`
	// +optional
	// +kubebuilder:validation:Enum=Never;Rotate
	RotationPolicy string `json:"rotationPolicy,omitempty"`
}

// MagmaAGWGatewayRegistrationSpec defines Orc8r/NMS gateway registration intent.
type MagmaAGWGatewayRegistrationSpec struct {
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// +optional
	DeleteOnRemoval bool `json:"deleteOnRemoval,omitempty"`
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	Description string `json:"description,omitempty"`
	// +optional
	Tier string `json:"tier,omitempty"`
}

// MagmaAGWDeletionPolicySpec defines opt-in cleanup for durable AGW state.
type MagmaAGWDeletionPolicySpec struct {
	// +optional
	DeletePVC bool `json:"deletePVC,omitempty"`
	// +optional
	DeleteIdentitySecret bool `json:"deleteIdentitySecret,omitempty"`
}

// MagmaAGWUERANSIMValidationSpec defines an optional one-shot simulator validation.
type MagmaAGWUERANSIMValidationSpec struct {
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// +optional
	Trigger string `json:"trigger,omitempty"`
	// +optional
	UEDeploymentName string `json:"ueDeploymentName,omitempty"`
	// +optional
	PingHost string `json:"pingHost,omitempty"`
	// +optional
	IPerfServer string `json:"iperfServer,omitempty"`
	// +optional
	IPerfPort int32 `json:"iperfPort,omitempty"`
	// +optional
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
}

// MagmaAGWDatapathSpec defines AGW host datapath node-prep gating.
type MagmaAGWDatapathSpec struct {
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// +optional
	ReadyLabelKey string `json:"readyLabelKey,omitempty"`
	// +optional
	ReadyLabelValue string `json:"readyLabelValue,omitempty"`
	// +optional
	RequireMagmaOvsKmod bool `json:"requireMagmaOvsKmod,omitempty"`
	// +optional
	OvsKmodUpgradePath string `json:"ovsKmodUpgradePath,omitempty"`
}

// MagmaAGWStatus defines the observed state of MagmaAGW.
type MagmaAGWStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	ReleaseName string `json:"releaseName,omitempty"`
	// +optional
	ReleaseNamespace string `json:"releaseNamespace,omitempty"`
	// +optional
	IdentitySecretName string `json:"identitySecretName,omitempty"`
	// +optional
	HardwareID string `json:"hardwareID,omitempty"`
	// +optional
	ChallengePublicKeyHash string `json:"challengePublicKeyHash,omitempty"`
	// +optional
	GatewayRegistered bool `json:"gatewayRegistered,omitempty"`
	// +optional
	Orc8rServiceIP string `json:"orc8rServiceIP,omitempty"`
	// +optional
	TrustBundleHash string `json:"trustBundleHash,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// MagmaAGW is the Schema for the magmaagws API
type MagmaAGW struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of MagmaAGW
	// +required
	Spec MagmaAGWSpec `json:"spec"`

	// status defines the observed state of MagmaAGW
	// +optional
	Status MagmaAGWStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MagmaAGWList contains a list of MagmaAGW
type MagmaAGWList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MagmaAGW `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &MagmaAGW{}, &MagmaAGWList{})
		return nil
	})
}
