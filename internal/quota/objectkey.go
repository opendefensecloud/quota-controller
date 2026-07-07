// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package quota

// ObjectKey returns the reservation key for a governed object:
// "namespace/name" for namespaced objects, the bare "name" for cluster-scoped
// ones. The admission webhook (reserving) and the accounting reconciler
// (folding live objects into confirmed) must both build keys through this
// function: a mismatch means fulfilled reservations are never matched and
// double-count against the limit until the TTL sweep reclaims them.
func ObjectKey(namespace, name string) string {
	if namespace == "" {
		return name
	}

	return namespace + "/" + name
}
