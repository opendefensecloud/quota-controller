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

// resolveIdentityHash waits for the controller to stamp status.identityHash on a
// provider's self-service ConsumptionQuota and returns it.
func resolveIdentityHash(providerWS string) string {
	GinkgoHelper()

	var hash string

	waitFor(3*time.Minute, "identityHash stamped on "+providerWS+" ConsumptionQuota", func() error {
		out, err := kcpctlNoFail(providerWS, "get", "consumptionquota", ssCQName,
			"-o", "jsonpath={.status.identityHash}")
		if err != nil {
			return err
		}

		hash = strings.TrimSpace(out)
		if hash == "" {
			return fmt.Errorf("identityHash not yet stamped")
		}

		return nil
	})

	return hash
}

// claimNameFor mirrors internal/selfservice.ClaimName: qc-<resource:20>-<id:8>.
func claimNameFor(resource, identityHash string) string {
	if len(resource) > 20 {
		resource = resource[:20]
	}

	if len(identityHash) > 8 {
		identityHash = identityHash[:8]
	}

	return fmt.Sprintf("qc-%s-%s", resource, identityHash)
}

// consumerClusterName returns a consumer workspace's logical-cluster name — the
// exact value the controller keys grants on (logicalcluster.From on the binding).
func consumerClusterName(ws string) string {
	GinkgoHelper()

	var name string

	waitFor(time.Minute, "resolve logical cluster for "+ws, func() error {
		out, err := kcpctlNoFail(ws, "get", "apibinding", "s3.example.com",
			"-o", "jsonpath={.metadata.annotations.kcp\\.io/cluster}")
		if err != nil {
			return err
		}

		name = strings.TrimSpace(out)
		if name == "" {
			return fmt.Errorf("no kcp.io/cluster annotation yet")
		}

		return nil
	})

	return name
}

// findGrant lists QuotaGrants in a provider workspace and returns the (name,
// decision) of the one matching the given consumer cluster + governed buckets.
// The brief mandates matching by spec.consumer + spec.governed, never by
// reconstructing the hashed grant name.
func findGrant(providerWS, consumerCluster string) (name, decision string, found bool) {
	out, err := kcpctlNoFail(providerWS, "get", "quotagrants",
		"-o", `jsonpath={range .items[*]}{.metadata.name}{"|"}{.spec.consumer}{"|"}{.spec.governed.resource}{"|"}{.spec.decision}{"\n"}{end}`)
	if err != nil {
		return "", "", false
	}

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) != 4 {
			continue
		}

		if parts[1] == consumerCluster && parts[2] == "buckets" {
			return parts[0], parts[3], true
		}
	}

	return "", "", false
}

// claimField reads one status jsonpath field from a QuotaClaim.
func claimField(ws, claim, jsonpath string) (string, error) {
	out, err := kcpctlNoFail(ws, "get", "quotaclaim", claim, "-o", "jsonpath="+jsonpath)

	return strings.TrimSpace(out), err
}

// expectClaim asserts a claim's status.phase and status.effectiveLimit.
func expectClaim(ws, claim, wantPhase, wantEffective string) error {
	phase, err := claimField(ws, claim, "{.status.phase}")
	if err != nil {
		return err
	}

	if phase != wantPhase {
		return fmt.Errorf("phase=%q, want %q", phase, wantPhase)
	}

	eff, err := claimField(ws, claim, "{.status.effectiveLimit}")
	if err != nil {
		return err
	}

	if eff != wantEffective {
		return fmt.Errorf("effectiveLimit=%q, want %q", eff, wantEffective)
	}

	return nil
}

// requestLimit patches spec.requestedLimit on a claim as kcp-admin (a functional
// trigger for the reconciler; the MPP-scoped write path is asserted in scenario 5).
func requestLimit(ws, claim string, n int) {
	GinkgoHelper()
	kcpctl(ws, "patch", "quotaclaim", claim, "--type=merge",
		"-p", fmt.Sprintf(`{"spec":{"requestedLimit":%d}}`, n))
}

// denyOverLimit asserts that creating one more bucket than currently allowed is
// rejected with the enforcement message.
func denyOverLimit(ws, name string) error {
	out, err := createBucketInline(ws, "default", name)
	if err == nil {
		_, _ = kcpctlNoFail(ws, "delete", "bucket", name, "-n", "default")

		return fmt.Errorf("%s was admitted, expected denial", name)
	}

	if !strings.Contains(out, "consumption quota exceeded") {
		return fmt.Errorf("unexpected error (no quota message): %s", out)
	}

	return nil
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

			// kubectl prints "No resources found in default namespace." (to stderr,
			// captured here) when the list is empty — that is the cleared state.
			if strings.TrimSpace(out) == "" || strings.Contains(out, "No resources found") {
				return nil
			}

			return fmt.Errorf("buckets still present in consumer2: %s", out)
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

// Self-service request/approval + workflow-integrity (design spec §15). The six
// scenarios run against the topology provisioned in setupSelfServiceWorkspaces:
// providers ss-provider-a/-b (both governing s3.example.com/buckets, distinct
// identityHashes; defaultLimit=2, autoApproveCeiling=5) and consumers ssc-auto/
// -manual/-reject (provider A) and ssc-second (provider B).
var _ = Describe("self-service", Ordered, func() {
	var (
		idA           string // ss-provider-a's s3 export identityHash
		idB           string // ss-provider-b's s3 export identityHash
		claimA        string // qc-buckets-<idA8> — the claim name in every provider-A consumer
		claimB        string // qc-buckets-<idB8>
		manualCluster string
		rejectCluster string
	)

	BeforeAll(func() {
		idA = resolveIdentityHash(wsSSProviderA)
		idB = resolveIdentityHash(wsSSProviderB)
		claimA = claimNameFor("buckets", idA)
		claimB = claimNameFor("buckets", idB)
		manualCluster = consumerClusterName(wsSSCManual)
		rejectCluster = consumerClusterName(wsSSCReject)
	})

	// Scenario 1 (visibility, R11): after binding both exports the controller
	// pre-creates a QuotaClaim named qc-<resource>-<id8>, visible to the consumer,
	// showing the current effective (default) limit.
	It("Scenario 1: pre-creates a QuotaClaim at the default limit, visible to the consumer", func() {
		By("waiting for the claim to be pre-created by the discovery sweep")

		waitFor(3*time.Minute, "claim "+claimA+" pre-created in ssc-auto at default 2", func() error {
			// A successful get by the computed qc-<resource>-<id8> name proves the
			// naming contract; effectiveLimit==2 proves it shows the default.
			return expectClaim(wsSSCAuto, claimA, "None", "2")
		})
	})

	// Scenario 2 (auto-approve, §8): a request at/below the ceiling is granted
	// automatically and enforcement follows — N ok, N+1 denied.
	It("Scenario 2: auto-approves a request <= ceiling and enforces the raised limit", func() {
		By("consumer requests 4 (<= ceiling 5)")
		requestLimit(wsSSCAuto, claimA, 4)

		By("claim becomes Approved with effectiveLimit 4")

		waitFor(2*time.Minute, "claim Approved at 4", func() error {
			return expectClaim(wsSSCAuto, claimA, "Approved", "4")
		})

		By("consumer creates 4 buckets (all admitted once enforcement picks up the grant)")

		for i := 1; i <= 4; i++ {
			name := fmt.Sprintf("auto-b%d", i)
			waitFor(2*time.Minute, "create "+name, func() error {
				_, err := createBucketInline(wsSSCAuto, "default", name)

				return err
			})
		}

		By("the 5th CREATE is denied with the enforcement message")

		waitFor(time.Minute, "5th bucket denied", func() error {
			return denyOverLimit(wsSSCAuto, "auto-b5")
		})
	})

	// Scenario 3 (manual approval, §8): a request above the ceiling parks the
	// claim Pending, the provider approves the grant, and enforcement honors it.
	It("Scenario 3: routes an above-ceiling request to Pending, then honors provider approval", func() {
		By("waiting for the manual consumer's claim to be pre-created")

		waitFor(3*time.Minute, "claim pre-created in ssc-manual", func() error {
			_, err := claimField(wsSSCManual, claimA, "{.status.phase}")

			return err
		})

		By("consumer requests 9 (> ceiling 5)")
		requestLimit(wsSSCManual, claimA, 9)

		By("claim becomes Pending")

		waitFor(2*time.Minute, "claim Pending", func() error {
			phase, err := claimField(wsSSCManual, claimA, "{.status.phase}")
			if err != nil {
				return err
			}

			if phase != "Pending" {
				return fmt.Errorf("phase=%q, want Pending", phase)
			}

			return nil
		})

		By("a Pending QuotaGrant exists in the provider workspace")

		var grantName string

		waitFor(2*time.Minute, "pending grant for manual consumer", func() error {
			name, decision, ok := findGrant(wsSSProviderA, manualCluster)
			if !ok {
				return fmt.Errorf("no grant for consumer %s yet", manualCluster)
			}

			if decision != "Pending" {
				return fmt.Errorf("grant decision=%q, want Pending", decision)
			}

			grantName = name

			return nil
		})

		By("provider approves the grant with grantedLimit 7")
		kcpctl(wsSSProviderA, "patch", "quotagrant", grantName, "--type=merge",
			"-p", `{"spec":{"decision":"Approved","grantedLimit":7}}`)

		By("claim becomes Approved with effectiveLimit 7")

		waitFor(2*time.Minute, "claim Approved at 7", func() error {
			return expectClaim(wsSSCManual, claimA, "Approved", "7")
		})

		By("enforcement honors the raised limit: 3 buckets (> old default 2) are admitted")

		for i := 1; i <= 3; i++ {
			name := fmt.Sprintf("manual-b%d", i)
			waitFor(2*time.Minute, "create "+name, func() error {
				_, err := createBucketInline(wsSSCManual, "default", name)

				return err
			})
		}
	})

	// Scenario 4 (rejection, §8): a rejected grant leaves the claim Rejected and
	// enforcement pinned at the default.
	It("Scenario 4: a rejected grant leaves enforcement at the default", func() {
		By("waiting for the reject consumer's claim to be pre-created")

		waitFor(3*time.Minute, "claim pre-created in ssc-reject", func() error {
			_, err := claimField(wsSSCReject, claimA, "{.status.phase}")

			return err
		})

		By("consumer requests 9 (> ceiling 5)")
		requestLimit(wsSSCReject, claimA, 9)

		By("a Pending QuotaGrant exists in the provider workspace")

		var grantName string

		waitFor(2*time.Minute, "pending grant for reject consumer", func() error {
			name, decision, ok := findGrant(wsSSProviderA, rejectCluster)
			if !ok {
				return fmt.Errorf("no grant for consumer %s yet", rejectCluster)
			}

			if decision != "Pending" {
				return fmt.Errorf("grant decision=%q, want Pending", decision)
			}

			grantName = name

			return nil
		})

		By("provider rejects the grant")
		kcpctl(wsSSProviderA, "patch", "quotagrant", grantName, "--type=merge",
			"-p", `{"spec":{"decision":"Rejected"}}`)

		By("claim becomes Rejected and effectiveLimit stays at the default 2")

		waitFor(2*time.Minute, "claim Rejected at default 2", func() error {
			return expectClaim(wsSSCReject, claimA, "Rejected", "2")
		})

		By("enforcement stays at the default: 2 admitted, the 3rd denied")

		for i := 1; i <= 2; i++ {
			name := fmt.Sprintf("reject-b%d", i)
			waitFor(2*time.Minute, "create "+name, func() error {
				_, err := createBucketInline(wsSSCReject, "default", name)

				return err
			})
		}

		waitFor(time.Minute, "3rd bucket denied", func() error {
			return denyOverLimit(wsSSCReject, "reject-b3")
		})
	})

	// Scenario 5 (workflow integrity / maximalPermissionPolicy, §10, ADR-002):
	// as the adversarial consumer-workspace admin (cluster-admin locally, only
	// system:authenticated globally) — create/delete/status-write on a QuotaClaim
	// are Forbidden by the MPP ceiling, but updating spec.requestedLimit succeeds.
	It("Scenario 5: the maximalPermissionPolicy locks the QuotaClaim surface", func() {
		kube := sscAutoAdminKubeconfig

		By("create of a new QuotaClaim is Forbidden")

		manifest := fmt.Sprintf(`apiVersion: quota.opendefense.cloud/v1alpha1
kind: QuotaClaim
metadata:
  name: qc-forbidden-probe
spec:
  governed:
    group: s3.example.com
    resource: buckets
    identityHash: %s
`, idA)
		out, err := asIdentityStdin(kube, wsSSCAuto, manifest, "create", "-f", "-")
		Expect(err).To(HaveOccurred(), "create should be denied by the MPP")
		Expect(out).To(ContainSubstring("Forbidden"))
		Expect(out).To(ContainSubstring(`cannot create resource "quotaclaims"`))

		By("delete of the controller-created claim is Forbidden")
		out, err = asIdentityNoFail(kube, wsSSCAuto, "delete", "quotaclaim", claimA)
		Expect(err).To(HaveOccurred(), "delete should be denied by the MPP")
		Expect(out).To(ContainSubstring("Forbidden"))
		Expect(out).To(ContainSubstring(`cannot delete resource "quotaclaims"`))

		By("writing quotaclaims/status is Forbidden")
		out, err = asIdentityNoFail(kube, wsSSCAuto, "patch", "quotaclaim", claimA,
			"--subresource=status", "--type=merge", "-p", `{"status":{"effectiveLimit":999}}`)
		Expect(err).To(HaveOccurred(), "status write should be denied by the MPP")
		Expect(out).To(ContainSubstring("Forbidden"))
		Expect(out).To(ContainSubstring(`quotaclaims/status`))

		By("updating spec.requestedLimit succeeds")
		out, err = asIdentityNoFail(kube, wsSSCAuto, "patch", "quotaclaim", claimA,
			"--type=merge", "-p", `{"spec":{"requestedLimit":3}}`)
		Expect(err).NotTo(HaveOccurred(), out)

		By("the controller-created claim still exists (delete was denied)")
		_, err = claimField(wsSSCAuto, claimA, "{.metadata.name}")
		Expect(err).NotTo(HaveOccurred())
	})

	// Scenario 6 (identity keying, §15): two providers of the SAME group/resource
	// carry distinct identityHashes, so their claims/ledgers are separate — a
	// grant approved under provider A never leaks into provider B's claim.
	It("Scenario 6: two providers of the same resource keep separate identity ledgers", func() {
		By("the two providers have distinct identity hashes and claim names")
		Expect(idA).NotTo(BeEmpty())
		Expect(idB).NotTo(BeEmpty())
		Expect(idA).NotTo(Equal(idB))
		Expect(claimA).NotTo(Equal(claimB), "claim names must differ by identityHash")

		By("provider B's consumer has its own claim at provider B's default")

		waitFor(3*time.Minute, "claim "+claimB+" pre-created in ssc-second at default 2", func() error {
			eff, err := claimField(wsSSCSecond, claimB, "{.status.effectiveLimit}")
			if err != nil {
				return err
			}

			if eff != "2" {
				return fmt.Errorf("effectiveLimit=%q, want 2", eff)
			}

			return nil
		})

		By("provider A's approval did not leak into provider B's claim")
		phaseB, err := claimField(wsSSCSecond, claimB, "{.status.phase}")
		Expect(err).NotTo(HaveOccurred())
		Expect(phaseB).NotTo(Equal("Approved"), "provider B claim must be untouched by provider A grants")

		// The provider-A consumer (ssc-auto) is Approved (scenarios 2/5); confirming
		// it stays Approved while provider B stays at default demonstrates the
		// per-identity separation.
		phaseA, err := claimField(wsSSCAuto, claimA, "{.status.phase}")
		Expect(err).NotTo(HaveOccurred())
		Expect(phaseA).To(Equal("Approved"))
	})
})
