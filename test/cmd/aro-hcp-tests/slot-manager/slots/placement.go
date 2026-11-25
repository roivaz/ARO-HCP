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

package slots

import (
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
)

// PlacementPolicy preserves an open pool's default without coupling provisioning to run placement.
type PlacementPolicy struct {
	DefaultRegion   string `yaml:"default_region"`
	AllowUndeclared bool   `yaml:"allow_undeclared"`
}

func validatePlacement(environment string, pool Pool) error {
	if pool.Placement != nil {
		if pool.Regions != nil || !pool.Placement.AllowUndeclared || strings.TrimSpace(pool.Placement.DefaultRegion) == "" || pool.Placement.DefaultRegion != strings.TrimSpace(pool.Placement.DefaultRegion) {
			return fmt.Errorf("environment %q pool %q requires either nonempty regions or placement with default_region and allow_undeclared: true", environment, pool.Name)
		}
		return nil
	}
	seen := sets.New[string]()
	for _, region := range pool.Regions {
		if strings.TrimSpace(region) == "" || region != strings.TrimSpace(region) || seen.Has(region) {
			return fmt.Errorf("environment %q pool %q has empty, malformed or duplicate regions", environment, pool.Name)
		}
		seen.Insert(region)
	}
	if len(pool.Regions) == 0 {
		return fmt.Errorf("environment %q pool %q must declare nonempty regions or open placement", environment, pool.Name)
	}
	return nil
}

func validateEnvironmentPlacement(environment string, pools []Pool) error {
	first := pools[0]
	for _, pool := range pools[1:] {
		if (first.Placement != nil) != (pool.Placement != nil) {
			return fmt.Errorf("environment %q mixes open and closed placement", environment)
		}
		if first.Placement != nil || len(first.Regions) == 1 && len(pool.Regions) == 1 {
			continue
		}
		if !slices.Equal(first.Regions, pool.Regions) {
			return fmt.Errorf("environment %q multi-region pools must declare the same ordered regions", environment)
		}
	}
	return nil
}

// rawSlotRegion is a journal/export compatibility projection, not run placement.
// Historical weighted slots kept this field empty, including weighted singletons.
// The independent identity list remains valid after legacy request removal.
func rawSlotRegion(environment string, pool Pool) string {
	for _, identity := range historicalEmptySlotRegions {
		if identity.environment == environment && identity.name == pool.Name && identity.subscription == pool.Subscriptions.E2E && identity.resourceType == pool.ResourceType {
			return ""
		}
	}
	if pool.Placement != nil {
		return pool.Placement.DefaultRegion
	}
	if len(pool.Regions) == 1 {
		return pool.Regions[0]
	}
	return ""
}
