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

func (r *TenantEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	tenantEnv, err := GetTenantEnvironment(ctx, r.Client, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !tenantEnv.ObjectMeta.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(tenantEnv, tenantEnvironmentFinalizer) {
			log.Info("Cleaning up tenant", "tenantId", tenantEnv.Spec.TenantID)

			// Delete namespace
			if err := DeleteNamespaceForTenant(ctx, r.Client, tenantEnv.Spec.TenantID, log); err != nil {
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
	log.Info("Reconciling tenant", "tenantId", tenantEnv.Spec.TenantID)

	// Create namespace for tenant
	if err := CreateNamespaceForTenant(ctx, r.Client, tenantEnv, log); err != nil {
		log.Error(err, "Failed to create namespace")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TenantEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&tenantv1.TenantEnvironment{}).
		Named("tenantenvironment").
		Complete(r)
}
