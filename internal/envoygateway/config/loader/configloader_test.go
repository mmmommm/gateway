// Copyright Envoy Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package loader

import (
	"context"
	_ "embed"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/envoyproxy/gateway/internal/envoygateway/config"
)

var (
	//go:embed testdata/default.yaml
	defaultConfig string
	//go:embed testdata/enable-redis.yaml
	redisConfig string
	//go:embed testdata/standalone.yaml
	standaloneConfig string
	//go:embed testdata/standalone-enable-extension-server.yaml
	standaloneConfigWithExtensionServer string
	//go:embed testdata/minimal.yaml
	minimalConfig string
)

func TestConfigLoader(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := tmpDir + "/config.yaml"
	require.NoError(t, os.WriteFile(cfgPath, []byte(defaultConfig), 0o600))
	s, err := config.New(os.Stdout, os.Stderr)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	var reloads atomic.Int32
	loader := New(cfgPath, s, func(context.Context, *config.Server) error {
		reloads.Add(1)
		return nil
	})
	// Stop the loader's watcher/hook goroutines when the test ends.
	defer cancel()

	require.NoError(t, loader.Start(ctx, os.Stdout))
	require.NoError(t, os.WriteFile(cfgPath, []byte(redisConfig), 0o600))

	// The initial load plus at least one reload of the changed file.
	require.Eventually(t, func() bool {
		return reloads.Load() >= 2
	}, 30*time.Second, 10*time.Millisecond)
	t.Logf("config reloaded %d times", reloads.Load())
}

func TestConfigLoaderStandaloneExtensionServerAndCustomResource(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := tmpDir + "/config.yaml"
	require.NoError(t, os.WriteFile(cfgPath, []byte(standaloneConfig), 0o600))
	s, err := config.New(os.Stdout, os.Stderr)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	var reloads atomic.Int32
	loader := New(cfgPath, s, func(context.Context, *config.Server) error {
		reloads.Add(1)
		return nil
	})
	defer cancel()

	require.NoError(t, loader.Start(ctx, os.Stdout))
	require.NotNil(t, loader.cfg.EnvoyGateway)
	require.Nil(t, loader.cfg.EnvoyGateway.ExtensionManager)

	require.NoError(t, os.WriteFile(cfgPath, []byte(standaloneConfigWithExtensionServer), 0o600))

	// Wait for the reload to surface the extension server config.
	var extMgr *egv1a1.ExtensionManager
	require.Eventually(t, func() bool {
		cfg := loader.snapshotConfig()
		if cfg == nil || cfg.EnvoyGateway == nil || cfg.EnvoyGateway.ExtensionManager == nil {
			return false
		}
		extMgr = cfg.EnvoyGateway.ExtensionManager
		return true
	}, 30*time.Second, 10*time.Millisecond)
	t.Logf("config reloaded %d times", reloads.Load())

	require.NotNil(t, extMgr)
	require.NotNil(t, extMgr.PolicyResources)
	require.Len(t, extMgr.PolicyResources, 1)
	require.Equal(t, "gateway.example.io", extMgr.PolicyResources[0].Group)
	require.Equal(t, "v1alpha1", extMgr.PolicyResources[0].Version)
	require.Equal(t, "ExampleExtPolicy", extMgr.PolicyResources[0].Kind)
}

// TestConfigLoaderDefaultsBeforeValidate ensures the hot-reload path applies defaults
// before validation. A config that omits `gateway` and `provider` fails validation
// outright (`gateway is unspecified`) but is valid once defaults populate those fields.
// Reloading to such a config must therefore succeed and surface the defaulted values
// to the hook.
func TestConfigLoaderDefaultsBeforeValidate(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := tmpDir + "/config.yaml"
	require.NoError(t, os.WriteFile(cfgPath, []byte(defaultConfig), 0o600))
	s, err := config.New(os.Stdout, os.Stderr)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	var reloads atomic.Int32
	loader := New(cfgPath, s, func(context.Context, *config.Server) error {
		reloads.Add(1)
		return nil
	})
	defer cancel()

	require.NoError(t, loader.Start(ctx, os.Stdout))
	require.NoError(t, os.WriteFile(cfgPath, []byte(minimalConfig), 0o600))

	// minimalConfig omits gateway/provider; the reload only fires the hook if it
	// passed validation, which requires defaults to have been applied first. The
	// initial defaultConfig can't be told apart from the defaulted minimalConfig
	// by content, so gate on the reload (the second invocation) instead.
	require.Eventually(t, func() bool {
		return reloads.Load() >= 2
	}, 30*time.Second, 10*time.Millisecond)
	t.Logf("config reloaded %d times", reloads.Load())

	eg := loader.snapshotConfig().EnvoyGateway
	require.NotNil(t, eg)
	require.NotNil(t, eg.Gateway)
	require.Equal(t, egv1a1.GatewayControllerName, eg.Gateway.ControllerName)
	require.NotNil(t, eg.Provider)
	require.Equal(t, egv1a1.ProviderTypeKubernetes, eg.Provider.Type)
	require.NotNil(t, eg.Logging)
}
