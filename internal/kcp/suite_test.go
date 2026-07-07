// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package kcp_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestKCP(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "KCP Suite")
}
