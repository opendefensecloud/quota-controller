// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Tool paths resolved from env vars with PATH fallback.
var (
	kindBin    string
	kubectlBin string
	helmBin    string
	dockerBin  string
)

const (
	kindClusterName = "quota-ctrl-e2e"
	kcpNamespace    = "kcp-system"
	quotaNamespace  = "quota-system"
	certManagerVer  = "v1.17.2"
	imageName       = "quota-controller:integration-test"
	helmTimeout     = "300s"

	// NodePort for the front-proxy service exposed via kind.
	frontProxyNodePort = "31443"
)

// Workspace names under root.
const (
	wsQuotaCtrl  = "quota-ctrl"
	wsS3Provider = "s3-provider"
	wsConsumer1  = "consumer1"
	wsConsumer2  = "consumer2"
)

var (
	rootDir     string
	fixturesDir string
	tmpDir      string

	// Host kubeconfig for kcp via front-proxy NodePort.
	kcpHostKubeconfig string

	// Per-component kubeconfigs for in-cluster pods.
	controllerKubeconfigPath string
	webhookKubeconfigPath    string

	// In-cluster front-proxy base URL (extracted from kcp-operator kubeconfig).
	inClusterFPURL string
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

func lookupTool(envVar, fallback string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}

	p, err := exec.LookPath(fallback)
	if err != nil {
		return fallback // let it fail later with a clear error
	}

	return p
}

func init() {
	kindBin = lookupTool("KIND", "kind")
	kubectlBin = lookupTool("KUBECTL", "kubectl")
	helmBin = lookupTool("HELM", "helm")
	dockerBin = lookupTool("DOCKER", "docker")
}

// run executes a command and returns combined output. Fails the test on non-zero exit.
func run(name string, args ...string) string {
	GinkgoHelper()
	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		Fail(fmt.Sprintf("command failed: %s %s\n%s\n%v", name, strings.Join(args, " "), buf.String(), err))
	}

	return buf.String()
}

// runNoFail executes a command and returns output + error without failing.
func runNoFail(name string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()

	return buf.String(), err
}

// kindctl runs kubectl against the kind cluster.
func kindctl(args ...string) string {
	GinkgoHelper()

	return run(kubectlBin, append([]string{"--context", "kind-" + kindClusterName}, args...)...)
}

// kindctlNoFail runs kubectl against the kind cluster without failing.
func kindctlNoFail(args ...string) (string, error) {
	return runNoFail(kubectlBin, append([]string{"--context", "kind-" + kindClusterName}, args...)...)
}

// kcpctl runs kubectl against a kcp workspace path.
func kcpctl(wsPath string, args ...string) {
	GinkgoHelper()
	run(kubectlBin, append([]string{
		"--kubeconfig", kcpHostKubeconfig,
		"--server", fmt.Sprintf("https://localhost:%s/clusters/root:%s", frontProxyNodePort, wsPath),
	}, args...)...)
}

// kcpctlNoFail runs kubectl against a kcp workspace without failing.
func kcpctlNoFail(wsPath string, args ...string) (string, error) {
	return runNoFail(kubectlBin, append([]string{
		"--kubeconfig", kcpHostKubeconfig,
		"--server", fmt.Sprintf("https://localhost:%s/clusters/root:%s", frontProxyNodePort, wsPath),
	}, args...)...)
}

// kcpctlRootNoFail runs kubectl against the kcp root workspace without failing.
func kcpctlRootNoFail(args ...string) (string, error) {
	return runNoFail(kubectlBin, append([]string{
		"--kubeconfig", kcpHostKubeconfig,
		"--server", fmt.Sprintf("https://localhost:%s/clusters/root", frontProxyNodePort),
	}, args...)...)
}

// applyFixtureToWS applies a YAML fixture to a kcp workspace with placeholder
// substitution. Retries on transient kcp authorization errors.
func applyFixtureToWS(wsPath, file string, substitutions map[string]string) {
	GinkgoHelper()
	raw, err := os.ReadFile(file)
	Expect(err).NotTo(HaveOccurred())

	content := string(raw)
	for k, v := range substitutions {
		content = strings.ReplaceAll(content, "${"+k+"}", v)
	}

	waitFor(2*time.Minute, fmt.Sprintf("apply %s to %s", file, wsPath), func() error {
		cmd := exec.CommandContext(context.Background(), kubectlBin, //nolint:gosec
			"--kubeconfig", kcpHostKubeconfig,
			"--server", fmt.Sprintf("https://localhost:%s/clusters/root:%s", frontProxyNodePort, wsPath),
			"apply", "-f", "-",
		)
		cmd.Stdin = strings.NewReader(content)
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%w: %s", err, buf.String())
		}

		return nil
	})
}

// waitFor retries a check function until it succeeds or the timeout is reached.
func waitFor(timeout time.Duration, desc string, check func() error) {
	GinkgoHelper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var lastErr error

	for {
		if err := check(); err == nil {
			return
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			Fail(fmt.Sprintf("timed out waiting for: %s (last error: %v)", desc, lastErr))
		case <-ticker.C:
		}
	}
}

// kindctlSecret extracts the kubeconfig from a k8s secret in the kcp-system namespace.
func kindctlSecret(name string) string {
	GinkgoHelper()

	return kindctl("-n", kcpNamespace, "get", "secret", name, "-o", "jsonpath={.data.kubeconfig}")
}

var _ = SynchronizedBeforeSuite(func() {
	var err error
	rootDir, err = filepath.Abs("../..")
	Expect(err).NotTo(HaveOccurred())
	fixturesDir = filepath.Join(rootDir, "test", "fixtures")

	tmpDir, err = os.MkdirTemp("", "quota-ctrl-e2e-*")
	Expect(err).NotTo(HaveOccurred())
	kcpHostKubeconfig = filepath.Join(tmpDir, "kcp-host.kubeconfig")

	By("creating kind cluster")
	createKindCluster()

	By("installing cert-manager")
	installCertManager()

	By("deploying kcp via kcp-operator")
	deployKCPOperator()

	By("deploying etcd")
	deployEtcd()

	By("creating kcp RootShard and FrontProxy")
	createKCPResources()

	By("generating admin kubeconfig")
	buildAdminKubeconfig()

	By("building component kubeconfigs")
	buildComponentKubeconfigs()

	By("building and loading image")
	buildAndLoadImage()

	By("setting up kcp workspaces")
	setupKCPWorkspaces()

	By("bootstrapping RBAC")
	bootstrapRBAC()

	By("deploying helm charts")
	deployCharts()
}, func() {})

var _ = SynchronizedAfterSuite(func() {}, func() {
	if os.Getenv("E2E_SKIP_CLEANUP") != "" {
		return
	}

	out, err := runNoFail(kindBin, "delete", "cluster", "--name", kindClusterName)
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "kind delete: %s %v\n", out, err)
	}

	if tmpDir != "" {
		_ = os.RemoveAll(tmpDir)
	}
})

func createKindCluster() {
	// Reuse if it already exists.
	out, _ := runNoFail(kindBin, "get", "clusters")

	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == kindClusterName {
			// Ensure kubeconfig context exists.
			run(kindBin, "export", "kubeconfig", "--name", kindClusterName)

			return
		}
	}

	run(kindBin, "create", "cluster",
		"--name", kindClusterName,
		"--config", filepath.Join(fixturesDir, "kind-config.yaml"),
		"--wait", "60s",
	)
}

func installCertManager() {
	kindctl("apply", "-f",
		fmt.Sprintf("https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml", certManagerVer))

	waitFor(2*time.Minute, "cert-manager ready", func() error {
		_, err := kindctlNoFail("-n", "cert-manager", "wait", "deployment", "cert-manager-webhook",
			"--for=condition=Available", "--timeout=1s")

		return err
	})

	waitFor(time.Minute, "self-signed ClusterIssuer created", func() error {
		_, err := kindctlNoFail("apply", "-f", filepath.Join(fixturesDir, "cert-manager-selfsigned-issuer.yaml"))

		return err
	})
}

func deployKCPOperator() {
	_, _ = runNoFail(helmBin, "repo", "add", "kcp", "https://kcp-dev.github.io/helm-charts")
	run(helmBin, "repo", "update", "kcp")

	run(helmBin, "upgrade", "--install", "kcp-operator", "kcp/kcp-operator",
		"--namespace", kcpNamespace,
		"--create-namespace",
		"--wait", "--timeout", helmTimeout,
	)
}

func deployEtcd() {
	applyEtcd("etcd-root")

	waitFor(2*time.Minute, "etcd-root ready", func() error {
		_, err := kindctlNoFail("-n", kcpNamespace, "wait", "statefulset", "etcd-root",
			"--for=jsonpath={.status.readyReplicas}=1", "--timeout=1s")

		return err
	})
}

// applyEtcd creates a minimal single-node etcd instance in the kcp namespace.
func applyEtcd(name string) {
	GinkgoHelper()
	manifest := fmt.Sprintf(`---
apiVersion: v1
kind: Service
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: etcd
    app.kubernetes.io/instance: %[1]s
  ports:
    - name: client
      port: 2379
      targetPort: client
---
apiVersion: v1
kind: Service
metadata:
  name: %[1]s-headless
  namespace: %[2]s
  annotations:
    service.alpha.kubernetes.io/tolerate-unready-endpoints: "true"
spec:
  type: ClusterIP
  clusterIP: None
  publishNotReadyAddresses: true
  selector:
    app.kubernetes.io/name: etcd
    app.kubernetes.io/instance: %[1]s
  ports:
    - name: client
      port: 2379
      targetPort: client
    - name: peer
      port: 2380
      targetPort: peer
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: etcd
      app.kubernetes.io/instance: %[1]s
  serviceName: %[1]s-headless
  template:
    metadata:
      labels:
        app.kubernetes.io/name: etcd
        app.kubernetes.io/instance: %[1]s
    spec:
      automountServiceAccountToken: false
      containers:
        - name: etcd
          image: quay.io/coreos/etcd:v3.5.21
          imagePullPolicy: IfNotPresent
          command: ["/usr/local/bin/etcd"]
          args:
            - --name=$(HOSTNAME)
            - --data-dir=/data
            - --listen-peer-urls=http://0.0.0.0:2380
            - --listen-client-urls=http://0.0.0.0:2379
            - --advertise-client-urls=http://$(HOSTNAME).%[1]s-headless.%[2]s.svc.cluster.local:2379
            - --initial-cluster-state=new
            - --initial-cluster-token=$(HOSTNAME)
            - --initial-cluster=$(HOSTNAME)=http://$(HOSTNAME).%[1]s-headless.%[2]s.svc.cluster.local:2380
            - --initial-advertise-peer-urls=http://$(HOSTNAME).%[1]s-headless.%[2]s.svc.cluster.local:2380
            - --listen-metrics-urls=http://0.0.0.0:8080
          env:
            - name: HOSTNAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
          ports:
            - name: client
              containerPort: 2379
            - name: peer
              containerPort: 2380
            - name: metrics
              containerPort: 8080
          livenessProbe:
            httpGet:
              path: /livez
              port: metrics
            initialDelaySeconds: 15
            periodSeconds: 10
          readinessProbe:
            httpGet:
              path: /readyz
              port: metrics
            initialDelaySeconds: 10
            periodSeconds: 5
            failureThreshold: 30
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              memory: 256Mi
          volumeMounts:
            - name: data
              mountPath: /data
  volumeClaimTemplates:
    - metadata:
        name: data
      spec:
        accessModes: [ReadWriteOnce]
        resources:
          requests:
            storage: 1Gi
`, name, kcpNamespace)

	cmd := exec.CommandContext(context.Background(), kubectlBin, //nolint:gosec
		"--context", "kind-"+kindClusterName, "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)

	var buf bytes.Buffer

	cmd.Stdout = &buf
	cmd.Stderr = &buf
	Expect(cmd.Run()).To(Succeed(), "applying etcd %s: %s", name, buf.String())
}

func createKCPResources() {
	// The front-proxy hostname used for in-cluster access and via NodePort.
	fpHostname := fmt.Sprintf("kcp-front-proxy.%s.svc.cluster.local", kcpNamespace)

	// Create a cert-manager Issuer in the kcp namespace for kcp-operator PKI.
	applyToKind(fmt.Sprintf(`apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: selfsigned
  namespace: %s
spec:
  selfSigned: {}`, kcpNamespace))

	// Create RootShard.
	applyToKind(fmt.Sprintf(`apiVersion: operator.kcp.io/v1alpha1
kind: RootShard
metadata:
  name: root
  namespace: %[1]s
spec:
  external:
    hostname: %[2]s
    port: 6443
  certificates:
    issuerRef:
      group: cert-manager.io
      kind: Issuer
      name: selfsigned
  certificateTemplates:
    server:
      spec:
        dnsNames:
          - localhost
        ipAddresses:
          - "127.0.0.1"
  cache:
    embedded:
      enabled: true
  etcd:
    endpoints:
      - http://etcd-root.%[1]s.svc.cluster.local:2379
  auth:
    serviceAccount:
      enabled: true
  deploymentTemplate:
    spec:
      template:
        spec:
          hostAliases:
            - ip: "10.96.200.200"
              hostnames:
                - "%[2]s"`, kcpNamespace, fpHostname))

	// Create FrontProxy with fixed ClusterIP and NodePort.
	applyToKind(fmt.Sprintf(`apiVersion: operator.kcp.io/v1alpha1
kind: FrontProxy
metadata:
  name: kcp
  namespace: %[1]s
spec:
  rootShard:
    ref:
      name: root
  auth:
    serviceAccount:
      enabled: true
  serviceTemplate:
    spec:
      type: NodePort
      clusterIP: "10.96.200.200"
  certificateTemplates:
    server:
      spec:
        dnsNames:
          - localhost
          - "%[2]s"
        ipAddresses:
          - "127.0.0.1"`, kcpNamespace, fpHostname))

	waitFor(3*time.Minute, "root shard running", func() error {
		out, err := kindctlNoFail("-n", kcpNamespace, "get", "rootshard", "root",
			"-o", "jsonpath={.status.phase}")
		if err != nil {
			return err
		}

		if strings.TrimSpace(out) != "Running" {
			return fmt.Errorf("root shard phase: %s", out)
		}

		return nil
	})

	waitFor(2*time.Minute, "front-proxy running", func() error {
		out, err := kindctlNoFail("-n", kcpNamespace, "get", "frontproxy", "kcp",
			"-o", "jsonpath={.status.phase}")
		if err != nil {
			return err
		}

		if strings.TrimSpace(out) != "Running" {
			return fmt.Errorf("front-proxy phase: %s", out)
		}

		return nil
	})

	// Pin NodePort to the constant frontProxyNodePort.
	kindctl("-n", kcpNamespace, "patch", "service", "kcp-front-proxy", "--type=json",
		fmt.Sprintf(`-p=[{"op":"replace","path":"/spec/ports/0/nodePort","value":%s}]`, frontProxyNodePort))
}

func buildAdminKubeconfig() {
	// Create a Kubeconfig CR for admin access via front-proxy.
	applyToKind(fmt.Sprintf(`apiVersion: operator.kcp.io/v1alpha1
kind: Kubeconfig
metadata:
  name: e2e-admin
  namespace: %s
spec:
  username: kcp-admin
  groups:
    - "system:kcp:admin"
  validity: 8766h
  secretRef:
    name: e2e-admin-kubeconfig
  target:
    frontProxyRef:
      name: kcp`, kcpNamespace))

	waitFor(2*time.Minute, "admin kubeconfig secret created", func() error {
		_, err := kindctlNoFail("-n", kcpNamespace, "get", "secret", "e2e-admin-kubeconfig",
			"-o", "jsonpath={.data.kubeconfig}")

		return err
	})

	kcRaw := kindctlSecret("e2e-admin-kubeconfig")
	kcBytes, err := decodeBase64(kcRaw)
	Expect(err).NotTo(HaveOccurred())

	adminServerURL := extractServerFromKubeconfig(kcBytes)
	rewritten := strings.ReplaceAll(string(kcBytes),
		adminServerURL,
		fmt.Sprintf("https://localhost:%s", frontProxyNodePort))

	Expect(os.WriteFile(kcpHostKubeconfig, []byte(rewritten), 0o600)).To(Succeed())

	waitFor(30*time.Second, "kcp API reachable via front-proxy", func() error {
		_, err := runNoFail(kubectlBin, "--kubeconfig", kcpHostKubeconfig,
			"--server", fmt.Sprintf("https://localhost:%s/clusters/root", frontProxyNodePort),
			"get", "--raw", "/readyz")

		return err
	})
}

// buildComponentKubeconfigs creates Kubeconfig CRs for the controller and webhook
// identities, then extracts the generated kubeconfigs pointing at the in-cluster
// front-proxy for use by deployed pods.
func buildComponentKubeconfigs() {
	quotaCtrlPath := "root:" + wsQuotaCtrl

	applyToKind(fmt.Sprintf(`apiVersion: operator.kcp.io/v1alpha1
kind: Kubeconfig
metadata:
  name: e2e-controller
  namespace: %[1]s
spec:
  username: "system:serviceaccount:%[2]s:quota-controller"
  groups:
    - "system:authenticated"
    - "system:serviceaccounts"
    - "system:serviceaccounts:%[2]s"
  validity: 8766h
  secretRef:
    name: e2e-controller-kubeconfig
  target:
    rootShardRef:
      name: root`, kcpNamespace, quotaNamespace))

	applyToKind(fmt.Sprintf(`apiVersion: operator.kcp.io/v1alpha1
kind: Kubeconfig
metadata:
  name: e2e-webhook
  namespace: %[1]s
spec:
  username: "system:serviceaccount:%[2]s:quota-webhook"
  groups:
    - "system:authenticated"
    - "system:serviceaccounts"
    - "system:serviceaccounts:%[2]s"
  validity: 8766h
  secretRef:
    name: e2e-webhook-kubeconfig
  target:
    rootShardRef:
      name: root`, kcpNamespace, quotaNamespace))

	// Wait for both kubeconfig secrets.
	for _, name := range []string{"e2e-controller-kubeconfig", "e2e-webhook-kubeconfig"} {
		waitFor(2*time.Minute, fmt.Sprintf("%s secret created", name), func() error {
			_, err := kindctlNoFail("-n", kcpNamespace, "get", "secret", name,
				"-o", "jsonpath={.data.kubeconfig}")

			return err
		})
	}

	// Extract shard URL and rewrite to front-proxy + workspace path for in-cluster use.
	fpHostname := fmt.Sprintf("kcp-front-proxy.%s.svc.cluster.local", kcpNamespace)
	kcRaw := kindctlSecret("e2e-controller-kubeconfig")
	kcBytes, err := decodeBase64(kcRaw)
	Expect(err).NotTo(HaveOccurred())
	shardURL := extractServerFromKubeconfig(kcBytes)

	parsed, err := url.Parse(shardURL)
	Expect(err).NotTo(HaveOccurred())
	fpPort := parsed.Port()

	if fpPort == "" {
		fpPort = "6443"
	}

	inClusterFPURL = "https://" + net.JoinHostPort(fpHostname, fpPort)
	quotaCtrlURL := inClusterFPURL + "/clusters/" + quotaCtrlPath

	controllerKubeconfigPath = filepath.Join(tmpDir, "kcp-controller.kubeconfig")
	extractAndRewriteKubeconfig("e2e-controller-kubeconfig", controllerKubeconfigPath,
		shardURL, quotaCtrlURL)

	webhookKubeconfigPath = filepath.Join(tmpDir, "kcp-webhook.kubeconfig")
	extractAndRewriteKubeconfig("e2e-webhook-kubeconfig", webhookKubeconfigPath,
		shardURL, quotaCtrlURL)
}

// extractAndRewriteKubeconfig extracts a kubeconfig from a secret, rewrites the
// server URL, and writes it to the given path.
func extractAndRewriteKubeconfig(secretName, outputPath, oldURL, newURL string) {
	GinkgoHelper()
	kcRaw := kindctlSecret(secretName)
	kcBytes, err := decodeBase64(kcRaw)
	Expect(err).NotTo(HaveOccurred())

	rewritten := strings.ReplaceAll(string(kcBytes), oldURL, newURL)
	rewritten = strings.ReplaceAll(rewritten, "current-context: default", "current-context: base")

	Expect(os.WriteFile(outputPath, []byte(rewritten), 0o600)).To(Succeed())
}

// decodeBase64 decodes a base64-encoded string.
func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimSpace(s))
}

// bootstrapRBAC creates RBAC for the controller and webhook identities.
func bootstrapRBAC() {
	// Webhook get/list via system:admin (shard-wide).
	applySystemAdminRBAC("root", "rootShardRef")

	// Controller RBAC in the root workspace.
	run(kubectlBin, "--kubeconfig", kcpHostKubeconfig,
		"--server", fmt.Sprintf("https://localhost:%s/clusters/root", frontProxyNodePort),
		"apply", "-f", filepath.Join(fixturesDir, "root-rbac-bootstrap.yaml"))

	// Controller + webhook RBAC in the quota-ctrl workspace.
	run(kubectlBin, "--kubeconfig", kcpHostKubeconfig,
		"--server", fmt.Sprintf("https://localhost:%s/clusters/root:%s", frontProxyNodePort, wsQuotaCtrl),
		"apply", "-f", filepath.Join(fixturesDir, "quota-ctrl-rbac-bootstrap.yaml"))
}

// applySystemAdminRBAC creates a system:masters kubeconfig targeting the given shard,
// port-forwards that shard's service to localhost, applies system-admin RBAC, then
// tears down the port-forward.
func applySystemAdminRBAC(shardName, refField string) {
	GinkgoHelper()

	kubeconfigName := "e2e-" + shardName + "-system-masters"
	secretName := kubeconfigName + "-kubeconfig"

	applyToKind(fmt.Sprintf(`apiVersion: operator.kcp.io/v1alpha1
kind: Kubeconfig
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  username: e2e-system-masters
  groups:
    - "system:masters"
  validity: 8766h
  secretRef:
    name: %[3]s
  target:
    %[4]s:
      name: %[5]s`, kubeconfigName, kcpNamespace, secretName, refField, shardName))

	waitFor(2*time.Minute, fmt.Sprintf("%s secret created", secretName), func() error {
		_, err := kindctlNoFail("-n", kcpNamespace, "get", "secret", secretName,
			"-o", "jsonpath={.data.kubeconfig}")

		return err
	})

	kcRaw := kindctlSecret(secretName)
	kcBytes, err := decodeBase64(kcRaw)
	Expect(err).NotTo(HaveOccurred())
	shardURL := extractServerFromKubeconfig(kcBytes)

	parsed, err := url.Parse(shardURL)
	Expect(err).NotTo(HaveOccurred())
	shardSvc := strings.SplitN(parsed.Hostname(), ".", 2)[0]
	shardPort := parsed.Port()

	if shardPort == "" {
		shardPort = "6443"
	}

	localPort := pickFreePort()
	rewritten := strings.ReplaceAll(string(kcBytes),
		shardURL, fmt.Sprintf("https://localhost:%d", localPort))
	rewritten = strings.ReplaceAll(rewritten,
		"current-context: default", "current-context: base")
	sysKubeconfig := filepath.Join(tmpDir, kubeconfigName+".kubeconfig")
	Expect(os.WriteFile(sysKubeconfig, []byte(rewritten), 0o600)).To(Succeed())

	// Start port-forward in the background; kill it on return.
	pfCmd := exec.CommandContext(context.Background(), kubectlBin, //nolint:gosec
		"--context", "kind-"+kindClusterName,
		"-n", kcpNamespace, "port-forward",
		"svc/"+shardSvc, fmt.Sprintf("%d:%s", localPort, shardPort))
	pfCmd.Stdout = GinkgoWriter
	pfCmd.Stderr = GinkgoWriter
	Expect(pfCmd.Start()).To(Succeed())

	defer func() {
		_ = pfCmd.Process.Kill()
		_, _ = pfCmd.Process.Wait()
	}()

	waitFor(30*time.Second, fmt.Sprintf("%s reachable via port-forward", shardSvc), func() error {
		_, err := runNoFail(kubectlBin, "--kubeconfig", sysKubeconfig,
			"--server", fmt.Sprintf("https://localhost:%d/clusters/system:admin", localPort),
			"get", "--raw", "/readyz")

		return err
	})

	// --validate=false: system:admin does not serve OpenAPI.
	run(kubectlBin, "--kubeconfig", sysKubeconfig,
		"--server", fmt.Sprintf("https://localhost:%d/clusters/system:admin", localPort),
		"apply", "--validate=false",
		"-f", filepath.Join(fixturesDir, "system-admin-rbac-bootstrap.yaml"))
}

// pickFreePort asks the kernel for a free TCP port on localhost.
func pickFreePort() int {
	GinkgoHelper()

	var lc net.ListenConfig

	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	port := listener.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert
	Expect(listener.Close()).To(Succeed())

	return port
}

// extractServerFromKubeconfig extracts the server URL from a kubeconfig YAML.
func extractServerFromKubeconfig(kubeconfig []byte) string {
	re := regexp.MustCompile(`server:\s*(https?://\S+)`)
	m := re.FindSubmatch(kubeconfig)

	if len(m) < 2 {
		Fail("could not extract server URL from kubeconfig")
	}

	return string(m[1])
}

func buildAndLoadImage() {
	run(dockerBin, "build", "-t", imageName, rootDir)
	run(kindBin, "load", "docker-image", imageName, "--name", kindClusterName)
}

func setupKCPWorkspaces() {
	for _, ws := range []string{wsQuotaCtrl, wsS3Provider, wsConsumer1, wsConsumer2} {
		createWorkspace(ws)
	}

	for _, ws := range []string{wsQuotaCtrl, wsS3Provider, wsConsumer1, wsConsumer2} {
		wsName := ws // capture for closure
		waitFor(time.Minute, fmt.Sprintf("workspace %s ready", wsName), func() error {
			out, err := kcpctlRootNoFail("get", "workspace", wsName, "-o", "jsonpath={.status.phase}")
			if err != nil {
				return err
			}

			if strings.TrimSpace(out) != "Ready" {
				return fmt.Errorf("workspace %s phase: %s", wsName, out)
			}

			return nil
		})
	}

	// Apply quota-ctrl APIResourceSchemas and APIExport.
	kcpctl(wsQuotaCtrl, "apply", "-f", filepath.Join(rootDir, "config/kcp"))

	// Apply S3 provider resources.
	kcpctl(wsS3Provider, "apply", "-f", filepath.Join(fixturesDir, "apiresourceschema-buckets.s3.example.com.yaml"))
	kcpctl(wsS3Provider, "apply", "-f", filepath.Join(fixturesDir, "apiexport-s3.example.com.yaml"))

	// Provider binds to quota-ctrl to accept the VWC permission claim.
	applyFixtureToWS(wsS3Provider, filepath.Join(fixturesDir, "apibinding-quota-provider.yaml"), map[string]string{
		"QUOTA_CTRL_PATH": "root:" + wsQuotaCtrl,
	})

	// Apply initial ConsumptionQuota (defaultLimit=3).
	applyFixtureToWS(wsS3Provider, filepath.Join(fixturesDir, "consumptionquota-buckets-limit3.yaml"), nil)

	// Both consumers bind to the S3 provider.
	s3ProviderPath := "root:" + wsS3Provider

	for _, ws := range []string{wsConsumer1, wsConsumer2} {
		applyFixtureToWS(ws, filepath.Join(fixturesDir, "apibinding-s3.example.com.yaml"), map[string]string{
			"S3_PROVIDER_PATH": s3ProviderPath,
		})
	}

	// Wait for bindings.
	bindings := []struct{ ws, name string }{
		{wsS3Provider, "quota-provider"},
		{wsConsumer1, "s3.example.com"},
		{wsConsumer2, "s3.example.com"},
	}

	for _, b := range bindings {
		bws, bname := b.ws, b.name
		waitFor(2*time.Minute, fmt.Sprintf("binding %s in %s bound", bname, bws), func() error {
			out, err := kcpctlNoFail(bws, "get", "apibinding", bname, "-o", "jsonpath={.status.phase}")
			if err != nil {
				return err
			}

			if strings.TrimSpace(out) != "Bound" {
				return fmt.Errorf("phase: %s", out)
			}

			return nil
		})
	}
}

// createWorkspace creates a kcp workspace under root.
func createWorkspace(name string) {
	GinkgoHelper()
	manifest := fmt.Sprintf(`apiVersion: tenancy.kcp.io/v1alpha1
kind: Workspace
metadata:
  name: %s`, name)

	cmd := exec.CommandContext(context.Background(), kubectlBin, //nolint:gosec
		"--kubeconfig", kcpHostKubeconfig,
		"--server", fmt.Sprintf("https://localhost:%s/clusters/root", frontProxyNodePort),
		"apply", "-f", "-",
	)
	cmd.Stdin = strings.NewReader(manifest)

	var buf bytes.Buffer

	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.Run() //nolint:errcheck // ignore AlreadyExists
}

// createKubeconfigSecret creates or updates a Secret in the quota-system namespace
// containing the given kubeconfig file.
func createKubeconfigSecret(secretName, kubeconfigPath string) {
	GinkgoHelper()
	cmd := exec.CommandContext(context.Background(), kubectlBin, "--context", "kind-"+kindClusterName, //nolint:gosec
		"-n", quotaNamespace, "create", "secret", "generic", secretName,
		"--from-file=kubeconfig="+kubeconfigPath,
		"--dry-run=client", "-o", "yaml")

	var buf bytes.Buffer

	cmd.Stdout = &buf
	cmd.Stderr = &buf
	Expect(cmd.Run()).To(Succeed(), buf.String())

	applyCmd := exec.CommandContext(context.Background(), kubectlBin, "--context", "kind-"+kindClusterName, "apply", "-f", "-") //nolint:gosec
	applyCmd.Stdin = bytes.NewReader(buf.Bytes())

	var applyBuf bytes.Buffer

	applyCmd.Stdout = &applyBuf
	applyCmd.Stderr = &applyBuf
	Expect(applyCmd.Run()).To(Succeed(), applyBuf.String())
}

func deployCharts() {
	kindctlNoFail("create", "namespace", quotaNamespace) //nolint:errcheck

	createKubeconfigSecret("kcp-controller-kubeconfig", controllerKubeconfigPath)
	createKubeconfigSecret("kcp-webhook-kubeconfig", webhookKubeconfigPath)

	run(helmBin, "upgrade", "--install", "quota-controller",
		filepath.Join(rootDir, "charts/quota-controller"),
		"--namespace", quotaNamespace,
		"--values", filepath.Join(fixturesDir, "integration-values.yaml"),
		"--set", "kcpBaseHost="+inClusterFPURL,
		"--wait", "--timeout", "120s",
	)
}

// applyToKind applies a YAML manifest to the kind cluster.
func applyToKind(manifest string) {
	GinkgoHelper()
	cmd := exec.CommandContext(context.Background(), kubectlBin, //nolint:gosec
		"--context", "kind-"+kindClusterName, "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)

	var buf bytes.Buffer

	cmd.Stdout = &buf
	cmd.Stderr = &buf
	Expect(cmd.Run()).To(Succeed(), "applying manifest: %s", buf.String())
}
