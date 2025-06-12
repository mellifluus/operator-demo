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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	tenantv1 "github.com/mellifluus/operator-demo.git/api/v1"
)

// TenantEnvironmentReconciler reconciles a TenantEnvironment object
type TenantEnvironmentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const (
	tenantEnvironmentFinalizer = "tenant.core.mellifluus.io/finalizer"
)

// +kubebuilder:rbac:groups=tenant.core.mellifluus.io,resources=tenantenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=tenant.core.mellifluus.io,resources=tenantenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=tenant.core.mellifluus.io,resources=tenantenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=resourcequotas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

func (r *TenantEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	tenantEnv, err := GetTenantEnvironment(ctx, r.Client, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !tenantEnv.ObjectMeta.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(tenantEnv, tenantEnvironmentFinalizer) {
			log.Info("Cleaning up tenant", "uid", tenantEnv.UID, "displayName", tenantEnv.Spec.DisplayName)

			// Delete PostgreSQL resources (clean up shared instance tracking)
			if err := DeletePostgreSQLForTenant(ctx, r.Client, tenantEnv, log); err != nil {
				log.Error(err, "Failed to delete PostgreSQL resources")
				return ctrl.Result{}, err
			}

			// Delete ResourceQuota if it exists
			if tenantEnv.Spec.ResourceQuotas != nil {
				if err := DeleteResourceQuotaForTenant(ctx, r.Client, tenantEnv, log); err != nil {
					log.Error(err, "Failed to delete ResourceQuota")
					return ctrl.Result{}, err
				}
			}

			// Delete namespace (this will also delete the ResourceQuota since it's in the namespace)
			if err := DeleteNamespaceForTenant(ctx, r.Client, string(tenantEnv.UID), log); err != nil {
				log.Error(err, "Failed to delete namespace")
				return ctrl.Result{}, err
			}

			controllerutil.RemoveFinalizer(tenantEnv, tenantEnvironmentFinalizer)
			return ctrl.Result{}, r.Update(ctx, tenantEnv)
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(tenantEnv, tenantEnvironmentFinalizer) {
		controllerutil.AddFinalizer(tenantEnv, tenantEnvironmentFinalizer)
		return ctrl.Result{Requeue: true}, r.Update(ctx, tenantEnv)
	}

	// Main reconciliation logic
	log.Info("Reconciling tenant", "uid", tenantEnv.UID, "displayName", tenantEnv.Spec.DisplayName)

	// Create namespace for tenant
	if err := CreateNamespaceForTenant(ctx, r.Client, tenantEnv, log); err != nil {
		log.Error(err, "Failed to create namespace")
		return ctrl.Result{}, err
	}

	// TODO: better handle resource quotas
	// Create ResourceQuota for tenant if specified
	// if tenantEnv.Spec.ResourceQuotas != nil {
	// 	if err := CreateResourceQuotaForTenant(ctx, r.Client, tenantEnv, log); err != nil {
	// 		log.Error(err, "Failed to create ResourceQuota")
	// 		return ctrl.Result{}, err
	// 	}
	// }

	// Create PostgreSQL database for tenant (only if not already assigned)
	if tenantEnv.Spec.Database.Status == "Unassigned" {
		tenantEnv.Spec.Database.Status = "Provisioning"
		if err := r.Update(ctx, tenantEnv); err != nil {
			log.Error(err, "Failed to update database status")
			return ctrl.Result{}, err
		}

		if err := CreatePostgreSQLForTenant(ctx, r.Client, tenantEnv, log); err != nil {
			log.Error(err, "Failed to create PostgreSQL database")
			return ctrl.Result{}, err
		}

		tenantEnv.Spec.Database.Status = "Assigned"
		if err := r.Update(ctx, tenantEnv); err != nil {
			log.Error(err, "Failed to update database status")
			return ctrl.Result{}, err
		}

		log.Info("Database assigned successfully", "tenant", tenantEnv.Name)
	} else {
		log.Info("Database already assigned, skipping...", "tenant", tenantEnv.Name)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TenantEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Add a runnable that will execute after the manager starts and cache is ready
	err := mgr.Add(&startupRunner{
		client: mgr.GetClient(),
	})
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&tenantv1.TenantEnvironment{}).
		Named("tenantenvironment").
		Complete(r)
}

type startupRunner struct {
	client client.Client
}

func (s *startupRunner) Start(ctx context.Context) error {
	log := logf.Log.WithName("tenantenvironment-startup")

	if err := ensureSharedServicesNamespace(ctx, s.client, log); err != nil {
		log.Error(err, "Failed to ensure shared services namespace on startup")
		return err
	}

	if err := ensureSharedPostgreSQLConfigMap(ctx, s.client, log); err != nil {
		log.Error(err, "Failed to ensure shared PostgreSQL config on startup")
		return err
	}

	log.Info("Startup initialization completed successfully")

	return nil
}
