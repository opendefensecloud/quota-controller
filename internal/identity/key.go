// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// UsageKey identifies one accounting ledger: a consumer workspace and a governed
// resource identity (spec §6.4).
type UsageKey struct {
	Cluster      string
	Group        string
	Resource     string
	IdentityHash string
}

// ObjectName returns a deterministic, DNS-1123-valid cluster-scoped name for the
// QuotaUsage object backing this key.
func (k UsageKey) ObjectName() string {
	raw := fmt.Appendf(nil, "%s|%s|%s|%s", k.Cluster, k.Group, k.Resource, k.IdentityHash)
	sum := sha256.Sum256(raw)

	return "qu-" + hex.EncodeToString(sum[:])[:32]
}
