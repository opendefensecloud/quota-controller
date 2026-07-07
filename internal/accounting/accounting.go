// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

// Package accounting holds the pure reservation math for QuotaUsage. No Kubernetes
// client — this is where the strict, no-overshoot guarantee is defined and tested.
package accounting

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
)

func metaTime(t time.Time) metav1.Time { return metav1.NewTime(t) }

// Used = confirmed + live (non-expired) reservations (spec §7).
func Used(st v1alpha1.QuotaUsageStatus, now time.Time) int32 {
	live := int32(0)
	for _, r := range st.Reservations {
		if r.ExpiresAt.Time.After(now) {
			live++
		}
	}

	return st.Confirmed + live
}

// Reserve grants a slot for objKey iff Used < limit. Idempotent: if objKey is already
// a live reservation it returns the status unchanged with ok=true (admission retry).
func Reserve(st v1alpha1.QuotaUsageStatus, objKey string, limit int32, now time.Time, ttl time.Duration) (v1alpha1.QuotaUsageStatus, bool) {
	for _, r := range st.Reservations {
		if r.Key == objKey && r.ExpiresAt.Time.After(now) {
			return st, true
		}
	}
	if Used(st, now) >= limit {
		return st, false
	}
	out := *st.DeepCopy()
	out.Reservations = append(out.Reservations, v1alpha1.Reservation{
		Key:       objKey,
		ExpiresAt: metaTime(now.Add(ttl)),
	})

	return out, true
}

// Sweep removes expired reservations (orphaned admits whose create never landed).
func Sweep(st v1alpha1.QuotaUsageStatus, now time.Time) v1alpha1.QuotaUsageStatus {
	out := *st.DeepCopy()
	kept := out.Reservations[:0]
	for _, r := range out.Reservations {
		if r.ExpiresAt.Time.After(now) {
			kept = append(kept, r)
		}
	}
	out.Reservations = kept

	return out
}

// FoldConfirmed sets Confirmed to the true live count and drops reservations whose
// object has now been observed (liveKeys) — the fulfilled-reservation hand-off (spec §7.2).
func FoldConfirmed(st v1alpha1.QuotaUsageStatus, trueCount int32, liveKeys sets.Set[string]) v1alpha1.QuotaUsageStatus {
	out := *st.DeepCopy()
	out.Confirmed = trueCount
	kept := out.Reservations[:0]
	for _, r := range out.Reservations {
		if !liveKeys.Has(r.Key) {
			kept = append(kept, r)
		}
	}
	out.Reservations = kept

	return out
}
