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

import "testing"

func TestPlacementModelValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		regions   []string
		placement *PlacementPolicy
		valid     bool
	}{
		{"singleton", []string{"westus3"}, nil, true},
		{"multiple", []string{"westus3", "centralus"}, nil, true},
		{"omitted", nil, nil, false},
		{"empty", []string{}, nil, false},
		{"blank", []string{""}, nil, false},
		{"whitespace", []string{" westus3"}, nil, false},
		{"duplicate", []string{"westus3", "westus3"}, nil, false},
		{"open", nil, &PlacementPolicy{DefaultRegion: "uksouth", AllowUndeclared: true}, true},
		{"no default", nil, &PlacementPolicy{AllowUndeclared: true}, false},
		{"closed default", nil, &PlacementPolicy{DefaultRegion: "uksouth"}, false},
		{"contradiction", []string{"uksouth"}, &PlacementPolicy{DefaultRegion: "uksouth", AllowUndeclared: true}, false},
		{"explicit empty contradiction", []string{}, &PlacementPolicy{DefaultRegion: "uksouth", AllowUndeclared: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePlacement("dev", Pool{Name: "p", Regions: tc.regions, Placement: tc.placement})
			if (err == nil) != tc.valid {
				t.Fatalf("validation = %v, valid %t", err, tc.valid)
			}
		})
	}
}

func TestEnvironmentPlacementShapes(t *testing.T) {
	t.Parallel()
	single := Pool{Regions: []string{"westus3"}}
	otherSingle := Pool{Regions: []string{"uksouth"}}
	multi := Pool{Regions: []string{"westus3", "centralus"}}
	reversed := Pool{Regions: []string{"centralus", "westus3"}}
	open := Pool{Placement: &PlacementPolicy{DefaultRegion: "uksouth", AllowUndeclared: true}}
	for _, tc := range []struct {
		name  string
		pools []Pool
		valid bool
	}{
		{"different singletons", []Pool{single, otherSingle}, true},
		{"identical ordered multiple", []Pool{multi, multi}, true},
		{"open defaults", []Pool{open, open}, true},
		{"ordered mismatch", []Pool{multi, reversed}, false},
		{"singleton multiple", []Pool{single, multi}, false},
		{"multiple singleton", []Pool{multi, single}, false},
		{"open singleton", []Pool{open, single}, false},
		{"multiple open", []Pool{multi, open}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEnvironmentPlacement("dev", tc.pools)
			if (err == nil) != tc.valid {
				t.Fatalf("validation = %v, valid %t", err, tc.valid)
			}
		})
	}
}

func TestExplicitProvisioningIndependentOfPlacement(t *testing.T) {
	t.Parallel()
	for _, pool := range []Pool{
		{Name: "singleton", Regions: []string{"eastus2"}},
		{Name: "multiple", Regions: []string{"eastus2", "centralus"}},
		{Name: "open", Placement: &PlacementPolicy{DefaultRegion: "uksouth", AllowUndeclared: true}},
	} {
		t.Run(pool.Name, func(t *testing.T) {
			pool.SlotCount = 1
			pool.SlotAssets.E2EIdentities = &E2EIdentitiesAsset{Allocation: AllocationDedicated, ProvisioningRegion: "westus3", ResourceGroupPrefix: "identities", ResourceGroupCount: 1}
			if err := normalizeE2EIdentities("dev", &pool); err != nil {
				t.Fatal(err)
			}
			if got := ExpandSlotsForPool("dev", pool)[0].Assets.E2EIdentities.ProvisioningRegion; got != "westus3" {
				t.Fatalf("provisioning region = %q", got)
			}
			pool.SlotAssets.E2EIdentities.ProvisioningRegion = ""
			if err := normalizeE2EIdentities("dev", &pool); err == nil {
				t.Fatal("accepted implicit provisioning")
			}
		})
	}
}
