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
	namespaceName := "tenant-" + tenantEnv.Spec.TenantID

	var namespace corev1.Namespace
	err := c.Get(ctx, types.NamespacedName{Name: namespaceName}, &namespace)

	if errors.IsNotFound(err) {
		namespace = corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespaceName,
				Labels: map[string]string{
					"tenant.core.mellifluus.io/tenant-id":     tenantEnv.Spec.TenantID,
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
