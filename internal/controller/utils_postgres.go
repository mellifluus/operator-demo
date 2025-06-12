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
	MaxTenantsPerSharedInstance = 2

	// Label to track tenant count in shared instances
	SharedInstanceTenantCountLabel = "tenant.core.mellifluus.io/tenant-count"
)

type DatabaseCredentials struct {
	Host     string
	Port     string
	Database string
	Username string
	Password string
}

// CreatePostgreSQLForTenant creates PostgreSQL database resources for a tenant
func CreatePostgreSQLForTenant(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, log logr.Logger) error {
	if tenantEnv.Spec.Database.DedicatedInstance {
		_, err := createPostgreSQLInstance(ctx, c, tenantEnv, log)
		if err != nil {
			return fmt.Errorf("failed to create dedicated PostgreSQL instance: %w", err)
		}
		log.Info("Created dedicated PostgreSQL instance", "tenant", tenantEnv.Name, "namespace", "tenant-"+string(tenantEnv.UID))
		return nil
	}

	statefulSetName, err := findAvailableSharedInstance(ctx, c, log)
	if err != nil {
		log.Info("Creating new shared PostgreSQL instance")
		statefulSetName, err = createPostgreSQLInstance(ctx, c, tenantEnv, log)
		if err != nil {
			return fmt.Errorf("failed to create shared PostgreSQL instance: %w", err)
		}
	} else {
		// create tenant secret and init job
		tenantDbCreds, err := createTenantPostgreSQLSecret(ctx, c, tenantEnv, statefulSetName, "shared-services", log)
		if err != nil {
			return err
		}

		err = createDatabaseInitJob(ctx, c, tenantEnv, "postgresql-master-secret-"+statefulSetName[len(statefulSetName)-6:], "shared-services", tenantDbCreds, log)
		if err != nil {
			return err
		}
	}

	err = addTenantToSharedInstance(ctx, c, tenantEnv, statefulSetName, log)
	if err != nil {
		return fmt.Errorf("failed to add tenant to shared PostgreSQL instance: %w", err)
	}
	return nil
}

func createPostgreSQLInstance(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, log logr.Logger) (string, error) {
	var targetNamespace string
	if tenantEnv.Spec.Database.DedicatedInstance {
		targetNamespace = "tenant-" + string(tenantEnv.UID)
		if err := copyPostgreSQLConfigMapToNamespace(ctx, c, targetNamespace, log); err != nil {
			return "", fmt.Errorf("failed to copy PostgreSQL ConfigMap to tenant namespace: %w", err)
		}
	} else {
		targetNamespace = "shared-services"
	}

	secretName, err := createPostgreSQLSecret(ctx, c, targetNamespace, log)
	if err != nil {
		return "", err
	}

	statefulSetName, err := createPostgreSQLStatefulSet(ctx, c, tenantEnv, targetNamespace, secretName, log)
	if err != nil {
		return "", err
	}

	serviceName, err := createPostgreSQLService(ctx, c, tenantEnv, targetNamespace, statefulSetName, log)
	if err != nil {
		return "", err
	}

	tenantDbCreds, err := createTenantPostgreSQLSecret(ctx, c, tenantEnv, serviceName, targetNamespace, log)
	if err != nil {
		return "", err
	}

	err = createDatabaseInitJob(ctx, c, tenantEnv, secretName, targetNamespace, tenantDbCreds, log)
	if err != nil {
		return "", err
	}

	return statefulSetName, nil
}

// createSharedPostgreSQLSecret creates tenant-specific credentials for shared PostgreSQL
func createTenantPostgreSQLSecret(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, serviceName string, instanceNamespaceName string, log logr.Logger) (*DatabaseCredentials, error) {
	tenantNamespaceName := "tenant-" + string(tenantEnv.UID)
	secretName := "database-secret"

	var secret corev1.Secret
	err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: tenantNamespaceName}, &secret)

	if errors.IsNotFound(err) {
		databaseName := "tenant_" + string(tenantEnv.UID)[:8]
		tenantUsername := "tenant_" + string(tenantEnv.UID)[:8]

		// Generate secure random password for tenant
		tenantPassword, err := generatePassword(24)
		if err != nil {
			return nil, fmt.Errorf("failed to generate tenant password: %w", err)
		}

		creds := &DatabaseCredentials{
			Host:     serviceName + "." + instanceNamespaceName + ".svc.cluster.local",
			Port:     "5432",
			Database: databaseName,
			Username: tenantUsername,
			Password: tenantPassword,
		}

		secret = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: tenantNamespaceName,
				Labels: map[string]string{
					"tenant.core.mellifluus.io/tenant-uid": string(tenantEnv.UID),
					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"DB_HOST":     []byte(creds.Host),
				"DB_PORT":     []byte(creds.Port),
				"DB_NAME":     []byte(creds.Database),
				"DB_USERNAME": []byte(creds.Username),
				"DB_PASSWORD": []byte(creds.Password),
			},
		}

		if err := c.Create(ctx, &secret); err != nil {
			return nil, err
		}
		log.Info("Created tenant database secret", "namespace", tenantNamespaceName, "database", databaseName, "instance", serviceName)
	} else if err != nil {
		return nil, err
	}

	return &DatabaseCredentials{
		Host:     string(secret.Data["DB_HOST"]),
		Port:     string(secret.Data["DB_PORT"]),
		Database: string(secret.Data["DB_NAME"]),
		Username: string(secret.Data["DB_USERNAME"]),
		Password: string(secret.Data["DB_PASSWORD"]),
	}, nil
}

// createPostgreSQLSecret creates a master secret for PostgreSQL instance
func createPostgreSQLSecret(ctx context.Context, c client.Client, namespace string, log logr.Logger) (string, error) {
	id, err := generateId(6)
	if err != nil {
		return "", fmt.Errorf("failed to generate an id: %w", err)
	}
	secretName := "postgresql-master-secret-" + id

	var secret corev1.Secret
	err = c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, &secret)

	if errors.IsNotFound(err) {
		password, err := generatePassword(32)
		if err != nil {
			return "", fmt.Errorf("failed to generate secure password: %w", err)
		}

		secret = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
				Labels: map[string]string{
					"tenant.core.mellifluus.io/managed-by": "tenant-operator",
					"app":                                  "postgresql",
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
			return "", err
		}
		log.Info("Created PostgreSQL master secret", "namespace", namespace, "secret", secretName)
	} else if err != nil {
		return "", err
	}
	return secretName, nil
}

// createPostgreSQLStatefulSet creates a PostgreSQL StatefulSet for dedicated instances
func createPostgreSQLStatefulSet(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, namespace string, secretName string, log logr.Logger) (string, error) {
	id := secretName[len(secretName)-6:]
	statefulSetName := "postgresql-" + id

	var statefulSet appsv1.StatefulSet
	err := c.Get(ctx, types.NamespacedName{Name: statefulSetName, Namespace: namespace}, &statefulSet)

	if errors.IsNotFound(err) {
		version := tenantEnv.Spec.Database.Version
		if version == "" {
			version = "15"
		}

		storageSize := tenantEnv.Spec.Database.StorageSize
		if storageSize.IsZero() {
			storageSize = resource.MustParse("1Gi")
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
		// TODO: are the volume mounts needed here if there's a config map?
		statefulSet = appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      statefulSetName,
				Namespace: namespace,
				Labels: func() map[string]string {
					labels := map[string]string{
						"tenant.core.mellifluus.io/managed-by": "tenant-operator",
						"app":                                  statefulSetName,
					}
					if tenantEnv.Spec.Database.DedicatedInstance {
						labels["tenant.core.mellifluus.io/tenant-uid"] = string(tenantEnv.UID)
					} else {
						labels["tenant.core.mellifluus.io/shared-instance"] = "true"
					}
					return labels
				}(),
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: int32Ptr(1),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": statefulSetName,
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": statefulSetName,
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
												Name: secretName,
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
			return "", err
		}
		log.Info("Created PostgreSQL StatefulSet", "namespace", namespace, "statefulset", statefulSetName)
	} else if err != nil {
		return "", err
	}

	return statefulSetName, nil
}

// createPostgreSQLService creates a service for PostgreSQL
func createPostgreSQLService(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, namespace string, statefulSetName string, log logr.Logger) (string, error) {
	id := statefulSetName[len(statefulSetName)-6:]
	serviceName := "postgresql-" + id

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
					"app":                                  statefulSetName,
				},
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{
					"app": statefulSetName,
				},
				Ports: []corev1.ServicePort{
					{
						Port:       5432,
						TargetPort: intstr.FromInt32(5432),
						Protocol:   corev1.ProtocolTCP,
						Name:       statefulSetName,
					},
				},
				Type: corev1.ServiceTypeClusterIP,
			},
		}

		if err := c.Create(ctx, &service); err != nil {
			return "", err
		}
		log.Info("Created PostgreSQL service", "namespace", namespace, "service", serviceName)
	} else if err != nil {
		return "", err
	}

	return serviceName, nil
}

// findOrCreateAvailableSharedInstance finds a shared instance with available capacity
func findAvailableSharedInstance(ctx context.Context, c client.Client, log logr.Logger) (string, error) {
	sharedNamespace := "shared-services"

	// List all shared PostgreSQL StatefulSets
	var statefulSets appsv1.StatefulSetList
	listOpts := []client.ListOption{
		client.InNamespace(sharedNamespace),
		client.MatchingLabels{"tenant.core.mellifluus.io/shared-instance": "true"},
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

	// No available instances found
	return "", fmt.Errorf("no available shared PostgreSQL instances found")
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

// getPostgreSQLPVCsByLabels gets all PVCs belonging to a StatefulSet using label selectors
func getPostgreSQLPVCsByLabels(ctx context.Context, c client.Client, statefulSet *appsv1.StatefulSet) ([]corev1.PersistentVolumeClaim, error) {
	var pvcList corev1.PersistentVolumeClaimList

	// List all PVCs in the namespace
	listOpts := []client.ListOption{
		client.InNamespace(statefulSet.Namespace),
	}

	if err := c.List(ctx, &pvcList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list PVCs: %w", err)
	}

	var matchingPVCs []corev1.PersistentVolumeClaim

	// Filter PVCs that belong to this StatefulSet
	for _, pvc := range pvcList.Items {
		// Check if PVC name matches any of the StatefulSet's VolumeClaimTemplates
		for _, vct := range statefulSet.Spec.VolumeClaimTemplates {
			expectedPrefix := fmt.Sprintf("%s-%s-", vct.Name, statefulSet.Name)
			if strings.HasPrefix(pvc.Name, expectedPrefix) {
				// Additional validation: ensure it's actually a StatefulSet PVC (ends with number)
				suffix := strings.TrimPrefix(pvc.Name, expectedPrefix)
				if _, err := strconv.Atoi(suffix); err == nil {
					matchingPVCs = append(matchingPVCs, pvc)
					break // Found a match for this PVC, no need to check other templates
				}
			}
		}
	}

	return matchingPVCs, nil
}

// cleanupSharedPostgreSQLInstance removes all resources associated with a shared PostgreSQL instance
func cleanupSharedPostgreSQLInstance(ctx context.Context, c client.Client, statefulSetName string, log logr.Logger) error {
	sharedNamespace := "shared-services"

	// Get the StatefulSet first to extract the ID
	var statefulSet appsv1.StatefulSet
	err := c.Get(ctx, types.NamespacedName{Name: statefulSetName, Namespace: sharedNamespace}, &statefulSet)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("StatefulSet not found, may already be deleted", "statefulset", statefulSetName)
		} else {
			return fmt.Errorf("failed to get StatefulSet: %w", err)
		}
	}

	// Extract ID from StatefulSet name (postgresql-{id})
	id := ""
	if len(statefulSetName) > 11 && strings.HasPrefix(statefulSetName, "postgresql-") {
		id = statefulSetName[11:] // Remove "postgresql-" prefix
	} else {
		return fmt.Errorf("invalid StatefulSet name format: %s", statefulSetName)
	}

	log.Info("Starting cleanup of shared PostgreSQL instance", "instance", statefulSetName, "id", id)

	var cleanupErrors []error

	// 1. Get and delete PVCs first (before StatefulSet deletion)
	if !errors.IsNotFound(err) { // Only if StatefulSet exists
		pvcs, pvcErr := getPostgreSQLPVCsByLabels(ctx, c, &statefulSet)
		if pvcErr != nil {
			log.Error(pvcErr, "Failed to get PVCs for cleanup", "instance", statefulSetName)
			cleanupErrors = append(cleanupErrors, fmt.Errorf("failed to get PVCs: %w", pvcErr))
		} else {
			for _, pvc := range pvcs {
				storageSize := "unknown"
				if storage, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
					storageSize = storage.String()
				}
				log.Info("Deleting PVC", "pvc", pvc.Name, "size", storageSize)

				if delErr := c.Delete(ctx, &pvc); delErr != nil && !errors.IsNotFound(delErr) {
					log.Error(delErr, "Failed to delete PVC", "pvc", pvc.Name)
					cleanupErrors = append(cleanupErrors, fmt.Errorf("failed to delete PVC %s: %w", pvc.Name, delErr))
				} else {
					log.Info("Deleted PVC", "pvc", pvc.Name)
				}
			}
		}
	}

	// 2. Delete StatefulSet (this will also delete Pods)
	if !errors.IsNotFound(err) { // Only if StatefulSet exists
		log.Info("Deleting StatefulSet", "statefulset", statefulSetName)
		if delErr := c.Delete(ctx, &statefulSet); delErr != nil && !errors.IsNotFound(delErr) {
			log.Error(delErr, "Failed to delete StatefulSet", "statefulset", statefulSetName)
			cleanupErrors = append(cleanupErrors, fmt.Errorf("failed to delete StatefulSet: %w", delErr))
		} else {
			log.Info("Deleted StatefulSet", "statefulset", statefulSetName)
		}
	}

	// 3. Delete Service
	serviceName := "postgresql-" + id
	var service corev1.Service
	if getErr := c.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: sharedNamespace}, &service); getErr == nil {
		log.Info("Deleting Service", "service", serviceName)
		if delErr := c.Delete(ctx, &service); delErr != nil && !errors.IsNotFound(delErr) {
			log.Error(delErr, "Failed to delete Service", "service", serviceName)
			cleanupErrors = append(cleanupErrors, fmt.Errorf("failed to delete Service: %w", delErr))
		} else {
			log.Info("Deleted Service", "service", serviceName)
		}
	} else if !errors.IsNotFound(getErr) {
		log.Error(getErr, "Failed to get Service", "service", serviceName)
		cleanupErrors = append(cleanupErrors, fmt.Errorf("failed to get Service: %w", getErr))
	}

	// 4. Delete master Secret
	masterSecretName := "postgresql-master-secret-" + id
	var secret corev1.Secret
	if getErr := c.Get(ctx, types.NamespacedName{Name: masterSecretName, Namespace: sharedNamespace}, &secret); getErr == nil {
		log.Info("Deleting master Secret", "secret", masterSecretName)
		if delErr := c.Delete(ctx, &secret); delErr != nil && !errors.IsNotFound(delErr) {
			log.Error(delErr, "Failed to delete master Secret", "secret", masterSecretName)
			cleanupErrors = append(cleanupErrors, fmt.Errorf("failed to delete Secret: %w", delErr))
		} else {
			log.Info("Deleted master Secret", "secret", masterSecretName)
		}
	} else if !errors.IsNotFound(getErr) {
		log.Error(getErr, "Failed to get master Secret", "secret", masterSecretName)
		cleanupErrors = append(cleanupErrors, fmt.Errorf("failed to get Secret: %w", getErr))
	}
	// 5. Delete related Jobs (init and removal jobs) - only for this specific StatefulSet
	var jobList batchv1.JobList
	listOpts := []client.ListOption{
		client.InNamespace(sharedNamespace),
		client.MatchingLabels{
			"tenant.core.mellifluus.io/managed-by": "tenant-operator",
			"tenant.core.mellifluus.io/ss-name":    statefulSetName, // Only jobs for this specific StatefulSet
		},
	}

	if listErr := c.List(ctx, &jobList, listOpts...); listErr == nil {
		for _, job := range jobList.Items {
			log.Info("Deleting related Job", "job", job.Name, "statefulset", statefulSetName)
			backgroundPolicy := metav1.DeletePropagationBackground
			if delErr := c.Delete(ctx, &job, &client.DeleteOptions{
				PropagationPolicy: &backgroundPolicy,
			}); delErr != nil && !errors.IsNotFound(delErr) {
				log.Error(delErr, "Failed to delete Job", "job", job.Name)
				cleanupErrors = append(cleanupErrors, fmt.Errorf("failed to delete Job %s: %w", job.Name, delErr))
			} else {
				log.Info("Deleted Job", "job", job.Name)
			}
		}
	} else {
		log.Error(listErr, "Failed to list Jobs for cleanup")
		cleanupErrors = append(cleanupErrors, fmt.Errorf("failed to list Jobs: %w", listErr))
	}

	// Return aggregated errors if any
	if len(cleanupErrors) > 0 {
		return fmt.Errorf("cleanup completed with %d errors: %v", len(cleanupErrors), cleanupErrors)
	}

	log.Info("Successfully completed cleanup of shared PostgreSQL instance", "instance", statefulSetName)
	return nil
}

// removeTenantFromSharedInstance removes a tenant from a shared instance's tenant list
func removeTenantFromSharedInstance(ctx context.Context, c client.Client, tenantUID string, log logr.Logger) error {
	sharedNamespace := "shared-services"

	// List all shared PostgreSQL StatefulSets to find which one contains this tenant
	var statefulSets appsv1.StatefulSetList
	listOpts := []client.ListOption{
		client.InNamespace(sharedNamespace),
		client.MatchingLabels{"tenant.core.mellifluus.io/shared-instance": "true"},
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

			// Check if this was the last tenant - handle instance cleanup
			if len(newTenantList) == 0 {
				log.Info("Shared instance has no remaining tenants - considering cleanup", "instance", ss.Name)
				cleanupSharedPostgreSQLInstance(ctx, c, ss.Name, log)
				return nil
			}

			jobName := "db-remove-" + string(tenantUID)[:8]

			// Check if removal job already exists or completed
			var job batchv1.Job
			err := c.Get(ctx, types.NamespacedName{Name: jobName, Namespace: sharedNamespace}, &job)
			if err == nil {
				// Job exists, check if it completed successfully
				for _, condition := range job.Status.Conditions {
					if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
						log.Info("Database removal job already completed", "job", jobName)
						return nil
					}
				}
				// Job exists but not completed, let it continue
				return nil
			} else if !errors.IsNotFound(err) {
				return err
			}

			masterSecretName := "postgresql-master-secret-" + ss.Name[len(ss.Name)-6:]
			hostname := ss.Name + "." + sharedNamespace + ".svc.cluster.local"
			tenantUsername := "tenant_" + string(tenantUID)[:8]
			tenantDatabase := "tenant_" + string(tenantUID)[:8]

			sqlScript := fmt.Sprintf(`#!/bin/bash
        set -e

        DB_HOST="%s"
        TENANT_USERNAME="%s"
        DATABASE_NAME="%s"

        echo "Waiting for PostgreSQL to be ready..."
        until pg_isready -h $DB_HOST -p 5432 -U postgres; do
          echo "PostgreSQL is unavailable - sleeping"
          sleep 2
        done

        echo "PostgreSQL is ready - deleting database and user..."

        # Drop the database if it exists
        PGPASSWORD="$POSTGRES_PASSWORD" psql -h $DB_HOST -p 5432 -U postgres -tc "SELECT 1 FROM pg_database WHERE datname = '$DATABASE_NAME'" | grep -q 1 && PGPASSWORD="$POSTGRES_PASSWORD" psql -h $DB_HOST -p 5432 -U postgres -c "DROP DATABASE IF EXISTS \"$DATABASE_NAME\";"
        # Drop the user if it exists
        PGPASSWORD="$POSTGRES_PASSWORD" psql -h $DB_HOST -p 5432 -U postgres -tc "SELECT 1 FROM pg_roles WHERE rolname='$TENANT_USERNAME'" | grep -q 1 && PGPASSWORD="$POSTGRES_PASSWORD" psql -h $DB_HOST -p 5432 -U postgres -c "DROP USER IF EXISTS \"$TENANT_USERNAME\";"

        echo "Database removal completed successfully!"
      `, hostname, tenantUsername, tenantDatabase)

			job = batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jobName,
					Namespace: "shared-services",
					Labels: map[string]string{
						"tenant.core.mellifluus.io/tenant-uid": tenantUID,
						"tenant.core.mellifluus.io/managed-by": "tenant-operator",
						"tenant.core.mellifluus.io/ss-name":    ss.Name,
					},
				},
				Spec: batchv1.JobSpec{
					BackoffLimit: int32Ptr(3), // Retry up to 3 times
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{
								{
									Name:    "db-remove",
									Image:   "postgres:15-alpine",
									Command: []string{"/bin/sh", "-c"},
									Args:    []string{sqlScript},
									Env: []corev1.EnvVar{
										{
											Name: "POSTGRES_PASSWORD",
											ValueFrom: &corev1.EnvVarSource{
												SecretKeyRef: &corev1.SecretKeySelector{
													LocalObjectReference: corev1.LocalObjectReference{
														Name: masterSecretName,
													},
													Key: "POSTGRES_PASSWORD",
												},
											},
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
				return fmt.Errorf("failed to create database removal job: %w", err)
			}

			return nil
		}
	}

	log.Info("Tenant not found in any shared instance", "tenant", tenantUID)
	return nil
}

// createDatabaseInitJob creates a Kubernetes Job to initialize database and user in shared PostgreSQL
func createDatabaseInitJob(ctx context.Context, c client.Client, tenantEnv *tenantv1.TenantEnvironment, masterSecretName string, instanceNamespace string, tenantCreds *DatabaseCredentials, log logr.Logger) error {
	jobName := "db-init-" + string(tenantEnv.UID)[:8]

	// Check if job already exists or completed
	var job batchv1.Job
	err := c.Get(ctx, types.NamespacedName{Name: jobName, Namespace: instanceNamespace}, &job)
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

	// Create SQL init script that uses environment variables
	sqlScript := fmt.Sprintf(`#!/bin/bash
    set -e

    DB_HOST="%s"
    TENANT_USERNAME="%s"
    DATABASE_NAME="%s"

    echo "Waiting for PostgreSQL to be ready..."
    until pg_isready -h $DB_HOST -p 5432 -U postgres; do
      echo "PostgreSQL is unavailable - sleeping"
      sleep 2
    done

    echo "PostgreSQL is ready - creating database and user..."

    # Create database if it doesn't exist
    PGPASSWORD="$POSTGRES_PASSWORD" psql -h $DB_HOST -p 5432 -U postgres -tc "SELECT 1 FROM pg_database WHERE datname = '$DATABASE_NAME'" | grep -q 1 || PGPASSWORD="$POSTGRES_PASSWORD" psql -h $DB_HOST -p 5432 -U postgres -c "CREATE DATABASE \"$DATABASE_NAME\";"

    # Create user if it doesn't exist (using TENANT_PASSWORD environment variable)
    PGPASSWORD="$POSTGRES_PASSWORD" psql -h $DB_HOST -p 5432 -U postgres -tc "SELECT 1 FROM pg_roles WHERE rolname='$TENANT_USERNAME'" | grep -q 1 || PGPASSWORD="$POSTGRES_PASSWORD" psql -h $DB_HOST -p 5432 -U postgres -c "CREATE USER \"$TENANT_USERNAME\" WITH PASSWORD '$TENANT_PASSWORD';"

    # Grant database-level privileges
    PGPASSWORD="$POSTGRES_PASSWORD" psql -h $DB_HOST -p 5432 -U postgres -c "GRANT ALL PRIVILEGES ON DATABASE \"$DATABASE_NAME\" TO \"$TENANT_USERNAME\";"

    # Grant schema-level privileges (connect to the tenant database)
    PGPASSWORD="$POSTGRES_PASSWORD" psql -h $DB_HOST -p 5432 -U postgres -d "$DATABASE_NAME" -c "GRANT USAGE, CREATE ON SCHEMA public TO \"$TENANT_USERNAME\";"

    # Grant privileges on all existing tables and sequences (if any)
    PGPASSWORD="$POSTGRES_PASSWORD" psql -h $DB_HOST -p 5432 -U postgres -d "$DATABASE_NAME" -c "GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO \"$TENANT_USERNAME\";"
    PGPASSWORD="$POSTGRES_PASSWORD" psql -h $DB_HOST -p 5432 -U postgres -d "$DATABASE_NAME" -c "GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO \"$TENANT_USERNAME\";"

    # Set default privileges for future objects created by any user
    PGPASSWORD="$POSTGRES_PASSWORD" psql -h $DB_HOST -p 5432 -U postgres -d "$DATABASE_NAME" -c "ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON TABLES TO \"$TENANT_USERNAME\";"
    PGPASSWORD="$POSTGRES_PASSWORD" psql -h $DB_HOST -p 5432 -U postgres -d "$DATABASE_NAME" -c "ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON SEQUENCES TO \"$TENANT_USERNAME\";"

    echo "Database initialization completed successfully!"
  `, tenantCreds.Host, tenantCreds.Username, tenantCreds.Database)

	// Create the Job in shared-services namespace
	job = batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: instanceNamespace,
			Labels: map[string]string{
				"tenant.core.mellifluus.io/tenant-uid": string(tenantEnv.UID),
				"tenant.core.mellifluus.io/managed-by": "tenant-operator",
				"tenant.core.mellifluus.io/ss-name":    strings.Split(tenantCreds.Host, ".")[0], // Extract StatefulSet name from host
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
							Image:   "postgres:15-alpine",
							Command: []string{"/bin/sh", "-c"},
							Args:    []string{sqlScript},
							Env: []corev1.EnvVar{
								{
									Name: "POSTGRES_PASSWORD",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: masterSecretName,
											},
											Key: "POSTGRES_PASSWORD",
										},
									},
								},
								{
									Name:  "TENANT_PASSWORD",
									Value: tenantCreds.Password,
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

	log.Info("Created database initialization job", "job", jobName, "namespace", instanceNamespace, "database", tenantCreds.Database, "user", tenantCreds.Username)
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
