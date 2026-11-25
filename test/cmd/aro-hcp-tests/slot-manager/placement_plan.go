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
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/Azure/ARO-HCP/test/cmd/aro-hcp-tests/slot-manager/slots"
)

// acquisitionPlan is a pure snapshot: retries rotate pools but never redraw regions.
// Calling planAcquisition for two requests provides an opt-in comparison hook
// without acquiring leases or publishing runtime state.
type acquisitionPlan struct {
	route          string
	pools          []slots.Pool
	regions        map[string]string
	legacyControls []string
	exclusions     map[string]string
	selection      RegionSelection
}

func planAcquisition(catalog *slots.Catalog, environment string, request acquireRequest, subscriptions []string, weights, buildID string) (acquisitionPlan, error) {
	plan := acquisitionPlan{route: "selector", regions: map[string]string{}, exclusions: map[string]string{}}
	var candidates []slots.Pool
	var err error
	if request.legacy {
		plan.route = "legacy"
		if len(subscriptions) > 0 {
			plan.legacyControls = append(plan.legacyControls, "allowed-subscriptions")
		}
		if request.placement != "" {
			plan.legacyControls = append(plan.legacyControls, "location-override")
		}
		candidates, err = legacyCandidatePools(catalog, environment, subscriptions)
	} else {
		candidates, err = catalog.SelectPools(environment, request.selector)
	}
	if err != nil {
		return plan, fmt.Errorf("planning %s request with placement %q: %w", plan.route, request.placement, err)
	}
	selected := sets.New[string]()
	for _, pool := range candidates {
		selected.Insert(pool.Name)
	}
	for _, pool := range catalog.Environments[environment].Pools {
		if !selected.Has(pool.Name) {
			plan.exclusions[pool.Name] = "request selector/filter did not match"
		}
	}
	var weightedRegions []string
	var shape string
	for _, pool := range candidates {
		if request.placement != "" {
			if pool.Placement == nil && !slices.Contains(pool.Regions, request.placement) {
				plan.exclusions[pool.Name] = fmt.Sprintf("hard placement is not declared by pool; allowed regions: %s", strings.Join(pool.Regions, ","))
				continue
			}
			plan.regions[pool.Name] = request.placement
			plan.pools = append(plan.pools, pool)
			continue
		}
		weighted := len(pool.Regions) > 1
		poolShape := "singleton"
		if weighted {
			poolShape = "weighted"
		}
		if pool.Placement != nil {
			poolShape = "open"
		}
		if shape != "" && shape != poolShape {
			return plan, fmt.Errorf("environment %q candidates mix incompatible placement policies", environment)
		}
		shape = poolShape
		switch poolShape {
		case "weighted":
			if weightedRegions != nil && !slices.Equal(weightedRegions, pool.Regions) {
				return plan, fmt.Errorf("environment %q candidates have different ordered region lists", environment)
			}
			if weightedRegions == nil {
				weightedRegions = pool.Regions
			}
		case "open":
			plan.regions[pool.Name] = pool.Placement.DefaultRegion
		default:
			if len(pool.Regions) != 1 {
				return plan, fmt.Errorf("pool %q has no automatic placement", pool.Name)
			}
			plan.regions[pool.Name] = pool.Regions[0]
		}
		plan.pools = append(plan.pools, pool)
	}
	if len(plan.pools) == 0 {
		return plan, fmt.Errorf("no pool found for environment %q: selector=%q placement=%q exclusions=%v", environment, request.selector, request.placement, plan.exclusions)
	}
	plan.selection = RegionSelection{RuntimeRegion: request.placement, LocationOverrideUsed: request.placement != ""}
	if request.placement != "" {
		return plan, nil
	}
	if weightedRegions == nil {
		if strings.TrimSpace(weights) != "" {
			return plan, fmt.Errorf("LOCATION_WEIGHTS is inapplicable to automatic singleton/open-default placement")
		}
		return plan, nil
	}
	plan.selection, err = resolveRegionSelection(weightedRegions, environment, weights, buildID)
	if err != nil {
		return plan, err
	}
	for _, pool := range plan.pools {
		plan.regions[pool.Name] = plan.selection.RuntimeRegion
	}
	return plan, nil
}
