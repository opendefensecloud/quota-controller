// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package kcp

import (
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WorkspaceConfig returns a copy of base scoped to a single logical cluster
// reached directly through the front-proxy: {base.Host}/clusters/{cluster}. A
// trailing slash on base.Host is tolerated. Use it for reads the quota-provider
// virtual workspace does not serve (APIExports, APIResourceSchemas, endpoint
// slices), which must go to the provider workspace directly.
func WorkspaceConfig(base *rest.Config, cluster string) *rest.Config {
	return scopedConfig(base, base.Host, cluster)
}

// VWConfig returns a copy of base scoped through a virtual-workspace URL:
// {vwURL}/clusters/{cluster}. Pass a concrete logical-cluster name to target one
// workspace, or "*" to reach every workspace the VW serves.
func VWConfig(base *rest.Config, vwURL, cluster string) *rest.Config {
	return scopedConfig(base, vwURL, cluster)
}

// WorkspaceClient is WorkspaceConfig wrapped in a controller-runtime client.
func WorkspaceClient(base *rest.Config, cluster string, scheme *runtime.Scheme) (client.Client, error) {
	return client.New(WorkspaceConfig(base, cluster), client.Options{Scheme: scheme})
}

// VWClient is VWConfig wrapped in a controller-runtime client.
func VWClient(base *rest.Config, vwURL, cluster string, scheme *runtime.Scheme) (client.Client, error) {
	return client.New(VWConfig(base, vwURL, cluster), client.Options{Scheme: scheme})
}

func scopedConfig(base *rest.Config, hostPrefix, cluster string) *rest.Config {
	cfg := rest.CopyConfig(base)
	cfg.Host = strings.TrimRight(hostPrefix, "/") + "/clusters/" + cluster

	return cfg
}
