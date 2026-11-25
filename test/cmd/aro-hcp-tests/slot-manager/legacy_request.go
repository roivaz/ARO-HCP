// Copyright 2026 Microsoft Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package slotmanager

import (
	"fmt"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/Azure/ARO-HCP/test/cmd/aro-hcp-tests/slot-manager/slots"
)

type acquireRequest struct {
	legacy    bool
	selector  string
	placement string
}

func effectiveAcquireEnv(baseline, override string) string {
	if value := strings.TrimSpace(os.Getenv(override)); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(baseline))
}

// decodeAcquireRequest retains source presence independently of flag precedence.
// In particular an explicitly empty flag cannot conceal inherited old controls.
func (o *RawAcquireOptions) decodeAcquireRequest() (acquireRequest, error) {
	changed := func(name string) bool { return o.flags != nil && o.flags.Changed(name) }
	// The location allowlist's meaning depended on the old fixed/weighted/runtime-selected class.
	// It is retired rather than emulated, and ignoring it would broaden pool selection.
	if len(o.AllowedLocations) != 0 {
		return acquireRequest{}, fmt.Errorf("--allowed-locations/ALLOWED_LOCATIONS is retired and a nonempty value is not accepted; use --placement-region (or MULTISTAGE_PARAM_OVERRIDE_LOCATION) for a hard location")
	}
	newPlacement := changed("placement-region") || strings.TrimSpace(o.PlacementRegion) != "" || o.newPlacementEnv
	newAPI := changed("pool-selector") || newPlacement || strings.TrimSpace(o.PoolSelector) != ""
	request := acquireRequest{legacy: !newAPI, selector: o.PoolSelector, placement: strings.TrimSpace(o.PlacementRegion)}
	if changed("placement-region") && request.placement == "" {
		return request, fmt.Errorf("--placement-region must not be empty when explicitly supplied")
	}
	if newAPI && (o.legacySelectorEnv || changed("allowed-subscriptions") || changed("allowed-locations") || len(o.AllowedSubscriptions) != 0) {
		return request, fmt.Errorf("new pool/placement controls cannot be combined with deprecated allowed-subscriptions controls")
	}
	alias := strings.TrimSpace(o.SelectedLocation)
	if newPlacement && alias != "" {
		return request, fmt.Errorf("placement-region and MULTISTAGE_PARAM_OVERRIDE_LOCATION are duplicate placement sources")
	}
	if request.placement == "" {
		request.placement = alias
	}
	return request, nil
}

// legacyCandidatePools applies only the deprecated subscription filter. Location handling is
// shared with the new route in planAcquisition, so no class-specific legacy semantics remain.
func legacyCandidatePools(catalog *slots.Catalog, environment string, subscriptions []string) ([]slots.Pool, error) {
	env, found := catalog.Environments[environment]
	if !found {
		return nil, fmt.Errorf("unknown environment %q", environment)
	}
	allowed := sets.New(subscriptions...)
	matches := make([]slots.Pool, 0, len(env.Pools))
	for _, pool := range env.Pools {
		if allowed.Len() == 0 || allowed.Has(pool.Subscriptions.E2E) {
			matches = append(matches, pool)
		}
	}
	if len(matches) == 0 {
		exclusions := make([]string, 0, len(env.Pools))
		for _, pool := range env.Pools {
			exclusions = append(exclusions, fmt.Sprintf("pool %q (subscription=%q): subscription mismatch", pool.Name, pool.Subscriptions.E2E))
		}
		return nil, fmt.Errorf("no pool found for environment %q matching legacy allowed_subscriptions=%q; exclusions: %s", environment, sets.List(allowed), strings.Join(exclusions, "; "))
	}
	return matches, nil
}
