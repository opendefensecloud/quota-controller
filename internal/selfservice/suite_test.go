// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package selfservice_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSelfService(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SelfService Suite")
}
