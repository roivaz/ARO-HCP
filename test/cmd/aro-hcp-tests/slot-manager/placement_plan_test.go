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
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Azure/ARO-HCP/test/cmd/aro-hcp-tests/slot-manager/slots"
)

func TestNewPlacementPlans(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		policies                  []slots.Pool
		placement, weights, build string
		want                      map[string]string
		failure                   string
	}{
		{name: "singleton without common inputs", policies: []slots.Pool{{Name: "a", Regions: []string{"westus3"}}}, want: map[string]string{"a": "westus3"}},
		{name: "different singletons", policies: []slots.Pool{{Name: "a", Regions: []string{"westus3"}}, {Name: "b", Regions: []string{"eastus"}}}, want: map[string]string{"a": "westus3", "b": "eastus"}},
		{name: "open default", policies: []slots.Pool{{Name: "a", Placement: &slots.PlacementPolicy{DefaultRegion: "uksouth", AllowUndeclared: true}}}, want: map[string]string{"a": "uksouth"}},
		{name: "open EUAP", policies: []slots.Pool{{Name: "a", Placement: &slots.PlacementPolicy{DefaultRegion: "uksouth", AllowUndeclared: true}}}, placement: "eastus2euap", want: map[string]string{"a": "eastus2euap"}},
		{name: "singleton weights inapplicable", policies: []slots.Pool{{Name: "a", Regions: []string{"westus3"}}}, weights: "westus3=1", failure: "inapplicable"},
		{name: "open weights inapplicable", policies: []slots.Pool{{Name: "a", Placement: &slots.PlacementPolicy{DefaultRegion: "uksouth", AllowUndeclared: true}}}, weights: "uksouth=1", failure: "inapplicable"},
		{name: "hard placement bypasses invalid weights", policies: []slots.Pool{{Name: "a", Regions: []string{"westus3"}}}, placement: "westus3", weights: "invalid", want: map[string]string{"a": "westus3"}},
		{name: "hard placement excludes pool", policies: []slots.Pool{{Name: "a", Regions: []string{"westus3"}}, {Name: "b", Regions: []string{"eastus"}}}, placement: "eastus", want: map[string]string{"b": "eastus"}},
		{name: "hard placement no match", policies: []slots.Pool{{Name: "a", Regions: []string{"westus3"}}}, placement: "eastus", failure: "no pool found"},
		{name: "common weighted draw", policies: []slots.Pool{{Name: "a", Regions: []string{"westus3", "centralus", "canadacentral"}}, {Name: "b", Regions: []string{"westus3", "centralus", "canadacentral"}}}, weights: "westus3=0,centralus=1,canadacentral=0", build: "any", want: map[string]string{"a": "centralus", "b": "centralus"}},
		{name: "heterogeneous weighted", policies: []slots.Pool{{Name: "a", Regions: []string{"westus3", "eastus"}}, {Name: "b", Regions: []string{"eastus", "westus3"}}}, failure: "different ordered"},
		{name: "mixed automatic policies", policies: []slots.Pool{{Name: "a", Regions: []string{"westus3"}}, {Name: "b", Regions: []string{"eastus", "westus3"}}}, failure: "incompatible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := &slots.Catalog{Environments: map[string]slots.Environment{"dev": {Pools: tc.policies}}}
			plan, err := planAcquisition(catalog, "dev", acquireRequest{placement: tc.placement}, nil, tc.weights, tc.build)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("got %v; want %s", err, tc.failure)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.pools) != len(tc.want) {
				t.Fatalf("candidates: %v", plan.pools)
			}
			for name, region := range tc.want {
				if plan.regions[name] != region {
					t.Fatalf("regions: %v", plan.regions)
				}
			}
			for i := range tc.policies {
				if len(tc.policies[i].Regions) > 0 {
					tc.policies[i].Regions[0] = "changed"
				}
			}
			opts := &AcquireOptions{completedAcquireOptions: &completedAcquireOptions{PlacementPlan: plan}}
			for _, pool := range rotatedCandidatePools(plan.pools, time.Unix(0, 1)) {
				if opts.runtimeRegionForPool(pool) != tc.want[pool.Name] {
					t.Fatal("plan changed after catalog mutation/rotation")
				}
			}
		})
	}
}

func TestPlanningErrorsPrecedeLoggerAndLease(t *testing.T) {
	for _, tc := range []struct{ name, selector, placement, weights string }{
		{name: "unknown selector", selector: "unknown=value"},
		{name: "no match", selector: "pool.name=absent"},
		{name: "incompatible placement", selector: "pool.name", placement: "eastus"},
		{name: "inapplicable weights", selector: "pool.name", weights: "westus3=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loggerCalls := 0
			opts := lifecycleOptions(t, lifecycleCatalog, "http://must-not-be-contacted.invalid", nil)
			opts.PoolSelector = tc.selector
			opts.PlacementRegion = tc.placement
			opts.LocationWeights = tc.weights
			opts.SetupAzureGlobalLogger = func(string) (io.Closer, error) { loggerCalls++; return noopCloser{}, nil }
			validated, err := opts.Validate()
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := slots.LoadCatalog(opts.CatalogPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = validated.completeCatalog(catalog); err == nil {
				t.Fatal("expected planning failure")
			}
			if loggerCalls != 0 {
				t.Fatal("planning failure created logger")
			}
		})
	}
}

func TestSingletonPlacementNeedsNoWeightsOnEitherRoute(t *testing.T) {
	catalog, err := slots.LoadCatalog("")
	if err != nil {
		t.Fatal(err)
	}
	env := catalog.Environments["dev"]
	for i := range env.Pools {
		env.Pools[i].Regions = []string{"westus3"}
	}
	catalog.Environments["dev"] = env
	for _, legacy := range []bool{true, false} {
		for _, tc := range []struct{ weights, build, want string }{
			{"", "", ""}, {"", "id", ""}, {"westus3=1", "id", "inapplicable"},
		} {
			request := acquireRequest{legacy: legacy, selector: "pool.name"}
			plan, err := planAcquisition(catalog, "dev", request, nil, tc.weights, tc.build)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("legacy=%v weights=%q: got %v want %s", legacy, tc.weights, err, tc.want)
				}
				continue
			}
			if err != nil {
				t.Fatalf("legacy=%v: %v", legacy, err)
			}
			for _, region := range plan.regions {
				if region != "westus3" {
					t.Fatal(region)
				}
			}
		}
	}
}

func TestLegacyPlacementMatrix(t *testing.T) {
	catalog, err := slots.LoadCatalog("")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, environment, override, weights, build string
		wantCount                                   int
		wantRegion, failure                         string
	}{
		{name: "closed singletons resolve directly", environment: "int", wantCount: 2},
		{name: "override filters closed singletons", environment: "int", override: "westus3", wantCount: 1, wantRegion: "westus3"},
		{name: "override outside every singleton", environment: "int", override: "eastus2", failure: "no pool found"},
		{name: "singleton rejects inapplicable weights", environment: "int", weights: "invalid", failure: "inapplicable"},
		{name: "STG closed singletons", environment: "stg", wantCount: 2, wantRegion: "westus3"},
		{name: "open default", environment: "prod", wantCount: 7, wantRegion: "uksouth"},
		{name: "open concrete EUAP", environment: "prod", override: "eastus2euap", wantCount: 7, wantRegion: "eastus2euap"},
		{name: "open rejects inapplicable weights", environment: "prod", weights: "invalid", failure: "inapplicable"},
		{name: "weighted draw", environment: "dev", weights: "westus3=1,centralus=0,canadacentral=0", build: "id", wantCount: 11, wantRegion: "westus3"},
		{name: "weighted override bypass", environment: "dev", override: "westus3", weights: "invalid", wantCount: 11, wantRegion: "westus3"},
		{name: "weighted override outside list", environment: "dev", override: "eastus2", failure: "no pool found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := planAcquisition(catalog, tc.environment, acquireRequest{legacy: true, placement: tc.override}, nil, tc.weights, tc.build)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("got %v want %s", err, tc.failure)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.pools) != tc.wantCount {
				t.Fatalf("candidate count %d, want %d", len(plan.pools), tc.wantCount)
			}
			if tc.wantRegion != "" {
				for _, region := range plan.regions {
					if region != tc.wantRegion {
						t.Fatal(plan.regions)
					}
				}
			}
		})
	}
}

func TestZeroCandidateDiagnostics(t *testing.T) {
	catalog, err := slots.LoadCatalog("")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                string
		request             acquireRequest
		subscriptions, want []string
	}{
		{name: "selector", request: acquireRequest{selector: "pool.name=absent"}, want: []string{"pool.name=absent", "shard0", "westus3-shard0"}},
		{name: "hard placement", request: acquireRequest{selector: "pool.name", placement: "eastus2"}, want: []string{"pool.name", "eastus2", "shard0", "hard placement"}},
		{name: "legacy subscription", request: acquireRequest{legacy: true}, subscriptions: []string{"unmatched-subscription"}, want: []string{"unmatched-subscription", "shard0", "subscription"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := planAcquisition(catalog, "int", tc.request, tc.subscriptions, "", "")
			if err == nil {
				t.Fatal("expected empty candidate rejection")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("diagnostic missing %q: %v", want, err)
				}
			}
		})
	}
}
