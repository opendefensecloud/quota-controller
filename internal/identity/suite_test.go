// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package identity_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIdentity(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Identity Suite")
}
