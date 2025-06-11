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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/mellifluus/operator-demo.git/test/utils"
)

// namespace where the project is deployed in
const namespace = "operator-demo-system"

// serviceAccountName created for the project
const serviceAccountName = "operator-demo-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "operator-demo-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "operator-demo-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=operator-demo-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints", metricsServiceName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
          "spec": {
            "containers": [{
              "name": "curl",
              "image": "curlimages/curl:latest",
              "command": ["/bin/sh", "-c"],
              "args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
              "securityContext": {
                "allowPrivilegeEscalation": false,
                "capabilities": {
                  "drop": ["ALL"]
                },
                "runAsNonRoot": true,
                "runAsUser": 1000,
                "seccompProfile": {
                  "type": "RuntimeDefault"
                }
              }
            }],
            "serviceAccount": "%s"
          }
        }`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := getMetricsOutput()
			Expect(metricsOutput).To(ContainSubstring(
				"controller_runtime_reconcile_total",
			))
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		// TODO: Customize the e2e test suite with scenarios specific to your project.
		// Consider applying sample/CR(s) and check their status and/or verifying
		// the reconciliation by using the metrics, i.e.:
		// metricsOutput := getMetricsOutput()
		// Expect(metricsOutput).To(ContainSubstring(
		//    fmt.Sprintf(`controller_runtime_reconcile_total{controller="%s",result="success"} 1`,
		//    strings.ToLower(<Kind>),
		// ))
	})

	// TenantEnvironment End-to-End Tests
	Context("TenantEnvironment operations", func() {
		var tenantName string
		var tenantUID string

		BeforeEach(func() {
			// Generate unique tenant name for each test
			tenantName = fmt.Sprintf("test-tenant-%d", time.Now().Unix())
		})

		AfterEach(func() {
			// Clean up the TenantEnvironment after each test
			if tenantName != "" {
				By("deleting the TenantEnvironment CR")
				cmd := exec.Command("kubectl", "delete", "tenantenvironment", tenantName, "--ignore-not-found=true")
				_, _ = utils.Run(cmd)

				// Wait for tenant namespace to be deleted
				if tenantUID != "" {
					tenantNamespace := fmt.Sprintf("tenant-%s", tenantUID)
					By(fmt.Sprintf("waiting for tenant namespace %s to be deleted", tenantNamespace))
					Eventually(func() bool {
						cmd := exec.Command("kubectl", "get", "namespace", tenantNamespace)
						_, err := utils.Run(cmd)
						return err != nil // namespace should not exist (error expected)
					}, 2*time.Minute, 5*time.Second).Should(BeTrue())
				}
			}
		})

		It("should create a tenant namespace when TenantEnvironment is created", func() {
			// Create TenantEnvironment YAML
			tenantYAML := fmt.Sprintf(`
        apiVersion: tenant.core.mellifluus.io/v1
        kind: TenantEnvironment
        metadata:
          name: %s
        spec:
          displayName: "Test Tenant"
          applicationImage: "nginx:latest"
          applicationPort: 80
          replicas: 1
          database:
            dedicatedInstance: false
            performanceTier: "standard"
          resourceQuotas:
            cpuLimit: "500m"
            memoryLimit: "1Gi"
            storageLimit: "5Gi"
            podLimit: 3
      `, tenantName)

			// Write to temporary file
			tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("%s.yaml", tenantName))
			err := os.WriteFile(tmpFile, []byte(tenantYAML), 0644)
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(tmpFile)

			By("creating the TenantEnvironment CR")
			cmd := exec.Command("kubectl", "apply", "-f", tmpFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create TenantEnvironment")

			By("waiting for TenantEnvironment to get UID")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "tenantenvironment", tenantName, "-o", "jsonpath={.metadata.uid}")
				uid, err := utils.Run(cmd)
				if err != nil {
					return ""
				}
				tenantUID = strings.TrimSpace(uid)
				return tenantUID
			}, 30*time.Second, 2*time.Second).ShouldNot(BeEmpty())

			tenantNamespace := fmt.Sprintf("tenant-%s", tenantUID)

			By(fmt.Sprintf("verifying that tenant namespace %s is created", tenantNamespace))
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "namespace", tenantNamespace)
				_, err := utils.Run(cmd)
				return err
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying namespace has correct labels")
			cmd = exec.Command("kubectl", "get", "namespace", tenantNamespace, "-o", "jsonpath={.metadata.labels}")
			labelsOutput, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			// Check for expected labels
			Expect(labelsOutput).To(ContainSubstring("tenant.core.mellifluus.io/tenant-uid"))
			Expect(labelsOutput).To(ContainSubstring("tenant.core.mellifluus.io/managed-by"))
			Expect(labelsOutput).To(ContainSubstring(tenantUID))

			By("verifying ResourceQuota is created in tenant namespace")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "resourcequota", "tenant-quota", "-n", tenantNamespace)
				_, err := utils.Run(cmd)
				return err
			}, 1*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying ResourceQuota has correct limits")
			cmd = exec.Command("kubectl", "get", "resourcequota", "tenant-quota", "-n", tenantNamespace, "-o", "jsonpath={.spec.hard}")
			quotaOutput, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(quotaOutput).To(ContainSubstring("500m")) // CPU limit
			Expect(quotaOutput).To(ContainSubstring("1Gi"))  // Memory limit
		})

		It("should clean up tenant namespace when TenantEnvironment is deleted", func() {
			// Create TenantEnvironment YAML
			tenantYAML := fmt.Sprintf(`
        apiVersion: tenant.core.mellifluus.io/v1
        kind: TenantEnvironment
        metadata:
          name: %s
        spec:
          displayName: "Test Tenant for Deletion"
          applicationImage: "nginx:latest"
          applicationPort: 80
          replicas: 1
          database:
            dedicatedInstance: false
            performanceTier: "standard"
          resourceQuotas:
            cpuLimit: "200m"
            memoryLimit: "512Mi"
            storageLimit: "2Gi"
            podLimit: 2
      `, tenantName)

			// Write to temporary file
			tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("%s.yaml", tenantName))
			err := os.WriteFile(tmpFile, []byte(tenantYAML), 0644)
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(tmpFile)

			By("creating the TenantEnvironment CR")
			cmd := exec.Command("kubectl", "apply", "-f", tmpFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for TenantEnvironment to get UID")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "tenantenvironment", tenantName, "-o", "jsonpath={.metadata.uid}")
				uid, err := utils.Run(cmd)
				if err != nil {
					return ""
				}
				tenantUID = strings.TrimSpace(uid)
				return tenantUID
			}, 30*time.Second, 2*time.Second).ShouldNot(BeEmpty())

			tenantNamespace := fmt.Sprintf("tenant-%s", tenantUID)

			By("waiting for tenant namespace to be created")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "namespace", tenantNamespace)
				_, err := utils.Run(cmd)
				return err
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("deleting the TenantEnvironment CR")
			cmd = exec.Command("kubectl", "delete", "tenantenvironment", tenantName)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying tenant namespace is deleted")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "namespace", tenantNamespace)
				_, err := utils.Run(cmd)
				return err != nil // should return error when namespace doesn't exist
			}, 3*time.Minute, 5*time.Second).Should(BeTrue())
			// Reset tenantName so AfterEach doesn't try to clean up again
			tenantName = ""
			tenantUID = ""
		})

		It("should assign non-enterprise tenants to shared database correctly", func() {
			var tenant1Name, tenant2Name string
			var tenant1UID, tenant2UID string

			// Generate unique tenant names
			timestamp := time.Now().Unix()
			tenant1Name = fmt.Sprintf("shared-tenant-1-%d", timestamp)
			tenant2Name = fmt.Sprintf("shared-tenant-2-%d", timestamp)

			defer func() {
				// Clean up both tenants
				if tenant1Name != "" {
					By("cleaning up first tenant")
					cmd := exec.Command("kubectl", "delete", "tenantenvironment", tenant1Name, "--ignore-not-found=true")
					_, _ = utils.Run(cmd)
				}
				if tenant2Name != "" {
					By("cleaning up second tenant")
					cmd := exec.Command("kubectl", "delete", "tenantenvironment", tenant2Name, "--ignore-not-found=true")
					_, _ = utils.Run(cmd)
				}
				// Wait for tenant namespaces to be deleted
				if tenant1UID != "" {
					tenant1Namespace := fmt.Sprintf("tenant-%s", tenant1UID)
					Eventually(func() bool {
						cmd := exec.Command("kubectl", "get", "namespace", tenant1Namespace)
						_, err := utils.Run(cmd)
						return err != nil
					}, 2*time.Minute, 5*time.Second).Should(BeTrue())
				}
				if tenant2UID != "" {
					tenant2Namespace := fmt.Sprintf("tenant-%s", tenant2UID)
					Eventually(func() bool {
						cmd := exec.Command("kubectl", "get", "namespace", tenant2Namespace)
						_, err := utils.Run(cmd)
						return err != nil
					}, 2*time.Minute, 5*time.Second).Should(BeTrue())
				}
			}()

			// Create first non-enterprise tenant
			tenant1YAML := fmt.Sprintf(`
        apiVersion: tenant.core.mellifluus.io/v1
        kind: TenantEnvironment
        metadata:
          name: %s
        spec:
          displayName: "Shared Test Tenant 1"
          applicationImage: "nginx:latest"
          applicationPort: 80
          replicas: 1
          database:
            dedicatedInstance: false
            performanceTier: "standard"
          resourceQuotas:
            cpuLimit: "200m"
            memoryLimit: "512Mi"
            storageLimit: "2Gi"
            podLimit: 2
      `, tenant1Name)

			// Create second non-enterprise tenant
			tenant2YAML := fmt.Sprintf(`
        apiVersion: tenant.core.mellifluus.io/v1
        kind: TenantEnvironment
        metadata:
          name: %s
        spec:
          displayName: "Shared Test Tenant 2"
          applicationImage: "nginx:latest"
          applicationPort: 80
          replicas: 1
          database:
            dedicatedInstance: false
            performanceTier: "standard"
          resourceQuotas:
            cpuLimit: "200m"
            memoryLimit: "512Mi"
            storageLimit: "2Gi"
            podLimit: 2
      `, tenant2Name)

			// Write tenant YAML files
			tmpFile1 := filepath.Join(os.TempDir(), fmt.Sprintf("%s.yaml", tenant1Name))
			tmpFile2 := filepath.Join(os.TempDir(), fmt.Sprintf("%s.yaml", tenant2Name))

			err := os.WriteFile(tmpFile1, []byte(tenant1YAML), 0644)
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(tmpFile1)

			err = os.WriteFile(tmpFile2, []byte(tenant2YAML), 0644)
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(tmpFile2)

			By("creating the first TenantEnvironment CR")
			cmd := exec.Command("kubectl", "apply", "-f", tmpFile1)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create first TenantEnvironment")

			By("creating the second TenantEnvironment CR")
			cmd = exec.Command("kubectl", "apply", "-f", tmpFile2)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create second TenantEnvironment")

			By("waiting for both TenantEnvironments to get UIDs")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "tenantenvironment", tenant1Name, "-o", "jsonpath={.metadata.uid}")
				uid, err := utils.Run(cmd)
				if err != nil {
					return ""
				}
				tenant1UID = strings.TrimSpace(uid)
				return tenant1UID
			}, 30*time.Second, 2*time.Second).ShouldNot(BeEmpty())

			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "tenantenvironment", tenant2Name, "-o", "jsonpath={.metadata.uid}")
				uid, err := utils.Run(cmd)
				if err != nil {
					return ""
				}
				tenant2UID = strings.TrimSpace(uid)
				return tenant2UID
			}, 30*time.Second, 2*time.Second).ShouldNot(BeEmpty())

			// 1. Check that shared-services namespace is created
			By("verifying that shared-services namespace is created")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "namespace", "shared-services")
				_, err := utils.Run(cmd)
				return err
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			// 2. Check that shared-postgresql StatefulSet is created and wait for it to be ready
			By("verifying that shared-postgresql StatefulSet is created")
			var statefulSetName string
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "statefulsets", "-n", "shared-services", "-l", "app=shared-postgresql", "-o", "jsonpath={.items[0].metadata.name}")
				name, err := utils.Run(cmd)
				if err != nil {
					return ""
				}
				statefulSetName = strings.TrimSpace(name)
				return statefulSetName
			}, 2*time.Minute, 5*time.Second).ShouldNot(BeEmpty())

			By(fmt.Sprintf("waiting for StatefulSet %s to be ready", statefulSetName))
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "statefulset", statefulSetName, "-n", "shared-services", "-o", "jsonpath={.status.readyReplicas}")
				replicas, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				return strings.TrimSpace(replicas) == "1"
			}, 5*time.Minute, 10*time.Second).Should(BeTrue())

			// 3. Check that shared-services namespace has two completed jobs with tenant UID prefixes
			By("verifying that two db-init jobs are completed")
			tenant1Prefix := tenant1UID[:8]
			tenant2Prefix := tenant2UID[:8]

			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "jobs", "-n", "shared-services", "-o", "json")
				output, err := utils.Run(cmd)
				if err != nil {
					return false
				}

				var jobList struct {
					Items []struct {
						Metadata struct {
							Name string `json:"name"`
						} `json:"metadata"`
						Status struct {
							Succeeded int `json:"succeeded"`
						} `json:"status"`
					} `json:"items"`
				}

				if err := json.Unmarshal([]byte(output), &jobList); err != nil {
					return false
				}

				foundTenant1Job := false
				foundTenant2Job := false

				for _, job := range jobList.Items {
					if strings.HasPrefix(job.Metadata.Name, fmt.Sprintf("db-init-%s", tenant1Prefix)) && job.Status.Succeeded > 0 {
						foundTenant1Job = true
					}
					if strings.HasPrefix(job.Metadata.Name, fmt.Sprintf("db-init-%s", tenant2Prefix)) && job.Status.Succeeded > 0 {
						foundTenant2Job = true
					}
				}

				return foundTenant1Job && foundTenant2Job
			}, 5*time.Minute, 10*time.Second).Should(BeTrue())

			// 4. Check that StatefulSet has tenant-count label set to "2"
			By("verifying that StatefulSet has tenant-count label set to '2'")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "statefulset", statefulSetName, "-n", "shared-services", "-o", "jsonpath={.metadata.labels.tenant\\.core\\.mellifluus\\.io/tenant-count}")
				count, err := utils.Run(cmd)
				if err != nil {
					return ""
				}
				return strings.TrimSpace(count)
			}, 2*time.Minute, 5*time.Second).Should(Equal("2"))

			// 5. Check that StatefulSet has tenant-list annotation containing both tenant UIDs
			By("verifying that StatefulSet has tenant-list annotation containing both tenant UIDs")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "statefulset", statefulSetName, "-n", "shared-services", "-o", "jsonpath={.metadata.annotations.tenant\\.core\\.mellifluus\\.io/tenant-list}")
				tenantList, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				tenantListStr := strings.TrimSpace(tenantList)
				return strings.Contains(tenantListStr, tenant1UID) && strings.Contains(tenantListStr, tenant2UID)
			}, 2*time.Minute, 5*time.Second).Should(BeTrue())

			// 6. Check that both tenant namespaces contain database-secret
			tenant1Namespace := fmt.Sprintf("tenant-%s", tenant1UID)
			tenant2Namespace := fmt.Sprintf("tenant-%s", tenant2UID)

			By(fmt.Sprintf("verifying that tenant namespace %s contains database-secret", tenant1Namespace))
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "secret", "database-secret", "-n", tenant1Namespace)
				_, err := utils.Run(cmd)
				return err
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By(fmt.Sprintf("verifying that tenant namespace %s contains database-secret", tenant2Namespace))
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "secret", "database-secret", "-n", tenant2Namespace)
				_, err := utils.Run(cmd)
				return err
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying database-secret contains expected keys")
			for _, namespace := range []string{tenant1Namespace, tenant2Namespace} {
				cmd := exec.Command("kubectl", "get", "secret", "database-secret", "-n", namespace, "-o", "jsonpath={.data}")
				secretData, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred())
				Expect(secretData).To(ContainSubstring("DB_HOST"))
				Expect(secretData).To(ContainSubstring("DB_PORT"))
				Expect(secretData).To(ContainSubstring("DB_NAME"))
				Expect(secretData).To(ContainSubstring("DB_USERNAME"))
				Expect(secretData).To(ContainSubstring("DB_PASSWORD"))
				Expect(secretData).To(ContainSubstring("DB_TYPE"))
			}
		})

		It("should assign enterprise tenants a dedicated database correctly", func() {
			// Create enterprise tenant
			tenantName := "test-enterprise"

			// Create the enterprise TenantEnvironment
			tenantYAML := fmt.Sprintf(`
        apiVersion: tenant.core.mellifluus.io/v1
        kind: TenantEnvironment
        metadata:
          name: %s
        spec:
          displayName: "Enterprise Bozo"
          applicationImage: "nginx:latest"
          applicationPort: 80
          replicas: 1
          database:
            dedicatedInstance: true
            performanceTier: "premium"
          resourceQuotas:
            cpuLimit: "200m"
            memoryLimit: "512Mi"
            storageLimit: "2Gi"
            podLimit: 2
      `, tenantName)

			// Write to temporary file
			tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("enterprise-tenant-%d.yaml", time.Now().Unix()))
			err := os.WriteFile(tmpFile, []byte(tenantYAML), 0644)
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(tmpFile)

			By("Creating enterprise tenant namespace and TenantEnvironment")
			cmd := exec.Command("kubectl", "apply", "-f", tmpFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			var tenantUID string

			By("waiting for TenantEnvironment to get UIDs")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "tenantenvironment", tenantName, "-o", "jsonpath={.metadata.uid}")
				uid, err := utils.Run(cmd)
				if err != nil {
					return ""
				}
				tenantUID = strings.TrimSpace(uid)
				return tenantUID
			}, 30*time.Second, 2*time.Second).ShouldNot(BeEmpty())

			tenantNamespace := fmt.Sprintf("tenant-%s", tenantUID)

			// Wait for the StatefulSet to be ready
			By("verifying that postgresql StatefulSet is created")
			var statefulSetName string
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "statefulsets", "-n", tenantNamespace, "-l", "app=postgresql", "-o", "jsonpath={.items[0].metadata.name}")
				name, err := utils.Run(cmd)
				if err != nil {
					return ""
				}
				statefulSetName = strings.TrimSpace(name)
				return statefulSetName
			}, 2*time.Minute, 5*time.Second).ShouldNot(BeEmpty())

			// Verify that the StatefulSet is labeled with tenant information
			// By("Checking StatefulSet tenant labels and annotations")
			// cmd = exec.Command("kubectl", "get", "statefulset", "postgresql", "-n", tenantNamespace, "-o", "json")
			// output, err := utils.Run(cmd)
			// Expect(err).NotTo(HaveOccurred())

			// var statefulSet map[string]interface{}
			// err = json.Unmarshal([]byte(output), &statefulSet)
			// Expect(err).NotTo(HaveOccurred())

			// metadata := statefulSet["metadata"].(map[string]interface{})
			// labels := metadata["labels"].(map[string]interface{})
			// annotations := metadata["annotations"].(map[string]interface{})

			// // Verify tenant labels
			// Expect(labels["app.kubernetes.io/component"]).To(Equal("database"))
			// Expect(labels["tenant-uid"]).To(Equal(tenantUID))
			// Expect(labels["tenant-type"]).To(Equal("enterprise"))

			// // Verify annotations contain tenant information
			// Expect(annotations["tenant-namespace"]).To(Equal(tenantNamespace))

			// Wait for database initialization job to complete
			By("Waiting for database initialization job to complete")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "jobs", "-n", tenantNamespace, "-l", "job-type=db-init", "-o", "json")
				output, err := utils.Run(cmd)
				if err != nil {
					return false
				}

				var jobList map[string]interface{}
				if err := json.Unmarshal([]byte(output), &jobList); err != nil {
					return false
				}

				items, ok := jobList["items"].([]interface{})
				if !ok || len(items) == 0 {
					return false
				}

				job := items[0].(map[string]interface{})
				status, ok := job["status"].(map[string]interface{})
				if !ok {
					return false
				}

				succeeded, ok := status["succeeded"]
				return ok && succeeded != nil && succeeded.(float64) == 1
			}).Should(BeTrue())

			// Verify the database secret is created in tenant namespace
			By("Checking that database secret is created in tenant namespace")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "secret", "postgresql-secret", "-n", tenantNamespace, "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				return strings.TrimSpace(output) == "postgresql-secret"
			}).Should(BeTrue())

			// Verify the secret contains the expected database connection details
			By("Verifying database secret contains connection details")
			cmd = exec.Command("kubectl", "get", "secret", "postgresql-secret", "-n", tenantNamespace, "-o", "yaml")
			secretData, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(secretData).To(ContainSubstring("DB_HOST"))
			Expect(secretData).To(ContainSubstring("DB_PORT"))
			Expect(secretData).To(ContainSubstring("DB_NAME"))
			Expect(secretData).To(ContainSubstring("DB_USERNAME"))
			Expect(secretData).To(ContainSubstring("DB_PASSWORD"))
			Expect(secretData).To(ContainSubstring("DB_TYPE"))

			// Cleanup - delete the tenant resources
			By("Cleaning up enterprise tenant resources")
			cleanupCmd := exec.Command("kubectl", "delete", "namespace", tenantNamespace, "--ignore-not-found=true")
			_, _ = utils.Run(cleanupCmd)
		})
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
    "apiVersion": "authentication.k8s.io/v1",
    "kind": "TokenRequest"
  }`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	metricsOutput, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
