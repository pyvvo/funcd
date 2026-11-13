package main

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The config keys' descriptions copy these bounds and defaults, so a changed one fails here until they follow
// (ADR-0323 Decision 4). Two defaults are pinned as funcd applies them to an empty key.
func TestDocNamesTheConstant(t *testing.T) {
	cfg := reflect.TypeFor[config.Config]()
	_, bootMax, err := bootBackoff(config.Defaults())
	require.NoError(t, err)
	doctag.NamesAt(t, cfg, "runtime.bootBackoffMax", v1.Duration(bootMax).String())
	p, err := pacing(config.Defaults())
	require.NoError(t, err)
	doctag.NamesAt(t, cfg, "runtime.readinessTimeout", v1.Duration(p.ReadinessTimeout).String())
	doctag.NamesAt(t, cfg, "runtime.process.stopGrace", maxStopGrace.String())
	doctag.NamesAt(t, cfg, "runtime.containerd.stopGrace", maxStopGrace.String())
	doctag.NamesAt(t, cfg, "s3gateway.keyRotationPeriod", minKeyRotationPeriod.String(), maxStopGrace.String())
	doctag.NamesAt(t, cfg, "controller.retryBackoffMax", "at least "+minRetryBackoffMax.String())
	doctag.NamesAt(t, cfg, "eventing.deliveryBackoffMax", v1.Duration(minDeliveryBackoffMax).String())
	doctag.NamesAt(t, cfg, "app.upgradeTimeout", v1.Duration(minAppUpgradeTimeout).String())
	doctag.NamesAt(t, cfg, "auth.credentials[].tokenFile", strconv.Itoa(maxTokenFileBytes))
}
