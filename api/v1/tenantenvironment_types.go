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
	corev1 "k8s.io/api/core/v1"
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

	// ApplicationImage specifies the container image for the tenant's application
	// +kubebuilder:validation:Required
	ApplicationImage string `json:"applicationImage"`

	// ApplicationPort specifies the port the application listens on
	// +kubebuilder:default=8080
	ApplicationPort int32 `json:"applicationPort,omitempty"`

	// Replicas specifies the number of application replicas
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	Replicas int32 `json:"replicas,omitempty"`

	// ResourceQuotas defines resource limits for the tenant environment
	ResourceQuotas *TenantResourceQuotas `json:"resourceQuotas,omitempty"`

	// Database configuration for the tenant (required)
	// +kubebuilder:validation:Required
	Database TenantDatabaseConfig `json:"database"`

	// NetworkPolicy defines network isolation rules
	// +kubebuilder:default=true
	NetworkIsolation bool `json:"networkIsolation,omitempty"`

	// Environment variables for the tenant application
	Environment []corev1.EnvVar `json:"environment,omitempty"`
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

// TenantDatabaseConfig defines PostgreSQL database configuration for a tenant
type TenantDatabaseConfig struct {
	// Whether to create a dedicated PostgreSQL instance (enterprise) or use shared instance with separate database (default)
	// +kubebuilder:default=false
	DedicatedInstance bool `json:"dedicatedInstance,omitempty"`

	// Storage size for the PostgreSQL database (only applies to dedicated instances)
	// +kubebuilder:default="20Gi"
	StorageSize resource.Quantity `json:"storageSize,omitempty"`

	// Database name for the tenant (defaults to tenant-{tenantId})
	// For shared: creates new database with this name in shared instance
	// For dedicated: creates database with this name in dedicated instance
	DatabaseName string `json:"databaseName,omitempty"`

	// PostgreSQL version to use (only applies to dedicated instances)
	// +kubebuilder:default="15"
	// +kubebuilder:validation:Enum="13";"14";"15";"16"
	Version string `json:"version,omitempty"`

	// Performance tier for database resources
	// +kubebuilder:default="standard"
	// +kubebuilder:validation:Enum=standard;premium;enterprise
	PerformanceTier string `json:"performanceTier,omitempty"`
}

// TenantEnvironmentStatus defines the observed state of TenantEnvironment
type TenantEnvironmentStatus struct {
	// Phase represents the current phase of the tenant environment
	// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Failed;Terminating
	Phase string `json:"phase,omitempty"`

	// Message provides human-readable details about the current state
	Message string `json:"message,omitempty"`

	// Namespace where the tenant environment is deployed
	Namespace string `json:"namespace,omitempty"`

	// Endpoint where the tenant application is accessible
	Endpoint string `json:"endpoint,omitempty"`

	// DatabaseConnection contains database connection information
	DatabaseConnection *DatabaseConnectionStatus `json:"databaseConnection,omitempty"`

	// Conditions represent the latest available observations of the environment's state
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastReconcileTime is the last time the environment was reconciled
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`
}

// DatabaseConnectionStatus contains database connection information
type DatabaseConnectionStatus struct {
	// Host is the database host
	Host string `json:"host,omitempty"`

	// Port is the database port
	Port int32 `json:"port,omitempty"`

	// DatabaseName is the name of the database
	DatabaseName string `json:"databaseName,omitempty"`

	// SecretName contains the secret with database credentials
	SecretName string `json:"secretName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

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
