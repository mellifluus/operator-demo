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
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

func (r *TenantEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if strings.HasPrefix(req.Name, "pod-event:") {
		return r.handlePodEventFromMapping(ctx, req, log)
	} else if req.Namespace == "default" {
		log.Info("Reconciling TenantEnvironment", "name", req.Name)
	} else {
		return ctrl.Result{}, nil
	}

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

	// Create ResourceQuota for tenant if specified
	if tenantEnv.Spec.ResourceQuotas != nil {
		if err := CreateResourceQuotaForTenant(ctx, r.Client, tenantEnv, log); err != nil {
			log.Error(err, "Failed to create ResourceQuota")
			return ctrl.Result{}, err
		}
	}

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

	// Create tenant service deployment
	if err := CreateTenantServiceDeployment(ctx, r.Client, tenantEnv, log); err != nil {
		log.Error(err, "Failed to create tenant service deployment")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *TenantEnvironmentReconciler) handlePodEventFromMapping(ctx context.Context, req ctrl.Request, log logr.Logger) (ctrl.Result, error) {
	parts := strings.Split(req.Name, ":")
	if len(parts) != 3 {
		log.Error(nil, "Invalid pod event format", "name", req.Name)
		return ctrl.Result{}, nil
	}

	podName := parts[1]
	tenantEnvName := parts[2]

	tenantEnv, err := GetTenantEnvironment(ctx, r.Client, types.NamespacedName{
		Name:      tenantEnvName,
		Namespace: req.Namespace,
	})
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	tenantNamespace := "tenant-" + string(tenantEnv.UID)
	var pod corev1.Pod
	err = r.Get(ctx, types.NamespacedName{
		Name:      podName,
		Namespace: tenantNamespace,
	}, &pod)

	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	_, hasTenantLabel := pod.Labels["tenant.core.mellifluus.io/tenant-uid"]
	if !hasTenantLabel {
		return ctrl.Result{}, nil
	}

	podReady := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			podReady = true
			break
		}
	}

	if podReady {
		if tenantEnv.Status.ReadyAt == nil {
			now := metav1.Now()
			tenantEnv.Status.ReadyAt = &now
			if err := r.Status().Update(ctx, tenantEnv); err != nil {
				log.Error(err, "Failed to update TenantEnvironment readyAt status")
				return ctrl.Result{}, err
			}
			log.Info("✅ TenantEnvironment marked as ready!", "tenant", tenantEnv.Name, "readyAt", now)
		}
	}

	return ctrl.Result{}, nil
}

func (r *TenantEnvironmentReconciler) podToTenantEnvironment(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}

	// Check if this is a backend service pod
	if component, hasComponent := pod.Labels["tenant.core.mellifluus.io/component"]; !hasComponent || component != "backend-service" {
		return nil
	}

	tenantUID, hasTenantUID := pod.Labels["tenant.core.mellifluus.io/tenant-uid"]
	if !hasTenantUID {
		return nil
	}

	var tenantEnvList tenantv1.TenantEnvironmentList
	if err := r.List(ctx, &tenantEnvList, client.InNamespace("default")); err != nil {
		return nil
	}

	for _, tenantEnv := range tenantEnvList.Items {
		if string(tenantEnv.UID) == tenantUID {
			return []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      "pod-event:" + pod.Name + ":" + tenantEnv.Name,
						Namespace: tenantEnv.Namespace,
					},
				},
			}
		}
	}

	return nil
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
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.podToTenantEnvironment)).
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
