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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	tenantv1 "github.com/mellifluus/operator-demo.git/api/v1"
)

// GetTenantEnvironment fetches a TenantEnvironment resource by namespaced name
func GetTenantEnvironment(ctx context.Context, c client.Client, namespacedName types.NamespacedName) (*tenantv1.TenantEnvironment, error) {
	var tenantEnv tenantv1.TenantEnvironment
	if err := c.Get(ctx, namespacedName, &tenantEnv); err != nil {
		return nil, err
	}
	return &tenantEnv, nil
}

// CreateNamespaceForTenant creates a namespace for the given tenant
func CreateNamespaceForTenant(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, log logr.Logger) error {
	namespaceName := "tenant-" + string(tenantEnv.UID)

	var namespace corev1.Namespace
	err := c.Get(ctx, types.NamespacedName{Name: namespaceName}, &namespace)

	if errors.IsNotFound(err) {
		namespace = corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespaceName,
				Labels: map[string]string{
					"tenant.core.mellifluus.io/tenant-uid":    string(tenantEnv.UID),
					"tenant.core.mellifluus.io/managed-by":    "tenant-operator",
					"tenant.core.mellifluus.io/resource-name": tenantEnv.Name,
				},
			},
		}

		if err := c.Create(ctx, &namespace); err != nil {
			return err
		}
		log.Info("Created namespace", "namespace", namespaceName)
	} else if err != nil {
		return err
	}

	return nil
}

// DeleteNamespaceForTenant deletes the namespace associated with a tenant
func DeleteNamespaceForTenant(ctx context.Context, c client.Client, tenantID string, log logr.Logger) error {
	namespaceName := "tenant-" + tenantID

	var namespace corev1.Namespace
	err := c.Get(ctx, types.NamespacedName{Name: namespaceName}, &namespace)

	if err == nil {
		if err := c.Delete(ctx, &namespace); err != nil {
			return err
		}
		log.Info("Deleted namespace", "namespace", namespaceName)
	} else if !errors.IsNotFound(err) {
		return err
	}

	return nil
}

// CreateResourceQuotaForTenant creates a ResourceQuota for the tenant namespace
func CreateResourceQuotaForTenant(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, log logr.Logger) error {
	namespaceName := "tenant-" + string(tenantEnv.UID)
	quotaName := "tenant-quota"

	var resourceQuota corev1.ResourceQuota
	err := c.Get(ctx, types.NamespacedName{Name: quotaName, Namespace: namespaceName}, &resourceQuota)

	if errors.IsNotFound(err) {
		// Build resource list from tenant spec
		resourceList := corev1.ResourceList{}

		if tenantEnv.Spec.ResourceQuotas != nil {
			if !tenantEnv.Spec.ResourceQuotas.CPULimit.IsZero() {
				resourceList[corev1.ResourceRequestsCPU] = tenantEnv.Spec.ResourceQuotas.CPULimit
				resourceList[corev1.ResourceLimitsCPU] = tenantEnv.Spec.ResourceQuotas.CPULimit
			}
			if !tenantEnv.Spec.ResourceQuotas.MemoryLimit.IsZero() {
				resourceList[corev1.ResourceRequestsMemory] = tenantEnv.Spec.ResourceQuotas.MemoryLimit
				resourceList[corev1.ResourceLimitsMemory] = tenantEnv.Spec.ResourceQuotas.MemoryLimit
			}
			if !tenantEnv.Spec.ResourceQuotas.StorageLimit.IsZero() {
				resourceList[corev1.ResourceRequestsStorage] = tenantEnv.Spec.ResourceQuotas.StorageLimit
			}
			if tenantEnv.Spec.ResourceQuotas.PodLimit > 0 {
				resourceList[corev1.ResourcePods] = *resource.NewQuantity(int64(tenantEnv.Spec.ResourceQuotas.PodLimit), resource.DecimalSI)
			}
		}

		resourceQuota = corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{
				Name:      quotaName,
				Namespace: namespaceName,
				Labels: map[string]string{
					"tenant.core.mellifluus.io/tenant-uid": string(tenantEnv.UID),
					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
				},
			},
			Spec: corev1.ResourceQuotaSpec{
				Hard: resourceList,
			},
		}

		if err := c.Create(ctx, &resourceQuota); err != nil {
			return err
		}
		log.Info("Created ResourceQuota", "namespace", namespaceName, "quota", quotaName)
	} else if err != nil {
		return err
	}

	return nil
}

// DeleteResourceQuotaForTenant deletes the ResourceQuota associated with a tenant's namespace
func DeleteResourceQuotaForTenant(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, log logr.Logger) error {
	namespaceName := "tenant-" + string(tenantEnv.UID)
	quotaName := "tenant-quota"

	var resourceQuota corev1.ResourceQuota
	err := c.Get(ctx, types.NamespacedName{Name: quotaName, Namespace: namespaceName}, &resourceQuota)

	if err == nil {
		if err := c.Delete(ctx, &resourceQuota); err != nil {
			return err
		}
		log.Info("Deleted ResourceQuota", "resourceQuota", quotaName, "namespace", namespaceName)
	} else if !errors.IsNotFound(err) {
		return err
	}

	return nil
}

// // CreatePostgreSQLForTenant creates PostgreSQL database resources for a tenant
// func CreatePostgreSQLForTenant(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, log logr.Logger) error {
// 	if tenantEnv.Spec.Database.DedicatedInstance {
// 		return createDedicatedPostgreSQL(ctx, c, tenantEnv, log)
// 	}
// 	return createSharedPostgreSQLEntry(ctx, c, tenantEnv, log)
// }

// // createDedicatedPostgreSQL creates a dedicated PostgreSQL instance for enterprise tenants
// func createDedicatedPostgreSQL(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, log logr.Logger) error {
// 	namespaceName := "tenant-" + string(tenantEnv.UID)

// 	// Create PostgreSQL Secret with credentials
// 	if err := createPostgreSQLSecret(ctx, c, tenantEnv, namespaceName, log); err != nil {
// 		return err
// 	}

// 	// Create PostgreSQL StatefulSet
// 	if err := createPostgreSQLStatefulSet(ctx, c, tenantEnv, namespaceName, log); err != nil {
// 		return err
// 	}

// 	// Create PostgreSQL Service
// 	if err := createPostgreSQLService(ctx, c, tenantEnv, namespaceName, log); err != nil {
// 		return err
// 	}

// 	// Create PostgreSQL ConfigMap with secure settings
// 	if err := createPostgreSQLConfigMap(ctx, c, namespaceName, "postgresql-config", log); err != nil {
// 		return err
// 	}

// 	return nil
// }

// // createPostgreSQLSecret creates a secret with PostgreSQL credentials
// func createPostgreSQLSecret(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, namespace string, log logr.Logger) error {
// 	secretName := "postgresql-secret"

// 	var secret corev1.Secret
// 	err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, &secret)

// 	if errors.IsNotFound(err) {
// 		databaseName := tenantEnv.Spec.Database.DatabaseName
// 		if databaseName == "" {
// 			databaseName = "tenant_db"
// 		}

// 		// Generate secure random password
// 		password, err := generatePassword(24)
// 		if err != nil {
// 			return fmt.Errorf("failed to generate secure password: %w", err)
// 		}

// 		secret = corev1.Secret{
// 			ObjectMeta: metav1.ObjectMeta{
// 				Name:      secretName,
// 				Namespace: namespace,
// 				Labels: map[string]string{
// 					"tenant.core.mellifluus.io/tenant-uid": string(tenantEnv.UID),
// 					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
// 					"app":                                  "postgresql",
// 				},
// 			},
// 			Type: corev1.SecretTypeOpaque,
// 			Data: map[string][]byte{
// 				"POSTGRES_DB":       []byte(databaseName),
// 				"POSTGRES_USER":     []byte("tenant_user"),
// 				"POSTGRES_PASSWORD": []byte(password),
// 			},
// 		}

// 		if err := c.Create(ctx, &secret); err != nil {
// 			return err
// 		}
// 		log.Info("Created PostgreSQL secret", "namespace", namespace, "secret", secretName)
// 	} else if err != nil {
// 		return err
// 	}

// 	return nil
// }

// // createPostgreSQLStatefulSet creates a PostgreSQL StatefulSet for dedicated instances
// func createPostgreSQLStatefulSet(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, namespace string, log logr.Logger) error {
// 	statefulSetName := "postgresql"

// 	var statefulSet appsv1.StatefulSet
// 	err := c.Get(ctx, types.NamespacedName{Name: statefulSetName, Namespace: namespace}, &statefulSet)

// 	if errors.IsNotFound(err) {
// 		version := tenantEnv.Spec.Database.Version
// 		if version == "" {
// 			version = "15"
// 		}

// 		storageSize := tenantEnv.Spec.Database.StorageSize
// 		if storageSize.IsZero() {
// 			storageSize = resource.MustParse("20Gi")
// 		}

// 		// Determine resource limits based on performance tier
// 		cpuLimit := "500m"
// 		memoryLimit := "1Gi"

// 		switch tenantEnv.Spec.Database.PerformanceTier {
// 		case "premium":
// 			cpuLimit = "1"
// 			memoryLimit = "2Gi"
// 		case "enterprise":
// 			cpuLimit = "2"
// 			memoryLimit = "4Gi"
// 		}

// 		statefulSet = appsv1.StatefulSet{
// 			ObjectMeta: metav1.ObjectMeta{
// 				Name:      statefulSetName,
// 				Namespace: namespace,
// 				Labels: map[string]string{
// 					"tenant.core.mellifluus.io/tenant-uid": string(tenantEnv.UID),
// 					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
// 					"app":                                  "postgresql",
// 				},
// 			},
// 			Spec: appsv1.StatefulSetSpec{
// 				Replicas: int32Ptr(1),
// 				Selector: &metav1.LabelSelector{
// 					MatchLabels: map[string]string{
// 						"app": "postgresql",
// 					},
// 				},
// 				Template: corev1.PodTemplateSpec{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Labels: map[string]string{
// 							"app": "postgresql",
// 						},
// 					},
// 					Spec: corev1.PodSpec{
// 						Containers: []corev1.Container{
// 							{
// 								Name:  "postgresql",
// 								Image: "postgres:" + version,
// 								EnvFrom: []corev1.EnvFromSource{
// 									{
// 										SecretRef: &corev1.SecretEnvSource{
// 											LocalObjectReference: corev1.LocalObjectReference{
// 												Name: "postgresql-secret",
// 											},
// 										},
// 									},
// 								},
// 								Ports: []corev1.ContainerPort{
// 									{
// 										ContainerPort: 5432,
// 										Name:          "postgresql",
// 									},
// 								},
// 								Resources: corev1.ResourceRequirements{
// 									Limits: corev1.ResourceList{
// 										corev1.ResourceCPU:    resource.MustParse(cpuLimit),
// 										corev1.ResourceMemory: resource.MustParse(memoryLimit),
// 									},
// 									Requests: corev1.ResourceList{
// 										corev1.ResourceCPU:    resource.MustParse("250m"),
// 										corev1.ResourceMemory: resource.MustParse("512Mi"),
// 									},
// 								},
// 								VolumeMounts: []corev1.VolumeMount{
// 									{
// 										Name:      "postgresql-storage",
// 										MountPath: "/var/lib/postgresql/data",
// 									},
// 									{
// 										Name:      "postgresql-config",
// 										MountPath: "/etc/postgresql/postgresql.conf",
// 										SubPath:   "postgresql.conf",
// 										ReadOnly:  true,
// 									},
// 									{
// 										Name:      "postgresql-config",
// 										MountPath: "/etc/postgresql/pg_hba.conf",
// 										SubPath:   "pg_hba.conf",
// 										ReadOnly:  true,
// 									},
// 								},
// 								Command: []string{
// 									"docker-entrypoint.sh",
// 									"postgres",
// 									"-c", "config_file=/etc/postgresql/postgresql.conf",
// 									"-c", "hba_file=/etc/postgresql/pg_hba.conf",
// 								},
// 							},
// 						},
// 						Volumes: []corev1.Volume{
// 							{
// 								Name: "postgresql-config",
// 								VolumeSource: corev1.VolumeSource{
// 									ConfigMap: &corev1.ConfigMapVolumeSource{
// 										LocalObjectReference: corev1.LocalObjectReference{
// 											Name: "postgresql-config",
// 										},
// 									},
// 								},
// 							},
// 						},
// 					},
// 				},
// 				VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
// 					{
// 						ObjectMeta: metav1.ObjectMeta{
// 							Name: "postgresql-storage",
// 						},
// 						Spec: corev1.PersistentVolumeClaimSpec{
// 							AccessModes: []corev1.PersistentVolumeAccessMode{
// 								corev1.ReadWriteOnce,
// 							},
// 							Resources: corev1.VolumeResourceRequirements{
// 								Requests: corev1.ResourceList{
// 									corev1.ResourceStorage: storageSize,
// 								},
// 							},
// 						},
// 					},
// 				},
// 			},
// 		}

// 		if err := c.Create(ctx, &statefulSet); err != nil {
// 			return err
// 		}
// 		log.Info("Created PostgreSQL StatefulSet", "namespace", namespace, "statefulset", statefulSetName)
// 	} else if err != nil {
// 		return err
// 	}

// 	return nil
// }

// // createPostgreSQLService creates a service for PostgreSQL
// func createPostgreSQLService(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, namespace string, log logr.Logger) error {
// 	serviceName := "postgresql-service"

// 	var service corev1.Service
// 	err := c.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: namespace}, &service)

// 	if errors.IsNotFound(err) {
// 		service = corev1.Service{
// 			ObjectMeta: metav1.ObjectMeta{
// 				Name:      serviceName,
// 				Namespace: namespace,
// 				Labels: map[string]string{
// 					"tenant.core.mellifluus.io/tenant-uid": string(tenantEnv.UID),
// 					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
// 					"app":                                  "postgresql",
// 				},
// 			},
// 			Spec: corev1.ServiceSpec{
// 				Selector: map[string]string{
// 					"app": "postgresql",
// 				},
// 				Ports: []corev1.ServicePort{
// 					{
// 						Port:       5432,
// 						TargetPort: intstr.FromInt(5432),
// 						Protocol:   corev1.ProtocolTCP,
// 						Name:       "postgresql",
// 					},
// 				},
// 				Type: corev1.ServiceTypeClusterIP,
// 			},
// 		}

// 		if err := c.Create(ctx, &service); err != nil {
// 			return err
// 		}
// 		log.Info("Created PostgreSQL service", "namespace", namespace, "service", serviceName)
// 	} else if err != nil {
// 		return err
// 	}

// 	return nil
// }

// // createSharedPostgreSQLEntry creates a database entry in the shared PostgreSQL instance
// func createSharedPostgreSQLEntry(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, log logr.Logger) error {
// 	// 1. Find or create an available shared PostgreSQL instance
// 	sharedInstanceName, err := findOrCreateAvailableSharedInstance(ctx, c, log)
// 	if err != nil {
// 		return err
// 	}

// 	// 2. Create secret with tenant database credentials
// 	if err := createSharedPostgreSQLSecret(ctx, c, tenantEnv, sharedInstanceName, log); err != nil {
// 		return err
// 	}

// 	// 3. Create ConfigMap with connection info
// 	if err := createSharedPostgreSQLConfig(ctx, c, tenantEnv, sharedInstanceName, log); err != nil {
// 		return err
// 	}

// 	// 4. Update tenant count for the shared instance (only once per tenant)
// 	if err := addTenantToSharedInstance(ctx, c, tenantEnv, sharedInstanceName, log); err != nil {
// 		return err
// 	}

// 	// 5. Create database initialization Job
// 	if err := createDatabaseInitJob(ctx, c, tenantEnv, sharedInstanceName, log); err != nil {
// 		return err
// 	}

// 	return nil
// }

// // findOrCreateAvailableSharedInstance finds a shared instance with available capacity or creates a new one
// func findOrCreateAvailableSharedInstance(ctx context.Context, c client.Client, log logr.Logger) (string, error) {
// 	sharedNamespace := "shared-services"

// 	// Ensure shared-services namespace exists
// 	if err := ensureSharedServicesNamespace(ctx, c, log); err != nil {
// 		return "", err
// 	}

// 	// List all shared PostgreSQL StatefulSets
// 	var statefulSets appsv1.StatefulSetList
// 	listOpts := []client.ListOption{
// 		client.InNamespace(sharedNamespace),
// 		client.MatchingLabels{"app": "shared-postgresql"},
// 	}

// 	if err := c.List(ctx, &statefulSets, listOpts...); err != nil {
// 		return "", err
// 	}

// 	// Find an instance with available capacity
// 	for _, ss := range statefulSets.Items {
// 		tenantCountStr, exists := ss.Labels[SharedInstanceTenantCountLabel]
// 		if !exists {
// 			tenantCountStr = "0"
// 		}

// 		tenantCount, err := strconv.Atoi(tenantCountStr)
// 		if err != nil {
// 			log.Error(err, "Invalid tenant count label", "statefulset", ss.Name)
// 			continue
// 		}

// 		if tenantCount < MaxTenantsPerSharedInstance {
// 			log.Info("Found available shared instance", "instance", ss.Name, "currentTenants", tenantCount)
// 			return ss.Name, nil
// 		}
// 	}

// 	// No available instances found, create a new one
// 	newInstanceName := fmt.Sprintf("shared-postgresql-%d", len(statefulSets.Items))
// 	log.Info("Creating new shared PostgreSQL instance", "instance", newInstanceName, "reason", "capacity limit reached")

// 	if err := createSharedPostgreSQLInstance(ctx, c, sharedNamespace, newInstanceName, log); err != nil {
// 		return "", err
// 	}

// 	return newInstanceName, nil
// }

// ensureSharedServicesNamespace ensures the shared-services namespace exists
func ensureSharedServicesNamespace(ctx context.Context, c client.Client, log logr.Logger) error {
	sharedNamespace := "shared-services"

	var namespace corev1.Namespace
	err := c.Get(ctx, types.NamespacedName{Name: sharedNamespace}, &namespace)
	if errors.IsNotFound(err) {
		namespace = corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: sharedNamespace,
				Labels: map[string]string{
					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
					"tenant.core.mellifluus.io/type":       "shared-services",
				},
			},
		}
		if err := c.Create(ctx, &namespace); err != nil {
			return err
		}
		log.Info("Created shared-services namespace")
	} else if err != nil {
		return err
	}
	return nil
}
