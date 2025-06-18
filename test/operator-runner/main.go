package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var processes []*exec.Cmd

const promDataDir = ".prom-tmp"

var (
	amount      int
	dedicatedDB bool
)

func main() {
	flag.IntVar(&amount, "amount", 100, "Number of tenant environments to create")
	flag.BoolVar(&dedicatedDB, "dedicated-db", false, "Use dedicated DB")
	flag.Parse()

	setupSignalHandler()

	wd, err := os.Getwd()
	if err != nil {
		fmt.Println("❌ Failed to get working directory:", err)
		os.Exit(1)
	}

	runStep("Checking controller image", func() error {
		return dockerImageOrBuild("controller:latest", "make", "build-controller")
	})

	runStep("Checking tenant-service image", func() error {
		return dockerImageOrBuild("tenant-service:latest", "make", "build-service")
	})

	runStep("Checking for existing kind cluster", func() error {
		cmd := exec.Command("kind", "get", "clusters")
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to check kind clusters: %w", err)
		}
		if string(output) == "kind\n" {
			fmt.Println("Kind cluster exists, deleting...")
			return runCommand("kind", "delete", "cluster")
		}
		return nil
	})

	runStep("Creating kind cluster", func() error {
		return runCommand("kind", "create", "cluster", "--config", wd+"/test/kind-config.yaml")
	})

	runStep("make install", func() error {
		return runCommand("make", "install")
	})

	runStep("make load-service", func() error {
		return runCommand("make", "load-service")
	})

	runStep("make load-controller", func() error {
		return runCommand("make", "load-controller")
	})

	runStep("make deploy", func() error {
		return runCommand("make", "deploy")
	})

	runStep("Expose controller metrics via NodePort", func() error {
		path := wd + "/test/operator-runner/config/controller-metrics-nodeport.yaml"
		return runCommand("kubectl", "apply", "-f", path)
	})

	runStep("Waiting for controller pod to become ready", func() error {
		return runCommand(
			"kubectl", "wait",
			"--namespace", "operator-demo-system",
			"--for=condition=Ready",
			"--timeout=60s",
			"pod",
			"-l", "control-plane=controller-manager",
		)
	})

	runStep("Starting Prometheus", func() error {
		if err := os.MkdirAll(promDataDir, 0755); err != nil {
			return fmt.Errorf("failed to create prometheus temp dir: %w", err)
		}

		cmd := exec.Command("prometheus", "--config.file=prometheus.yml", "--storage.tsdb.path="+promDataDir)
		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start prometheus: %w", err)
		}
		processes = append(processes, cmd)
		fmt.Printf("Started Prometheus [PID %d]\n", cmd.Process.Pid)
		return nil
	})

	runStep("Starting Grafana", func() error {
		provisioningPath := wd + "/grafana"

		cmd := exec.Command("grafana",
			"server",
			"--config=/opt/homebrew/etc/grafana/grafana.ini",
			"--homepath=/opt/homebrew/share/grafana",
			"--packaging=brew",
			"--configOverrides=cfg:default.paths.provisioning="+provisioningPath,
		)

		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start grafana: %w", err)
		}
		processes = append(processes, cmd)
		fmt.Printf("Started Grafana [PID %d]\n", cmd.Process.Pid)
		return nil
	})

	runStep(fmt.Sprintf("Create %d tenant environments", amount), func() error {
		yaml := generateTenantCRYaml(amount, dedicatedDB)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(yaml)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	})

	fmt.Println("🕒 CRs deployed. Observe Grafana at http://localhost:3000. Press Ctrl+C to clean up.")

	for {
		cmd := exec.Command("kubectl", "get", "tenantenvironments", "-A", "-o", `jsonpath={range .items[*]}{.metadata.name}:{.status.phase} {end}`)
		output, err := cmd.Output()
		if err != nil {
			fmt.Println("❌ Failed to get tenant environments:", err)
			cleanupAndExit()
		}

		raw := strings.Trim(string(output), "\" \n")
		statuses := strings.Fields(raw)

		allReady := true
		for _, status := range statuses {
			parts := strings.Split(status, ":")
			if len(parts) != 2 || parts[1] != "Ready" {
				allReady = false
				break
			}
		}

		if allReady {
			fmt.Println("✅ All tenants are Ready")
			break
		}

		time.Sleep(2 * time.Second)
	}

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

	// Clean up Prometheus temp data
	if err := os.RemoveAll(promDataDir); err == nil {
		fmt.Println("Deleted Prometheus temp data")
	}

	fmt.Println("✅ Cleanup complete.")
	os.Exit(0)
}

func generateTenantCRYaml(amount int, dedicated bool) string {
	var builder strings.Builder

	dedicatedStr := "false"
	if dedicated {
		dedicatedStr = "true"
	}

	for i := 1; i <= amount; i++ {
		name := fmt.Sprintf("tenant-%02d", i)
		yaml := fmt.Sprintf(`apiVersion: tenant.core.mellifluus.io/v1
kind: TenantEnvironment
metadata:
  name: %s
  namespace: default
spec:
  displayName: "%s"
  resourceQuotas:
    cpuLimit: "2"
    memoryLimit: "2Gi"
    storageLimit: "2Gi"
    podLimit: 3
  database:
    dedicatedInstance: %s
---
`, name, name, dedicatedStr)

		builder.WriteString(yaml)
	}

	return builder.String()
}
