// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package accounting_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAccounting(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Accounting Suite")
}
