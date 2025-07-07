/*
Copyright 2025 mellifluus.

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

package v1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TenantEnvironmentSpec defines the desired state of TenantEnvironment
type TenantEnvironmentSpec struct {
	// DisplayName is a human-readable name for the tenant
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	DisplayName string `json:"displayName"`

	// Replicas specifies the number of application replicas
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	Replicas int32 `json:"replicas,omitempty"`

	// ServiceVersion specifies the version of the tenant service to deploy
	// +kubebuilder:default="latest"
	ServiceVersion string `json:"serviceVersion,omitempty"`

	// ResourceQuotas defines resource limits for the tenant environment
	// +kubebuilder:default={"cpuLimit":"2","memoryLimit":"4Gi","storageLimit":"10Gi","podLimit":10}
	ResourceQuotas *TenantResourceQuotas `json:"resourceQuotas,omitempty"`

	// Database configuration for the tenant
	// +kubebuilder:default={"dedicatedInstance": false}
	Database TenantDatabaseConfig `json:"database"`
}

// TenantResourceQuotas defines resource limits for a tenant environment
type TenantResourceQuotas struct {
	// CPU limit for the tenant namespace
	// +kubebuilder:default="2"
	CPULimit resource.Quantity `json:"cpuLimit,omitempty"`

	// Memory limit for the tenant namespace
	// +kubebuilder:default="4Gi"
	MemoryLimit resource.Quantity `json:"memoryLimit,omitempty"`

	// Storage limit for the tenant namespace
	// +kubebuilder:default="10Gi"
	StorageLimit resource.Quantity `json:"storageLimit,omitempty"`

	// Maximum number of pods in the tenant namespace
	// +kubebuilder:default=10
	PodLimit int32 `json:"podLimit,omitempty"`
}

// TenantDatabaseConfig defines simplified PostgreSQL database configuration for a tenant
type TenantDatabaseConfig struct {
	// Whether to create a dedicated PostgreSQL instance or use shared instance with separate database (default)
	// +kubebuilder:default=false
	DedicatedInstance bool `json:"dedicatedInstance,omitempty"`
}

// TenantEnvironmentStatus defines the observed state of TenantEnvironment
type TenantEnvironmentStatus struct {
	// Phase represents the current phase of the tenant environment
	// +kubebuilder:default="Pending"
	// +kubebuilder:validation:Enum=Pending;Ready
	Phase string `json:"phase,omitempty"`

	// Message provides human-readable details about the current state
	Message string `json:"message,omitempty"`

	// Namespace where the tenant environment is deployed
	Namespace string `json:"namespace,omitempty"`

	// CreatedAt timestamp when the tenant was created
	CreatedAt *metav1.Time `json:"createdAt,omitempty"`

	// ReadyAt timestamp when the tenant became ready
	ReadyAt *metav1.Time `json:"readyAt,omitempty"`

	// Database status indicates whether the database has been assigned/provisioned
	// +kubebuilder:default="Unassigned"
	// +kubebuilder:validation:Enum=Unassigned;Provisioning;Assigned
	DatabaseStatus string `json:"databaseStatus,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.status.namespace`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TenantEnvironment is the Schema for the tenantenvironments API.
type TenantEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TenantEnvironmentSpec   `json:"spec,omitempty"`
	Status TenantEnvironmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TenantEnvironmentList contains a list of TenantEnvironment.
type TenantEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TenantEnvironment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TenantEnvironment{}, &TenantEnvironmentList{})
}
