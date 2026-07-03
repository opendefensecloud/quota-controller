// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// createBucketGenerateName creates a Bucket in the given kcp workspace using
// metadata.generateName so the apiserver assigns the final name. This exercises
// the generateName code path in the CreationValidator webhook (Fix ADR-003).
func createBucketGenerateName(wsPath, namespace, prefix string) (string, error) {
	manifest := fmt.Sprintf(`apiVersion: s3.example.com/v1
kind: Bucket
metadata:
  generateName: %s
  namespace: %s
spec:
  region: eu-west-1
`, prefix, namespace)

	cmd := exec.CommandContext(context.Background(), kubectlBin, //nolint:gosec
		"--kubeconfig", kcpHostKubeconfig,
		"--server", fmt.Sprintf("https://localhost:%s/clusters/root:%s", frontProxyNodePort, wsPath),
		"create", "-f", "-",
	)
	cmd.Stdin = strings.NewReader(manifest)

	var buf bytes.Buffer

	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()

	return buf.String(), err
}

// createBucketInline creates a Bucket object in the given kcp workspace and
// namespace via kubectl apply from an inline manifest.
func createBucketInline(wsPath, namespace, name string) (string, error) {
	manifest := fmt.Sprintf(`apiVersion: s3.example.com/v1
kind: Bucket
metadata:
  name: %s
  namespace: %s
spec:
  region: eu-west-1
`, name, namespace)

	cmd := exec.CommandContext(context.Background(), kubectlBin, //nolint:gosec
		"--kubeconfig", kcpHostKubeconfig,
		"--server", fmt.Sprintf("https://localhost:%s/clusters/root:%s", frontProxyNodePort, wsPath),
		"apply", "-f", "-",
	)
	cmd.Stdin = strings.NewReader(manifest)

	var buf bytes.Buffer

	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()

	return buf.String(), err
}

var _ = Describe("Quota Controller E2E", Ordered, func() {
	BeforeAll(func() {
	})

	// Scenario A: happy path + strict boundary.
	// Provider has ConsumptionQuota{defaultLimit:3}. Consumer creates 3 Buckets
	// (all succeed), the 4th is DENIED with the quota message. Deleting 1 frees a
	// slot, allowing the previously-denied bucket to be created.
	It("Scenario A: allows up to defaultLimit:3 and denies the next", func() {
		By("creating bucket-a1 in consumer1 — should succeed")
		applyFixtureToWS(wsConsumer1, filepath.Join(fixturesDir, "bucket-a1.yaml"), nil)

		By("creating bucket-a2 in consumer1 — should succeed")
		applyFixtureToWS(wsConsumer1, filepath.Join(fixturesDir, "bucket-a2.yaml"), nil)

		By("creating bucket-a3 in consumer1 — should succeed (at limit)")
		applyFixtureToWS(wsConsumer1, filepath.Join(fixturesDir, "bucket-a3.yaml"), nil)

		By("verifying all three buckets exist")

		waitFor(30*time.Second, "three buckets exist in consumer1", func() error {
			out, err := kcpctlNoFail(wsConsumer1, "get", "buckets", "-n", "default", "--no-headers")
			if err != nil {
				return err
			}

			lines := 0

			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				if strings.TrimSpace(l) != "" {
					lines++
				}
			}

			if lines < 3 {
				return fmt.Errorf("only %d buckets exist, want 3", lines)
			}

			return nil
		})

		By("creating bucket-a4 — should be DENIED (over limit)")

		waitFor(time.Minute, "4th bucket create is denied with quota message", func() error {
			out, err := createBucketInline(wsConsumer1, "default", "bucket-a4")
			if err == nil {
				// Admission went through — clean up and retry (controller may not have
				// installed the webhook yet).
				_, _ = kcpctlNoFail(wsConsumer1, "delete", "bucket", "bucket-a4", "-n", "default")

				return fmt.Errorf("bucket-a4 was admitted, expected denial")
			}

			if !strings.Contains(out+err.Error(), "quota") {
				return fmt.Errorf("unexpected error (no 'quota' in message): %s %v", out, err)
			}

			return nil
		})

		By("deleting bucket-a1 to free a slot")
		kcpctl(wsConsumer1, "delete", "bucket", "bucket-a1", "-n", "default")

		By("waiting for confirmed count to drop to 2 (reconciler must observe the deletion)")

		waitFor(2*time.Minute, "confirmed drops after deletion", func() error {
			// Re-attempt creating bucket-a4: succeeds once a slot is free.
			_, err := createBucketInline(wsConsumer1, "default", "bucket-a4")

			return err
		})
	})

	// Update the ConsumptionQuota to defaultLimit:5 before Scenario B.
	It("raises defaultLimit to 5 for Scenario B", func() {
		applyFixtureToWS(wsS3Provider, filepath.Join(fixturesDir, "consumptionquota-buckets-limit5.yaml"), nil)

		waitFor(time.Minute, "ConsumptionQuota defaultLimit is 5", func() error {
			out, err := kcpctlNoFail(wsS3Provider, "get", "consumptionquota", "quota-s3-buckets",
				"-o", "jsonpath={.spec.defaultLimit}")
			if err != nil {
				return err
			}

			if strings.TrimSpace(out) != "5" {
				return fmt.Errorf("defaultLimit still %s, want 5", out)
			}

			return nil
		})
	})

	// Scenario B: no overshoot.
	// defaultLimit:5, fire 10 concurrent Bucket creates, assert exactly 5 exist.
	It("Scenario B: never admits more than defaultLimit:5 under concurrency", func() {
		const total = 10

		var wg sync.WaitGroup

		wg.Add(total)

		for i := range total {
			go func(i int) {
				defer GinkgoRecover()
				defer wg.Done()

				name := fmt.Sprintf("bucket-b%d", i)
				_, _ = createBucketInline(wsConsumer2, "default", name)
			}(i)
		}

		wg.Wait()

		By("counting admitted buckets in consumer2")

		waitFor(time.Minute, "admitted bucket count stabilises at ≤5", func() error {
			out, err := kcpctlNoFail(wsConsumer2, "get", "buckets", "-n", "default", "--no-headers")
			if err != nil {
				return err
			}

			count := 0

			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				if strings.TrimSpace(l) != "" {
					count++
				}
			}

			if count > 5 {
				return fmt.Errorf("overshoot: %d buckets exist, limit is 5", count)
			}

			return nil
		})

		// Final assertion: exactly 5 must exist.
		Eventually(func() int {
			out, err := kcpctlNoFail(wsConsumer2, "get", "buckets", "-n", "default", "--no-headers")
			Expect(err).NotTo(HaveOccurred())
			count := 0

			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				if strings.TrimSpace(l) != "" {
					count++
				}
			}

			return count
		}).Should(Equal(5), "exactly 5 buckets must exist (no overshoot)")
	})

	// Scenario D: generateName burst respects the limit.
	// Clean consumer2, then fire N concurrent Bucket creates using metadata.generateName
	// against the current defaultLimit (5). Assert exactly limit objects exist.
	// This is an authored regression for the generateName bypass fix — NOT run in CI.
	It("Scenario D: generateName burst never exceeds the defaultLimit", func() {
		const genPrefix = "bucket-gen-"
		const genLimit = 5 // matches the limit set by Scenario B's predecessor
		const genTotal = 10

		By("deleting all consumer2 buckets to start with a clean slate")
		_, _ = kcpctlNoFail(wsConsumer2, "delete", "buckets", "--all", "-n", "default")

		waitFor(time.Minute, "consumer2 buckets cleared", func() error {
			out, err := kcpctlNoFail(wsConsumer2, "get", "buckets", "-n", "default", "--no-headers")
			if err != nil {
				return err
			}

			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				if strings.TrimSpace(l) != "" {
					return fmt.Errorf("buckets still present in consumer2: %s", out)
				}
			}

			return nil
		})

		By("firing a burst of generateName Bucket creates")

		var wg sync.WaitGroup

		wg.Add(genTotal)

		for i := range genTotal {
			go func(i int) {
				defer GinkgoRecover()
				defer wg.Done()
				_, _ = createBucketGenerateName(wsConsumer2, "default", genPrefix)
			}(i)
		}

		wg.Wait()

		By("asserting admitted count does not exceed the limit")

		waitFor(time.Minute, "admitted generateName bucket count stabilises at ≤genLimit", func() error {
			out, err := kcpctlNoFail(wsConsumer2, "get", "buckets", "-n", "default", "--no-headers")
			if err != nil {
				return err
			}

			count := 0

			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				if strings.TrimSpace(l) != "" {
					count++
				}
			}

			if count > genLimit {
				return fmt.Errorf("overshoot with generateName: %d buckets, limit is %d", count, genLimit)
			}

			return nil
		})

		// Final assertion: exactly genLimit must exist (limit respected, no under-admission).
		Eventually(func() int {
			out, err := kcpctlNoFail(wsConsumer2, "get", "buckets", "-n", "default", "--no-headers")
			Expect(err).NotTo(HaveOccurred())
			count := 0

			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				if strings.TrimSpace(l) != "" {
					count++
				}
			}

			return count
		}).Should(Equal(genLimit), "exactly %d generateName buckets must exist (limit respected)", genLimit)
	})

	// Scenario C: fail-closed.
	// Scale the webhook Deployment to 0 → a Bucket create is rejected because the
	// webhook is unreachable and failurePolicy=Fail. Scale back to 1 → creates resume.
	It("Scenario C: rejects creates when webhook unreachable, resumes after scale-up", func() {
		By("scaling webhook Deployment to 0")
		kindctl("scale", "deployment",
			"quota-controller-webhook",
			"-n", quotaNamespace,
			"--replicas=0")

		waitFor(2*time.Minute, "webhook pods reach 0", func() error {
			out, err := kindctlNoFail("-n", quotaNamespace, "get", "deployment",
				"quota-controller-webhook",
				"-o", "jsonpath={.status.readyReplicas}")
			if err != nil {
				return err
			}

			ready := strings.TrimSpace(out)
			if ready != "" && ready != "0" {
				return fmt.Errorf("still %s ready replicas", ready)
			}

			return nil
		})

		By("attempting a Bucket create — should fail (webhook unreachable)")

		waitFor(time.Minute, "create is rejected due to unreachable webhook", func() error {
			_, err := createBucketInline(wsConsumer1, "default", "bucket-c1")
			if err == nil {
				// Admission granted; clean up and report failure.
				_, _ = kcpctlNoFail(wsConsumer1, "delete", "bucket", "bucket-c1", "-n", "default")

				return fmt.Errorf("create was admitted with webhook at 0 replicas")
			}

			return nil
		})

		By("scaling webhook Deployment back to 1")
		kindctl("scale", "deployment",
			"quota-controller-webhook",
			"-n", quotaNamespace,
			"--replicas=1")

		waitFor(2*time.Minute, "webhook pod ready", func() error {
			_, err := kindctlNoFail("-n", quotaNamespace, "wait", "deployment",
				"quota-controller-webhook",
				"--for=condition=Available", "--timeout=1s")

			return err
		})

		By("creating bucket-c1 — should succeed once webhook is back")

		waitFor(time.Minute, "bucket-c1 admitted after webhook restored", func() error {
			// consumer1 has bucket-a2, bucket-a3, bucket-a4 (3 objects), limit=5
			// so there is room for 2 more.
			_, err := createBucketInline(wsConsumer1, "default", "bucket-c1")

			return err
		})
	})
})
