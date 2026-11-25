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
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Azure/ARO-HCP/test/pkg/leaseenv"
)

type legacyPoolGolden struct {
	Environment        string
	Name               string
	ProvisioningRegion string
	PhysicalCount      int
	Slots              []ExpandedSlot
	ProvisionedGroups  [][]string
	Journal            []string
	Exports            []string
}

type legacyInventoryGolden struct {
	Pools           []legacyPoolGolden
	Assets          []AssetInventory
	Boskos          map[string][]string
	BoskosTypes     string
	BoskosResources string
}

// Expectations were executed against the pre-migration Go source and v2 catalog
// frozen in legacy-provenance.json, not derived from the v3 reader.
func TestLegacyInventoryGoldenCompatibility(t *testing.T) {
	data, err := os.ReadFile("testdata/legacy-v2-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden legacyInventoryGolden
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog("")
	if err != nil {
		t.Fatal(err)
	}
	inventories, err := catalog.AssetInventories()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inventories, golden.Assets) {
		t.Fatalf("asset provisioning inventory drift: got %#v, want %#v", inventories, golden.Assets)
	}
	boskos, err := ExpectedBoskosResources(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(boskos, golden.Boskos) {
		t.Fatalf("full Boskos identity inventory drift: got %#v, want %#v", boskos, golden.Boskos)
	}
	resources, err := RenderBoskosResourcesBlock(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if resources != golden.BoskosResources || RenderBoskosTypesBlock(catalog) != golden.BoskosTypes {
		t.Fatal("Boskos generated source drift")
	}
	poolCount := 0
	for _, environment := range catalog.Environments {
		poolCount += len(environment.Pools)
	}
	if poolCount != len(golden.Pools) {
		t.Fatalf("pool coverage: got %d, want %d", poolCount, len(golden.Pools))
	}
	for _, want := range golden.Pools {
		t.Run(want.Environment+"/"+want.Name, func(t *testing.T) {
			var pool *Pool
			for _, candidate := range catalog.Environments[want.Environment].Pools {
				if candidate.Name == want.Name {
					pool = &candidate
					break
				}
			}
			if pool == nil {
				t.Fatal("frozen pool missing")
			}
			if pool.EffectiveIdentityProvisioningRegion() != want.ProvisioningRegion || pool.EffectiveIdentityContainerProvisioningCount() != want.PhysicalCount {
				t.Fatal("identity provisioning parameters drift")
			}
			raw := ExpandSlotsForPool(want.Environment, *pool)
			if !reflect.DeepEqual(raw, want.Slots) {
				t.Fatalf("raw expanded slots drift: got %#v, want %#v", raw, want.Slots)
			}
			for i, slot := range raw {
				if !reflect.DeepEqual(slot.ProvisionedIdentityContainerNames(), want.ProvisionedGroups[i]) {
					t.Fatalf("slot %s physical identity names drift", slot.ResourceName)
				}
				var state AcquiredSlotState
				if err := yaml.Unmarshal([]byte(want.Journal[i]), &state); err != nil {
					t.Fatal(err)
				}
				if err := state.ValidateForAdmission(); err != nil {
					t.Fatal(err)
				}
				shared := t.TempDir()
				if err := WriteAcquiredSlotState(shared, &state); err != nil {
					t.Fatal(err)
				}
				loaded, err := LoadAcquiredSlotState(shared)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(loaded, &state) {
					t.Fatal("legacy journal roundtrip changed state")
				}
				journal, err := yaml.Marshal(loaded)
				if err != nil {
					t.Fatal(err)
				}
				if string(journal) != want.Journal[i] {
					t.Fatal("legacy journal wire bytes changed")
				}
				// Construct the current writer's state from current expansion and
				// inventory, independently of the decoded historical journal.
				slot.Subscriptions.E2E.ID = "e2e00000-0000-4000-8000-000000000001"
				slot.Subscriptions.Infrastructure.ID = "1f000000-0000-4000-8000-000000000002"
				region := slot.Region
				if region == "" {
					region = "westus3"
				}
				current := AcquiredSlotState{
					Version:            2,
					DeployEnvironment:  slot.DeployEnvironment,
					RuntimeRegion:      region,
					Slot:               slot,
					LeasedResourceName: slot.ResourceName,
					Leases: LeaseSet{Primary: Lease{
						ResourceType: slot.ResourceType,
						ResourceName: slot.ResourceName,
					}},
				}
				for _, requirement := range slot.AssetRequirements {
					if requirement.Kind == KindMockIdentities {
						current.Slot.Assets.MockIdentities = &leaseenv.MockIdentityBundlePayload{SlotName: slot.ResourceName, SubscriptionID: slot.Subscriptions.E2E.ID,
							MSI:             leaseenv.MockPrincipal{ClientID: "a1000000-0000-4000-8000-000000000001", PrincipalID: "a1000000-0000-4000-8000-000000000002", CertificateName: "fixture-msi"},
							Backend:         leaseenv.MockPrincipal{ClientID: "a1000000-0000-4000-8000-000000000003", PrincipalID: "a1000000-0000-4000-8000-000000000004", CertificateName: "fixture-backend"},
							ClustersService: leaseenv.MockPrincipal{ClientID: "a1000000-0000-4000-8000-000000000005", PrincipalID: "a1000000-0000-4000-8000-000000000006", CertificateName: "fixture-cs"}}
					}
					if requirement.Kind != KindInfrastructureIdentities {
						continue
					}
					inventory, found := AssetInventoryForRequirement(inventories, requirement)
					if !found {
						t.Fatalf("missing current inventory for %s", requirement.AssetPoolName)
					}
					groups := make([]string, 0, requirement.UnitsPerSlot)
					leases := make([]Lease, 0, requirement.UnitsPerSlot)
					for index := 0; index < requirement.UnitsPerSlot; index++ {
						name := inventory.ResourceName(index)
						groups = append(groups, name)
						leases = append(leases, Lease{ResourceType: inventory.AssetPool.ResourceType, ResourceName: name})
					}
					current.Leases.Assets = map[AssetKind][]Lease{requirement.Kind: leases}
					current.Slot.Assets.InfrastructureIdentities = &ResolvedInfrastructureIdentities{
						Allocation:     AllocationLeased,
						ResourceGroups: groups,
						Identities:     []string{"fixture-managed-identity"},
					}
				}
				if err := current.ValidateForAdmission(); err != nil {
					t.Fatal(err)
				}
				if err := WriteAcquiredSlotState(shared, &current); err != nil {
					t.Fatal(err)
				}
				path, err := SlotStateFile(shared)
				if err != nil {
					t.Fatal(err)
				}
				currentJournal, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(currentJournal) != want.Journal[i] {
					t.Fatalf("slot %s current journal differs from independent legacy golden", slot.ResourceName)
				}
				contract := NewRuntimeContractBuilder()
				if err := AddCoreRuntimeExports(contract, &current, slot.Subscriptions.E2E.Name, "/fixture/profile with spaces"); err != nil {
					t.Fatal(err)
				}
				if string(contract.MarshalShell()) != want.Exports[i] {
					t.Fatalf("slot %s runtime exports drift", slot.ResourceName)
				}
			}
		})
	}
}

// This deliberately decodes only test evidence, never a production v2 reader. The v3 shape
// alone must reproduce each frozen v2 region class: no policy table exists to mask a mismatch.
func TestV3ShapeMatchesFrozenV2RegionClass(t *testing.T) {
	data, err := os.ReadFile("testdata/legacy-v2-e2e-slots.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		Environments map[string]struct {
			Pools []struct {
				Name    string   `yaml:"name"`
				Mode    string   `yaml:"region_mode"`
				Region  string   `yaml:"region"`
				Regions []string `yaml:"regions"`
				Assets  struct {
					E2E *struct {
						ProvisioningRegion string `yaml:"provisioning_region"`
					} `yaml:"e2e_identities"`
				} `yaml:"slot_assets"`
			} `yaml:"pools"`
		} `yaml:"environments"`
	}
	if err := yaml.Unmarshal(data, &frozen); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog("")
	if err != nil {
		t.Fatal(err)
	}
	for environment, config := range frozen.Environments {
		for _, old := range config.Pools {
			t.Run(environment+"/"+old.Name, func(t *testing.T) {
				var pool *Pool
				for i := range catalog.Environments[environment].Pools {
					if catalog.Environments[environment].Pools[i].Name == old.Name {
						pool = &catalog.Environments[environment].Pools[i]
					}
				}
				if pool == nil {
					t.Fatal("frozen pool missing")
				}
				switch old.Mode {
				case "fixed":
					if pool.Placement != nil || !reflect.DeepEqual(pool.Regions, []string{old.Region}) {
						t.Fatalf("fixed pool is not a closed singleton of %q: %#v %#v", old.Region, pool.Regions, pool.Placement)
					}
				case "weighted":
					if pool.Placement != nil || !reflect.DeepEqual(pool.Regions, old.Regions) {
						t.Fatalf("weighted pool lost its ordered region list: %#v", pool.Regions)
					}
				case "runtime-selected":
					if pool.Regions != nil || pool.Placement == nil || !pool.Placement.AllowUndeclared || pool.Placement.DefaultRegion != old.Region {
						t.Fatalf("runtime-selected pool is not open with default %q: %#v", old.Region, pool.Placement)
					}
				default:
					t.Fatalf("unrecognized frozen region mode %q", old.Mode)
				}
				// Provisioning never follows placement: it is the old effective location.
				want := old.Region
				if old.Assets.E2E != nil && old.Assets.E2E.ProvisioningRegion != "" {
					want = old.Assets.E2E.ProvisioningRegion
				}
				if got := pool.EffectiveIdentityProvisioningRegion(); got != want {
					t.Fatalf("provisioning region: got %q, want %q", got, want)
				}
			})
		}
	}
}
