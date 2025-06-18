package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	processes         []*exec.Cmd
	promDataDir       = ".prom-tmp"
	sharedInstanceSem = make(chan struct{}, 1) // semaphore for shared psql instance access

	amount      int
	dedicatedDB bool
	rootDir     string
	runnerDir   = getRunnerDir()

	tenantProvisioningDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "tenant_provisioning_duration_seconds",
			Help:    "Time taken to provision a tenant from creation to ready",
			Buckets: []float64{5, 10, 30, 60, 120, 300, 600},
		},
		[]string{"dedicated_db"},
	)
)

var kubeClient *kubernetes.Clientset

func main() {
	flag.IntVar(&amount, "amount", 100, "Number of tenant environments to create")
	flag.BoolVar(&dedicatedDB, "dedicated-db", false, "Use dedicated DB")
	flag.Parse()

	setupSignalHandler()

	var err error
	rootDir, err = os.Getwd()
	if err != nil {
		fmt.Println("❌ Failed to get working directory:", err)
		os.Exit(1)
	}

	go func() {
		prometheus.MustRegister(tenantProvisioningDuration)

		http.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(":8082", nil); err != nil {
			fmt.Println("❌ Failed to start metrics server:", err)
		}
		fmt.Println("✅ Metrics server started on http://localhost:8082/metrics")
	}()

	runStep("Checking tenant-service image", func() error {
		return dockerImageOrBuild("tenant-service:latest", "make", "build-service")
	})

	runStep("Checking for existing kind cluster", func() error {
		if output, err := exec.Command("kind", "get", "clusters").Output(); err == nil && string(output) == "kind\n" {
			fmt.Println("Kind cluster exists, deleting...")
			return runCommand("kind", "delete", "cluster")
		}
		return nil
	})

	runStep("Creating kind cluster", func() error {
		return runCommand("kind", "create", "cluster", "--config", filepath.Join(rootDir, "test/kind-config.yaml"))
	})

	runStep("Initializing Kubernetes client", func() error {
		kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return fmt.Errorf("failed to load kubeconfig: %w", err)
		}

		kubeClient, err = kubernetes.NewForConfig(config)
		if err != nil {
			return fmt.Errorf("failed to create clientset: %w", err)
		}
		return nil
	})

	runStep("make load-service", func() error {
		return runCommand("make", "load-service")
	})

	runStep("Starting Prometheus", func() error {
		if err := os.MkdirAll(promDataDir, 0755); err != nil {
			return fmt.Errorf("failed to create prometheus temp dir: %w", err)
		}

		cmd := exec.Command("prometheus", "--config.file="+filepath.Join(runnerDir, "config", "prometheus.yml"), "--storage.tsdb.path="+promDataDir)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start prometheus: %w", err)
		}

		processes = append(processes, cmd)
		fmt.Printf("Started Prometheus [PID %d]\n", cmd.Process.Pid)
		return nil
	})

	runStep("Starting Grafana", func() error {
		provisioningPath := filepath.Join(runnerDir, "config", "grafana")
		cmd := exec.Command("grafana",
			"server",
			"--config=/opt/homebrew/etc/grafana/grafana.ini",
			"--homepath=/opt/homebrew/share/grafana",
			"--packaging=brew",
			"--configOverrides=cfg:default.paths.provisioning="+provisioningPath,
		)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start grafana: %w", err)
		}

		processes = append(processes, cmd)
		fmt.Printf("Started Grafana [PID %d]\n", cmd.Process.Pid)
		return nil
	})

	runStep("Initializing shared-services environment", func() error {
		return initEnvironment(context.Background())
	})

	runStep(fmt.Sprintf("Deploying %d tenants", amount), func() error {
		return deployTenants(amount, dedicatedDB)
	})

	fmt.Println("🕒 Deployments initialized. Observe Grafana at http://localhost:3000. Press Ctrl+C to clean up.")
	select {}
}

func runStep(name string, fn func() error) {
	fmt.Println("▶️", name)
	if err := fn(); err != nil {
		fmt.Println("❌", err)
		cleanupAndExit()
	}
}

func dockerImageOrBuild(image string, buildCmd ...string) error {
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		fmt.Printf("Image %s not found. Building...\n", image)
		return runCommand(buildCmd[0], buildCmd[1:]...)
	}
	fmt.Printf("Image %s found ✅\n", image)
	return nil
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func setupSignalHandler() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		fmt.Printf("\nReceived %s. Cleaning up...\n", sig)
		cleanupAndExit()
	}()
}

func cleanupAndExit() {
	for _, cmd := range processes {
		if cmd.Process != nil {
			fmt.Printf("Killing %s [PID %d]\n", cmd.Path, cmd.Process.Pid)
			_ = cmd.Process.Kill()
		}
	}

	fmt.Println("Deleting kind cluster...")
	_ = exec.Command("kind", "delete", "cluster").Run()

	if err := os.RemoveAll(promDataDir); err == nil {
		fmt.Println("Deleted Prometheus temp data")
	}

	fmt.Println("✅ Cleanup complete.")
	os.Exit(0)
}

func getRunnerDir() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		panic("could not get caller info")
	}
	return filepath.Dir(filename)
}

func generatePassword(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"

	bytes := make([]byte, length)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}

	for i := range bytes {
		bytes[i] = charset[bytes[i]%byte(len(charset))]
	}

	return string(bytes), nil
}

func generateId(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyz"

	bytes := make([]byte, length)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}

	for i := range bytes {
		bytes[i] = charset[bytes[i]%byte(len(charset))]
	}

	return string(bytes), nil
}

func renderTemplate(templateName string, data map[string]string) ([]byte, error) {
	tmplPath := filepath.Join(runnerDir, "templates", templateName)
	content, err := os.ReadFile(tmplPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read template %s: %w", tmplPath, err)
	}

	tmpl, err := template.New(templateName).Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.Bytes(), nil
}

func initEnvironment(ctx context.Context) error {
	// Create shared-services namespace
	nsYaml, err := renderTemplate("namespace.yaml", map[string]string{
		"Name": "shared-services",
	})
	if err != nil {
		return err
	}

	var ns corev1.Namespace
	if err := yaml.Unmarshal(nsYaml, &ns); err != nil {
		return fmt.Errorf("failed to unmarshal namespace: %w", err)
	}

	if _, err := kubeClient.CoreV1().Namespaces().Create(ctx, &ns, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create namespace: %w", err)
	}
	fmt.Println("✅ Created namespace:", ns.Name)

	// Create PostgreSQL ConfigMap
	cmYaml, err := renderTemplate("psql-config-map.yaml", map[string]string{
		"Name":      "postgresql-config",
		"Namespace": ns.Name,
	})
	if err != nil {
		return err
	}

	var cm corev1.ConfigMap
	if err := yaml.Unmarshal(cmYaml, &cm); err != nil {
		return fmt.Errorf("failed to unmarshal configmap: %w", err)
	}

	if _, err := kubeClient.CoreV1().ConfigMaps(ns.Name).Create(ctx, &cm, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create configmap: %w", err)
	}
	fmt.Println("✅ Created ConfigMap:", cm.Name)

	return nil
}

func assignSharedInstance(ctx context.Context, tenantId string) (string, error) {
	sharedInstanceSem <- struct{}{}
	defer func() { <-sharedInstanceSem }()

	for {
		ssList, err := kubeClient.AppsV1().StatefulSets("shared-services").List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", fmt.Errorf("failed to list StatefulSets: %w", err)
		}

		for _, ss := range ssList.Items {
			if ss.Annotations == nil {
				continue
			}

			tenantListStr := ss.Annotations["tenant-list"]
			countStr := ss.Annotations["tenant-count"]

			count, err := strconv.Atoi(countStr)
			if err != nil || count >= 10 {
				continue
			}

			var tenantList []string
			if tenantListStr != "" {
				tenantList = strings.Split(tenantListStr, ",")
			}

			for _, existing := range tenantList {
				if existing == tenantId {
					return ss.Name, nil
				}
			}

			tenantList = append(tenantList, tenantId)
			newListStr := strings.Join(tenantList, ",")
			newCountStr := strconv.Itoa(len(tenantList))

			latest, err := kubeClient.AppsV1().StatefulSets("shared-services").Get(ctx, ss.Name, metav1.GetOptions{})
			if err != nil {
				continue
			}

			if latest.Annotations == nil {
				latest.Annotations = make(map[string]string)
			}

			latest.Annotations["tenant-list"] = newListStr
			latest.Annotations["tenant-count"] = newCountStr

			_, err = kubeClient.AppsV1().StatefulSets("shared-services").Update(ctx, latest, metav1.UpdateOptions{})
			if err != nil {
				if apierrors.IsConflict(err) {
					time.Sleep(100 * time.Millisecond)
					continue
				}
				return "", fmt.Errorf("failed to update statefulset: %w", err)
			}

			return ss.Name, nil
		}

		time.Sleep(200 * time.Millisecond)
	}
}

func deployTenants(amount int, dedicated bool) error {
	var wg sync.WaitGroup

	for i := 0; i < amount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ctx := context.Background()
			provisionTenant(ctx, index, dedicated)
		}(i)
	}

	wg.Wait()
	fmt.Println("✅ All tenants deployed")
	return nil
}

func provisionTenant(ctx context.Context, index int, dedicated bool) {
	start := time.Now()
	tenantId := uuid.New().String()
	tenantNamespaceName := "tenant-" + tenantId

	// TENANT NAMESPACE
	nsYaml, err := renderTemplate("namespace.yaml", map[string]string{
		"Name": tenantNamespaceName,
	})
	if err != nil {
		fmt.Printf("❌ [%s] Failed to render namespace template: %v\n", tenantId, err)
		return
	}

	var ns corev1.Namespace
	if err := yaml.Unmarshal(nsYaml, &ns); err != nil {
		fmt.Printf("❌ [%s] Failed to unmarshal namespace YAML: %v\n", tenantId, err)
		return
	}

	_, err = kubeClient.CoreV1().Namespaces().Create(ctx, &ns, metav1.CreateOptions{})
	if err != nil {
		fmt.Printf("❌ [%s] Failed to create namespace: %v\n", tenantId, err)
		return
	}

	//TENANT RESOURCE QUOTA
	rqYaml, err := renderTemplate("resource-quota.yaml", map[string]string{
		"Namespace": tenantNamespaceName,
	})
	if err != nil {
		fmt.Printf("❌ [%s] Failed to render resource quota template: %v\n", tenantId, err)
		return
	}

	var rq corev1.ResourceQuota
	if err := yaml.Unmarshal(rqYaml, &rq); err != nil {
		fmt.Printf("❌ [%s] Failed to unmarshal resource quota YAML: %v\n", tenantId, err)
		return
	}
	if _, err := kubeClient.CoreV1().ResourceQuotas(tenantNamespaceName).Create(ctx, &rq, metav1.CreateOptions{}); err != nil {
		fmt.Printf("❌ [%s] Failed to create resource quota: %v\n", tenantId, err)
		return
	}

	var instanceName string

	// POSTGRES
	if index%10 == 0 {
		// Create shared instance
		instanceId, err := generateId(6)
		if err != nil {
			fmt.Printf("❌ [%s] Failed to generate instance ID: %v\n", tenantId, err)
			return
		}
		instanceName = "postgresql-" + instanceId

		masterPassword, err := generatePassword(24)
		if err != nil {
			fmt.Printf("❌ [%s] Failed to generate master password: %v\n", tenantId, err)
			return
		}

		msYaml, err := renderTemplate("shared-postgresql-master-secret.yaml", map[string]string{
			"Id":       instanceId,
			"Password": masterPassword,
		})
		if err != nil {
			fmt.Printf("❌ [%s] Failed to render master secret template: %v\n", tenantId, err)
			return
		}
		var ms corev1.Secret
		if err := yaml.Unmarshal(msYaml, &ms); err != nil {
			fmt.Printf("❌ [%s] Failed to unmarshal master secret YAML: %v\n", tenantId, err)
			return
		}

		if _, err := kubeClient.CoreV1().Secrets(ms.Namespace).Create(ctx, &ms, metav1.CreateOptions{}); err != nil {
			fmt.Printf("❌ [%s] Failed to create PostgreSQL instance master secret: %v\n", tenantId, err)
			return
		}

		// STATEFULSET
		ssYaml, err := renderTemplate("psql-statefulset.yaml", map[string]string{
			"Id":        instanceId,
			"Namespace": "shared-services",
			"TenantId":  tenantId,
		})
		if err != nil {
			fmt.Printf("❌ [%s] Failed to render statefulset template: %v\n", tenantId, err)
			return
		}

		var ss appsv1.StatefulSet
		if err := yaml.Unmarshal(ssYaml, &ss); err != nil {
			fmt.Printf("❌ [%s] Failed to unmarshal statefulset YAML: %v\n", tenantId, err)
			return
		}
		if _, err := kubeClient.AppsV1().StatefulSets(ss.Namespace).Create(ctx, &ss, metav1.CreateOptions{}); err != nil {
			fmt.Printf("❌ [%s] Failed to create PostgreSQL instance statefulset: %v\n", tenantId, err)
			return
		}

		// SERVICE
		svcYaml, err := renderTemplate("psql-service.yaml", map[string]string{
			"Id":        instanceId,
			"Namespace": "shared-services",
		})
		if err != nil {
			fmt.Printf("❌ [%s] Failed to render service template: %v\n", tenantId, err)
			return
		}
		var svc corev1.Service
		if err := yaml.Unmarshal(svcYaml, &svc); err != nil {
			fmt.Printf("❌ [%s] Failed to unmarshal service YAML: %v\n", tenantId, err)
			return
		}
		if _, err := kubeClient.CoreV1().Services(svc.Namespace).Create(ctx, &svc, metav1.CreateOptions{}); err != nil {
			fmt.Printf("❌ [%s] Failed to create PostgreSQL instance service: %v\n", tenantId, err)
			return
		}
	} else {
		// Leech off shared instance
		instanceName, err = assignSharedInstance(ctx, tenantId)
		if err != nil {
			fmt.Printf("❌ [%s] Failed to assign shared instance: %v\n", tenantId, err)
			return
		}
	}

	// TENANT DATABASE CONFIG
	tenantDbPassword, err := generatePassword(24)
	if err != nil {
		fmt.Printf("❌ [%s] Failed to generate tenant database password: %v\n", tenantId, err)
		return
	}

	tsYaml, err := renderTemplate("psql-tenant-secret.yaml", map[string]string{
		"Namespace": tenantNamespaceName,
		"Host":      instanceName + ".shared-services.svc.cluster.local",
		"Port":      "5432",
		"DbName":    tenantNamespaceName,
		"Username":  tenantNamespaceName,
		"Password":  tenantDbPassword,
	})
	if err != nil {
		fmt.Printf("❌ [%s] Failed to render tenant secret template: %v\n", tenantId, err)
		return
	}
	var ts corev1.Secret
	if err := yaml.Unmarshal(tsYaml, &ts); err != nil {
		fmt.Printf("❌ [%s] Failed to unmarshal tenant secret YAML: %v\n", tenantId, err)
		return
	}
	if _, err := kubeClient.CoreV1().Secrets(tenantNamespaceName).Create(ctx, &ts, metav1.CreateOptions{}); err != nil {
		fmt.Printf("❌ [%s] Failed to create tenant database secret: %v\n", tenantId, err)
		return
	}

	dbInitYaml, err := renderTemplate("psql-init-job.yaml", map[string]string{
		"Namespace": "shared-services",
		"TenantId":  tenantId,
		"Id":        instanceName[len(instanceName)-6:],
		"Host":      ts.StringData["DB_HOST"],
		"Username":  ts.StringData["DB_USERNAME"],
		"DbName":    ts.StringData["DB_NAME"],
		"Password":  ts.StringData["DB_PASSWORD"],
	})
	if err != nil {
		fmt.Printf("❌ [%s] Failed to render init job template: %v\n", tenantId, err)
		return
	}
	var job batchv1.Job
	if err := yaml.Unmarshal(dbInitYaml, &job); err != nil {
		fmt.Printf("❌ [%s] Failed to unmarshal job YAML: %v\n", tenantId, err)
		return
	}
	if _, err := kubeClient.BatchV1().Jobs("shared-services").Create(ctx, &job, metav1.CreateOptions{}); err != nil {
		fmt.Printf("❌ [%s] Failed to create tenant database init job: %v\n", tenantId, err)
		return
	}

	// TENANT DEPLOYMENT
	deploymentYaml, err := renderTemplate("tenant-service-deployment.yaml", map[string]string{
		"Namespace": tenantNamespaceName,
	})
	if err != nil {
		fmt.Printf("❌ [%s] Failed to render deployment template: %v\n", tenantId, err)
		return
	}
	var deployment appsv1.Deployment
	if err := yaml.Unmarshal(deploymentYaml, &deployment); err != nil {
		fmt.Printf("❌ [%s] Failed to unmarshal deployment YAML: %v\n", tenantId, err)
		return
	}
	if _, err := kubeClient.AppsV1().Deployments(tenantNamespaceName).Create(ctx, &deployment, metav1.CreateOptions{}); err != nil {
		fmt.Printf("❌ [%s] Failed to create tenant deployment: %v\n", tenantId, err)
		return
	}

	for {
		pods, err := kubeClient.CoreV1().Pods(tenantNamespaceName).List(ctx, metav1.ListOptions{
			LabelSelector: "app=backend",
		})
		if err != nil {
			fmt.Printf("❌ [%s] Failed to list pods: %v\n", tenantId, err)
			return
		}

		if len(pods.Items) > 0 {
			pod := pods.Items[0]
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					end := time.Now()
					duration := end.Sub(start).Seconds()
					tenantProvisioningDuration.WithLabelValues(strconv.FormatBool(dedicated)).Observe(duration)
					return
				}
			}
		}

		// Wait and retry
		time.Sleep(100 * time.Millisecond)
	}
}
