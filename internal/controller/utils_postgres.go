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

package controller

import (
	"context"
	"embed"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	tenantv1 "github.com/mellifluus/operator-demo.git/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//go:embed configs/*
var postgresqlConfigs embed.FS

const (
	// Maximum number of tenants per shared PostgreSQL instance
	MaxTenantsPerSharedInstance = 20

	// Label to track tenant count in shared instances
	SharedInstanceTenantCountLabel = "tenant.core.mellifluus.io/tenant-count"
)

// CreatePostgreSQLForTenant creates PostgreSQL database resources for a tenant
func CreatePostgreSQLForTenant(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, log logr.Logger) error {
	if tenantEnv.Spec.Database.DedicatedInstance {
		return createDedicatedPostgreSQL(ctx, c, tenantEnv, log)
	}
	return createSharedPostgreSQLEntry(ctx, c, tenantEnv, log)
}

// createDedicatedPostgreSQL creates a dedicated PostgreSQL instance for enterprise tenants
func createDedicatedPostgreSQL(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, log logr.Logger) error {
	namespaceName := "tenant-" + string(tenantEnv.UID)

	// Create PostgreSQL Secret with credentials
	if err := createPostgreSQLSecret(ctx, c, namespaceName, "postgresql", log); err != nil {
		return err
	}

	// Create PostgreSQL StatefulSet
	if err := createPostgreSQLStatefulSet(ctx, c, tenantEnv, namespaceName, log); err != nil {
		return err
	}

	// Create PostgreSQL Service
	if err := createPostgreSQLService(ctx, c, tenantEnv, namespaceName, log); err != nil {
		return err
	}

	// Copy PostgreSQL ConfigMap from shared-services to tenant namespace
	if err := copyPostgreSQLConfigMapToNamespace(ctx, c, namespaceName, log); err != nil {
		return err
	}

	return nil
}

// createSharedPostgreSQLEntry creates a database entry in the shared PostgreSQL instance
func createSharedPostgreSQLEntry(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, log logr.Logger) error {
	// 1. Find or create an available shared PostgreSQL instance
	sharedInstanceName, err := findOrCreateAvailableSharedInstance(ctx, c, log)
	if err != nil {
		return err
	}

	// 2. Create secret with tenant database credentials
	if err := createSharedPostgreSQLSecret(ctx, c, tenantEnv, sharedInstanceName, log); err != nil {
		return err
	}

	// 3. Create ConfigMap with connection info
	if err := createSharedPostgreSQLConfig(ctx, c, tenantEnv, sharedInstanceName, log); err != nil {
		return err
	}

	// 4. Update tenant count for the shared instance (only once per tenant)
	if err := addTenantToSharedInstance(ctx, c, tenantEnv, sharedInstanceName, log); err != nil {
		return err
	}

	// 5. Create database initialization Job
	if err := createDatabaseInitJob(ctx, c, tenantEnv, sharedInstanceName, log); err != nil {
		return err
	}

	return nil
}

// createPostgreSQLSecret creates a master secret for PostgreSQL instance
func createPostgreSQLSecret(ctx context.Context, c client.Client, namespace, instanceName string, log logr.Logger) error {
	secretName := instanceName + "-master-secret"

	var secret corev1.Secret
	err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, &secret)

	if errors.IsNotFound(err) {
		// Generate secure random password
		password, err := generatePassword(32)
		if err != nil {
			return fmt.Errorf("failed to generate secure password: %w", err)
		}

		secret = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
				Labels: map[string]string{
					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
					"app":                                  "postgresql",
					"instance":                             instanceName,
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"POSTGRES_DB":       []byte("postgres"),
				"POSTGRES_USER":     []byte("postgres"),
				"POSTGRES_PASSWORD": []byte(password),
			},
		}

		if err := c.Create(ctx, &secret); err != nil {
			return err
		}
		log.Info("Created PostgreSQL master secret", "namespace", namespace, "instance", instanceName, "secret", secretName)
	} else if err != nil {
		return err
	}

	return nil
}

// createPostgreSQLStatefulSet creates a PostgreSQL StatefulSet for dedicated instances
func createPostgreSQLStatefulSet(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, namespace string, log logr.Logger) error {
	statefulSetName := "postgresql"

	var statefulSet appsv1.StatefulSet
	err := c.Get(ctx, types.NamespacedName{Name: statefulSetName, Namespace: namespace}, &statefulSet)

	if errors.IsNotFound(err) {
		version := tenantEnv.Spec.Database.Version
		if version == "" {
			version = "15"
		}

		storageSize := tenantEnv.Spec.Database.StorageSize
		if storageSize.IsZero() {
			storageSize = resource.MustParse("20Gi")
		}

		// Determine resource limits based on performance tier
		cpuLimit := "500m"
		memoryLimit := "1Gi"

		switch tenantEnv.Spec.Database.PerformanceTier {
		case "premium":
			cpuLimit = "1"
			memoryLimit = "2Gi"
		case "enterprise":
			cpuLimit = "2"
			memoryLimit = "4Gi"
		}

		statefulSet = appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      statefulSetName,
				Namespace: namespace,
				Labels: map[string]string{
					"tenant.core.mellifluus.io/tenant-uid": string(tenantEnv.UID),
					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
					"app":                                  "postgresql",
				},
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: int32Ptr(1),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": "postgresql",
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "postgresql",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "postgresql",
								Image: "postgres:" + version,
								EnvFrom: []corev1.EnvFromSource{
									{
										SecretRef: &corev1.SecretEnvSource{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: "postgresql-master-secret",
											},
										},
									},
								},
								Ports: []corev1.ContainerPort{
									{
										ContainerPort: 5432,
										Name:          "postgresql",
									},
								},
								Resources: corev1.ResourceRequirements{
									Limits: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse(cpuLimit),
										corev1.ResourceMemory: resource.MustParse(memoryLimit),
									},
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("250m"),
										corev1.ResourceMemory: resource.MustParse("512Mi"),
									},
								},
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "postgresql-storage",
										MountPath: "/var/lib/postgresql/data",
									},
									{
										Name:      "postgresql-config",
										MountPath: "/etc/postgresql/postgresql.conf",
										SubPath:   "postgresql.conf",
										ReadOnly:  true,
									},
									{
										Name:      "postgresql-config",
										MountPath: "/etc/postgresql/pg_hba.conf",
										SubPath:   "pg_hba.conf",
										ReadOnly:  true,
									},
								},
								Command: []string{
									"docker-entrypoint.sh",
									"postgres",
									"-c", "config_file=/etc/postgresql/postgresql.conf",
									"-c", "hba_file=/etc/postgresql/pg_hba.conf",
								},
							},
						},
						Volumes: []corev1.Volume{
							{
								Name: "postgresql-config",
								VolumeSource: corev1.VolumeSource{
									ConfigMap: &corev1.ConfigMapVolumeSource{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "postgresql-config",
										},
									},
								},
							},
						},
					},
				},
				VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "postgresql-storage",
						},
						Spec: corev1.PersistentVolumeClaimSpec{
							AccessModes: []corev1.PersistentVolumeAccessMode{
								corev1.ReadWriteOnce,
							},
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceStorage: storageSize,
								},
							},
						},
					},
				},
			},
		}

		if err := c.Create(ctx, &statefulSet); err != nil {
			return err
		}
		log.Info("Created PostgreSQL StatefulSet", "namespace", namespace, "statefulset", statefulSetName)
	} else if err != nil {
		return err
	}

	return nil
}

// createPostgreSQLService creates a service for PostgreSQL
func createPostgreSQLService(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, namespace string, log logr.Logger) error {
	serviceName := "postgresql-service"

	var service corev1.Service
	err := c.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: namespace}, &service)

	if errors.IsNotFound(err) {
		service = corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serviceName,
				Namespace: namespace,
				Labels: map[string]string{
					"tenant.core.mellifluus.io/tenant-uid": string(tenantEnv.UID),
					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
					"app":                                  "postgresql",
				},
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{
					"app": "postgresql",
				},
				Ports: []corev1.ServicePort{
					{
						Port:       5432,
						TargetPort: intstr.FromInt(5432),
						Protocol:   corev1.ProtocolTCP,
						Name:       "postgresql",
					},
				},
				Type: corev1.ServiceTypeClusterIP,
			},
		}

		if err := c.Create(ctx, &service); err != nil {
			return err
		}
		log.Info("Created PostgreSQL service", "namespace", namespace, "service", serviceName)
	} else if err != nil {
		return err
	}

	return nil
}

// findOrCreateAvailableSharedInstance finds a shared instance with available capacity or creates a new one
func findOrCreateAvailableSharedInstance(ctx context.Context, c client.Client, log logr.Logger) (string, error) {
	sharedNamespace := "shared-services"

	// Ensure shared-services namespace exists
	if err := ensureSharedServicesNamespace(ctx, c, log); err != nil {
		return "", err
	}

	// List all shared PostgreSQL StatefulSets
	var statefulSets appsv1.StatefulSetList
	listOpts := []client.ListOption{
		client.InNamespace(sharedNamespace),
		client.MatchingLabels{"app": "shared-postgresql"},
	}

	if err := c.List(ctx, &statefulSets, listOpts...); err != nil {
		return "", err
	}

	// Find an instance with available capacity
	for _, ss := range statefulSets.Items {
		tenantCountStr, exists := ss.Labels[SharedInstanceTenantCountLabel]
		if !exists {
			tenantCountStr = "0"
		}

		tenantCount, err := strconv.Atoi(tenantCountStr)
		if err != nil {
			log.Error(err, "Invalid tenant count label", "statefulset", ss.Name)
			continue
		}

		if tenantCount < MaxTenantsPerSharedInstance {
			log.Info("Found available shared instance", "instance", ss.Name, "currentTenants", tenantCount)
			return ss.Name, nil
		}
	}

	// No available instances found, create a new one
	newInstanceName := fmt.Sprintf("shared-postgresql-%d", len(statefulSets.Items))
	log.Info("Creating new shared PostgreSQL instance", "instance", newInstanceName, "reason", "capacity limit reached")

	if err := createSharedPostgreSQLInstance(ctx, c, sharedNamespace, newInstanceName, log); err != nil {
		return "", err
	}

	return newInstanceName, nil
}

// createSharedPostgreSQLInstance creates a complete shared PostgreSQL instance
func createSharedPostgreSQLInstance(ctx context.Context, c client.Client, namespace, instanceName string, log logr.Logger) error {
	// Create master secret
	if err := createPostgreSQLSecret(ctx, c, namespace, instanceName, log); err != nil {
		return err
	}

	// ConfigMap already exists in shared-services namespace from startup
	// No need to create it again - the StatefulSet will reference the shared one

	// Create StatefulSet (it will reference the shared postgresql-config ConfigMap)
	if err := createSharedPostgreSQLStatefulSet(ctx, c, namespace, instanceName, log); err != nil {
		return err
	}

	// Create Service
	if err := createSharedPostgreSQLService(ctx, c, namespace, instanceName, log); err != nil {
		return err
	}

	return nil
}

// addTenantToSharedInstance adds a tenant to a shared instance's tenant list (idempotent)
func addTenantToSharedInstance(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, instanceName string, log logr.Logger) error {
	sharedNamespace := "shared-services"
	tenantUID := string(tenantEnv.UID)

	var statefulSet appsv1.StatefulSet
	err := c.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: sharedNamespace}, &statefulSet)
	if err != nil {
		return err
	}

	// Get current tenant list from annotations
	if statefulSet.Annotations == nil {
		statefulSet.Annotations = make(map[string]string)
	}

	tenantListStr, exists := statefulSet.Annotations["tenant.core.mellifluus.io/tenant-list"]
	if !exists {
		tenantListStr = ""
	}

	// Parse existing tenant list
	var tenantList []string
	if tenantListStr != "" {
		tenantList = strings.Split(tenantListStr, ",")
	}

	// Check if this tenant is already in the list
	for _, existingTenant := range tenantList {
		if existingTenant == tenantUID {
			log.Info("Tenant already registered to shared instance", "instance", instanceName, "tenant", tenantUID)
			return nil // Already exists, nothing to do
		}
	}

	// Add the new tenant to the list
	tenantList = append(tenantList, tenantUID)

	// Update annotations and labels
	statefulSet.Annotations["tenant.core.mellifluus.io/tenant-list"] = strings.Join(tenantList, ",")
	if statefulSet.Labels == nil {
		statefulSet.Labels = make(map[string]string)
	}
	statefulSet.Labels[SharedInstanceTenantCountLabel] = strconv.Itoa(len(tenantList))

	if err := c.Update(ctx, &statefulSet); err != nil {
		return err
	}

	log.Info("Added tenant to shared instance", "instance", instanceName, "tenant", tenantUID, "tenantCount", len(tenantList))
	return nil
}

// removeTenantFromSharedInstance removes a tenant from a shared instance's tenant list
func removeTenantFromSharedInstance(ctx context.Context, c client.Client, tenantUID string, log logr.Logger) error {
	sharedNamespace := "shared-services"

	// List all shared PostgreSQL StatefulSets to find which one contains this tenant
	var statefulSets appsv1.StatefulSetList
	listOpts := []client.ListOption{
		client.InNamespace(sharedNamespace),
		client.MatchingLabels{"app": "shared-postgresql"},
	}

	if err := c.List(ctx, &statefulSets, listOpts...); err != nil {
		return err
	}

	for _, ss := range statefulSets.Items {
		// Check if this tenant is in this instance's tenant list
		if ss.Annotations == nil {
			continue
		}

		tenantListStr, exists := ss.Annotations["tenant.core.mellifluus.io/tenant-list"]
		if !exists || tenantListStr == "" {
			continue
		}

		tenantList := strings.Split(tenantListStr, ",")
		tenantFound := false
		newTenantList := []string{}

		// Remove the tenant from the list
		for _, existingTenant := range tenantList {
			if existingTenant != tenantUID {
				newTenantList = append(newTenantList, existingTenant)
			} else {
				tenantFound = true
			}
		}

		if tenantFound {
			// Update the StatefulSet with the new tenant list
			ss.Annotations["tenant.core.mellifluus.io/tenant-list"] = strings.Join(newTenantList, ",")
			if ss.Labels == nil {
				ss.Labels = make(map[string]string)
			}
			ss.Labels[SharedInstanceTenantCountLabel] = strconv.Itoa(len(newTenantList))

			if err := c.Update(ctx, &ss); err != nil {
				return err
			}

			log.Info("Removed tenant from shared instance", "instance", ss.Name, "tenant", tenantUID, "remainingTenants", len(newTenantList))
			return nil
		}
	}

	log.Info("Tenant not found in any shared instance", "tenant", tenantUID)
	return nil
}

// createSharedPostgreSQLStatefulSet creates the shared PostgreSQL StatefulSet
func createSharedPostgreSQLStatefulSet(ctx context.Context, c client.Client, namespace, instanceName string, log logr.Logger) error {
	var statefulSet appsv1.StatefulSet
	err := c.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: namespace}, &statefulSet)

	if errors.IsNotFound(err) {
		statefulSet = appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      instanceName,
				Namespace: namespace,
				Labels: map[string]string{
					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
					"app":                                  "shared-postgresql",
					"tier":                                 "shared",
					"instance":                             instanceName,
					SharedInstanceTenantCountLabel:         "0", // Initialize tenant count
				},
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: int32Ptr(1),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app":      "shared-postgresql",
						"instance": instanceName,
					},
				},
				ServiceName: instanceName,
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app":      "shared-postgresql",
							"instance": instanceName,
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "postgresql",
								Image: "postgres:15",
								EnvFrom: []corev1.EnvFromSource{
									{
										SecretRef: &corev1.SecretEnvSource{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: instanceName + "-master",
											},
										},
									},
								},
								Ports: []corev1.ContainerPort{
									{
										ContainerPort: 5432,
										Name:          "postgresql",
									},
								},
								Resources: corev1.ResourceRequirements{
									Limits: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("1"),
										corev1.ResourceMemory: resource.MustParse("2Gi"),
									},
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("500m"),
										corev1.ResourceMemory: resource.MustParse("1Gi"),
									},
								},
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "postgresql-storage",
										MountPath: "/var/lib/postgresql/data",
									},
									{
										Name:      "postgresql-config",
										MountPath: "/etc/postgresql/postgresql.conf",
										SubPath:   "postgresql.conf",
										ReadOnly:  true,
									},
									{
										Name:      "postgresql-config",
										MountPath: "/etc/postgresql/pg_hba.conf",
										SubPath:   "pg_hba.conf",
										ReadOnly:  true,
									},
								},
								Command: []string{
									"docker-entrypoint.sh",
									"postgres",
									"-c", "config_file=/etc/postgresql/postgresql.conf",
									"-c", "hba_file=/etc/postgresql/pg_hba.conf",
								},
							},
						},
						Volumes: []corev1.Volume{
							{
								Name: "postgresql-config",
								VolumeSource: corev1.VolumeSource{
									ConfigMap: &corev1.ConfigMapVolumeSource{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "postgresql-config",
										},
									},
								},
							},
						},
					},
				},
				VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "postgresql-storage",
						},
						Spec: corev1.PersistentVolumeClaimSpec{
							AccessModes: []corev1.PersistentVolumeAccessMode{
								corev1.ReadWriteOnce,
							},
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceStorage: resource.MustParse("50Gi"), // Shared storage size
								},
							},
						},
					},
				},
			},
		}

		if err := c.Create(ctx, &statefulSet); err != nil {
			return err
		}
		log.Info("Created shared PostgreSQL StatefulSet", "namespace", namespace, "instance", instanceName)
	} else if err != nil {
		return err
	}

	return nil
}

// createSharedPostgreSQLService creates the service for shared PostgreSQL
func createSharedPostgreSQLService(ctx context.Context, c client.Client, namespace, instanceName string, log logr.Logger) error {
	var service corev1.Service
	err := c.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: namespace}, &service)

	if errors.IsNotFound(err) {
		service = corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      instanceName,
				Namespace: namespace,
				Labels: map[string]string{
					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
					"app":                                  "shared-postgresql",
					"instance":                             instanceName,
				},
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{
					"app":      "shared-postgresql",
					"instance": instanceName,
				},
				Ports: []corev1.ServicePort{
					{
						Port:       5432,
						TargetPort: intstr.FromInt(5432),
						Protocol:   corev1.ProtocolTCP,
						Name:       "postgresql",
					},
				},
				Type: corev1.ServiceTypeClusterIP,
			},
		}

		if err := c.Create(ctx, &service); err != nil {
			return err
		}
		log.Info("Created shared PostgreSQL service", "namespace", namespace, "instance", instanceName)
	} else if err != nil {
		return err
	}

	return nil
}

// createSharedPostgreSQLSecret creates tenant-specific credentials for shared PostgreSQL
func createSharedPostgreSQLSecret(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, instanceName string, log logr.Logger) error {
	namespaceName := "tenant-" + string(tenantEnv.UID)
	secretName := "database-secret"

	var secret corev1.Secret
	err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespaceName}, &secret)

	if errors.IsNotFound(err) {
		databaseName := tenantEnv.Spec.Database.DatabaseName
		if databaseName == "" {
			databaseName = "tenant_" + string(tenantEnv.UID)[:8]
		}

		tenantUsername := "tenant_" + string(tenantEnv.UID)[:8]

		// Generate secure random password for tenant
		tenantPassword, err := generatePassword(20)
		if err != nil {
			return fmt.Errorf("failed to generate secure tenant password: %w", err)
		}

		secret = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespaceName,
				Labels: map[string]string{
					"tenant.core.mellifluus.io/tenant-uid": string(tenantEnv.UID),
					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"DB_HOST":     []byte(instanceName + ".shared-services.svc.cluster.local"),
				"DB_PORT":     []byte("5432"),
				"DB_NAME":     []byte(databaseName),
				"DB_USERNAME": []byte(tenantUsername),
				"DB_PASSWORD": []byte(tenantPassword),
				"DB_TYPE":     []byte("shared"),
			},
		}

		if err := c.Create(ctx, &secret); err != nil {
			return err
		}
		log.Info("Created shared database secret", "namespace", namespaceName, "database", databaseName, "instance", instanceName)
	} else if err != nil {
		return err
	}

	return nil
}

// createSharedPostgreSQLConfig creates a ConfigMap with non-sensitive database connection info
func createSharedPostgreSQLConfig(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, instanceName string, log logr.Logger) error {
	namespaceName := "tenant-" + string(tenantEnv.UID)
	configMapName := "database-config"

	var configMap corev1.ConfigMap
	err := c.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespaceName}, &configMap)

	if errors.IsNotFound(err) {
		databaseName := tenantEnv.Spec.Database.DatabaseName
		if databaseName == "" {
			databaseName = "tenant_" + string(tenantEnv.UID)[:8]
		}

		configMap = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: namespaceName,
				Labels: map[string]string{
					"tenant.core.mellifluus.io/tenant-uid": string(tenantEnv.UID),
					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
				},
			},
			Data: map[string]string{
				"DB_HOST":     instanceName + ".shared-services.svc.cluster.local",
				"DB_PORT":     "5432",
				"DB_NAME":     databaseName,
				"DB_TYPE":     "shared",
				"DB_SSL_MODE": "disable", // Configure as needed
			},
		}

		if err := c.Create(ctx, &configMap); err != nil {
			return err
		}
		log.Info("Created shared database config", "namespace", namespaceName, "database", databaseName, "instance", instanceName)
	} else if err != nil {
		return err
	}

	return nil
}

// createDatabaseInitJob creates a Kubernetes Job to initialize database and user in shared PostgreSQL
func createDatabaseInitJob(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, instanceName string, log logr.Logger) error {
	sharedNamespace := "shared-services"
	jobName := "db-init-" + string(tenantEnv.UID)[:8]

	// Check if job already exists or completed
	var job batchv1.Job
	err := c.Get(ctx, types.NamespacedName{Name: jobName, Namespace: sharedNamespace}, &job)
	if err == nil {
		// Job exists, check if it completed successfully
		for _, condition := range job.Status.Conditions {
			if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
				log.Info("Database initialization job already completed", "job", jobName)
				return nil
			}
		}
		// Job exists but not completed, let it continue
		return nil
	} else if !errors.IsNotFound(err) {
		return err
	}

	// Get database details from tenant spec
	databaseName := tenantEnv.Spec.Database.DatabaseName
	if databaseName == "" {
		databaseName = "tenant_" + string(tenantEnv.UID)[:8]
	}
	tenantUsername := "tenant_" + string(tenantEnv.UID)[:8]

	// Get the master secret (we'll reference it directly in the job)
	var masterSecret corev1.Secret
	err = c.Get(ctx, types.NamespacedName{Name: instanceName + "-master", Namespace: "shared-services"}, &masterSecret)
	if err != nil {
		return fmt.Errorf("failed to get master secret for init job: %w", err)
	}

	// Get the generated tenant password from the database secret in tenant namespace
	tenantNamespace := "tenant-" + string(tenantEnv.UID)
	var dbSecret corev1.Secret
	err = c.Get(ctx, types.NamespacedName{Name: "database-secret", Namespace: tenantNamespace}, &dbSecret)
	if err != nil {
		return fmt.Errorf("failed to get database secret for init job: %w", err)
	}

	tenantPassword := string(dbSecret.Data["DB_PASSWORD"])

	// Create SQL init script that uses environment variables
	sqlScript := fmt.Sprintf(`#!/bin/bash
set -e

echo "Waiting for PostgreSQL to be ready..."
until pg_isready -h %s.shared-services.svc.cluster.local -p 5432 -U postgres; do
  echo "PostgreSQL is unavailable - sleeping"
  sleep 2
done

echo "PostgreSQL is ready - creating database and user..."

# Create database if it doesn't exist
PGPASSWORD="$POSTGRES_PASSWORD" psql -h %s.shared-services.svc.cluster.local -p 5432 -U postgres -tc "SELECT 1 FROM pg_database WHERE datname = '%s'" | grep -q 1 || PGPASSWORD="$POSTGRES_PASSWORD" psql -h %s.shared-services.svc.cluster.local -p 5432 -U postgres -c "CREATE DATABASE \"%s\";"

# Create user if it doesn't exist (using TENANT_PASSWORD environment variable)
PGPASSWORD="$POSTGRES_PASSWORD" psql -h %s.shared-services.svc.cluster.local -p 5432 -U postgres -tc "SELECT 1 FROM pg_roles WHERE rolname='%s'" | grep -q 1 || PGPASSWORD="$POSTGRES_PASSWORD" psql -h %s.shared-services.svc.cluster.local -p 5432 -U postgres -c "CREATE USER \"%s\" WITH PASSWORD '$TENANT_PASSWORD';"

# Grant database-level privileges
PGPASSWORD="$POSTGRES_PASSWORD" psql -h %s.shared-services.svc.cluster.local -p 5432 -U postgres -c "GRANT ALL PRIVILEGES ON DATABASE \"%s\" TO \"%s\";"

# Grant schema-level privileges (connect to the tenant database)
PGPASSWORD="$POSTGRES_PASSWORD" psql -h %s.shared-services.svc.cluster.local -p 5432 -U postgres -d "%s" -c "GRANT USAGE, CREATE ON SCHEMA public TO \"%s\";"

# Grant privileges on all existing tables and sequences (if any)
PGPASSWORD="$POSTGRES_PASSWORD" psql -h %s.shared-services.svc.cluster.local -p 5432 -U postgres -d "%s" -c "GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO \"%s\";"
PGPASSWORD="$POSTGRES_PASSWORD" psql -h %s.shared-services.svc.cluster.local -p 5432 -U postgres -d "%s" -c "GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO \"%s\";"

# Set default privileges for future objects created by any user
PGPASSWORD="$POSTGRES_PASSWORD" psql -h %s.shared-services.svc.cluster.local -p 5432 -U postgres -d "%s" -c "ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON TABLES TO \"%s\";"
PGPASSWORD="$POSTGRES_PASSWORD" psql -h %s.shared-services.svc.cluster.local -p 5432 -U postgres -d "%s" -c "ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON SEQUENCES TO \"%s\";"

echo "Database initialization completed successfully!"
`, instanceName, instanceName, databaseName, instanceName, databaseName, instanceName, tenantUsername, instanceName, tenantUsername, instanceName, databaseName, tenantUsername, instanceName, databaseName, tenantUsername, instanceName, databaseName, tenantUsername, instanceName, databaseName, tenantUsername, instanceName, databaseName, tenantUsername, instanceName, databaseName, tenantUsername)

	// Create the Job in shared-services namespace
	job = batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: sharedNamespace,
			Labels: map[string]string{
				"tenant.core.mellifluus.io/tenant-uid": string(tenantEnv.UID),
				"tenant.core.mellifluus.io/managed-by": "tenant-operator",
				"job-type":                             "database-init",
				"instance":                             instanceName,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: int32Ptr(3), // Retry up to 3 times
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "db-init",
							Image:   "postgres:15-alpine", // Use Alpine for smaller size
							Command: []string{"/bin/sh", "-c"},
							Args:    []string{sqlScript},
							Env: []corev1.EnvVar{
								{
									Name: "POSTGRES_PASSWORD",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: instanceName + "-master",
											},
											Key: "POSTGRES_PASSWORD",
										},
									},
								},
								{
									Name:  "TENANT_PASSWORD",
									Value: tenantPassword,
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("200m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	if err := c.Create(ctx, &job); err != nil {
		return fmt.Errorf("failed to create database initialization job: %w", err)
	}

	log.Info("Created database initialization job", "job", jobName, "namespace", sharedNamespace, "database", databaseName, "user", tenantUsername)
	return nil
}

// ensureSharedPostgreSQLConfigMap ensures a shared PostgreSQL ConfigMap exists in shared-services namespace
func ensureSharedPostgreSQLConfigMap(ctx context.Context, c client.Client, log logr.Logger) error {
	sharedNamespace := "shared-services"
	configMapName := "postgresql-config"

	var configMap corev1.ConfigMap
	err := c.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: sharedNamespace}, &configMap)

	if errors.IsNotFound(err) {
		// Read PostgreSQL configuration from embedded files
		postgresqlConfData, err := postgresqlConfigs.ReadFile("configs/postgresql.conf")
		if err != nil {
			return fmt.Errorf("failed to read postgresql.conf: %w", err)
		}
		postgresqlConf := string(postgresqlConfData)

		pgHbaConfData, err := postgresqlConfigs.ReadFile("configs/pg_hba.conf")
		if err != nil {
			return fmt.Errorf("failed to read pg_hba.conf: %w", err)
		}
		pgHbaConf := string(pgHbaConfData)

		configMap = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: sharedNamespace,
				Labels: map[string]string{
					"app":                                  "postgresql",
					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
				},
			},
			Data: map[string]string{
				"postgresql.conf": postgresqlConf,
				"pg_hba.conf":     pgHbaConf,
			},
		}

		if err := c.Create(ctx, &configMap); err != nil {
			return fmt.Errorf("failed to create shared PostgreSQL config ConfigMap: %w", err)
		}
		log.Info("Created shared PostgreSQL configuration ConfigMap", "name", configMapName, "namespace", sharedNamespace)
	} else if err != nil {
		return err
	}

	return nil
}

// copyPostgreSQLConfigMapToNamespace copies the shared PostgreSQL ConfigMap to a target namespace
func copyPostgreSQLConfigMapToNamespace(ctx context.Context, c client.Client, targetNamespace string, log logr.Logger) error {
	sharedNamespace := "shared-services"
	configMapName := "postgresql-config"

	// Get the source ConfigMap from shared-services namespace
	var sourceConfigMap corev1.ConfigMap
	err := c.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: sharedNamespace}, &sourceConfigMap)
	if err != nil {
		return fmt.Errorf("failed to get shared PostgreSQL config: %w", err)
	}

	// Check if target ConfigMap already exists
	var targetConfigMap corev1.ConfigMap
	err = c.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: targetNamespace}, &targetConfigMap)
	if errors.IsNotFound(err) {
		// Create a new ConfigMap in the target namespace
		targetConfigMap = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: targetNamespace,
				Labels: map[string]string{
					"app":                                  "postgresql",
					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
				},
			},
			Data: sourceConfigMap.Data, // Copy the configuration data
		}

		if err := c.Create(ctx, &targetConfigMap); err != nil {
			return fmt.Errorf("failed to copy PostgreSQL config ConfigMap to namespace %s: %w", targetNamespace, err)
		}
		log.Info("Copied PostgreSQL configuration ConfigMap", "from", sharedNamespace+"/"+configMapName, "to", targetNamespace+"/"+configMapName)
	} else if err != nil {
		return err
	}

	return nil
}

// DeletePostgreSQLForTenant cleans up PostgreSQL resources for a tenant
func DeletePostgreSQLForTenant(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, log logr.Logger) error {
	if tenantEnv.Spec.Database.DedicatedInstance {
		// For dedicated instances, the namespace deletion will handle cleanup
		log.Info("Dedicated PostgreSQL instance will be cleaned up with namespace deletion", "tenant", tenantEnv.UID)
		return nil
	}

	// For shared instances, remove tenant from the shared instance count
	return removeTenantFromSharedInstance(ctx, c, string(tenantEnv.UID), log)
}
