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
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Azure/ARO-HCP/test/cmd/aro-hcp-tests/slot-manager/slots"
)

func TestCanonicalLegacyCallerGoldenPlans(t *testing.T) {
	var fixture struct {
		Callers []struct {
			Source    string            `json:"source"`
			Test      string            `json:"test"`
			ImageMode string            `json:"image_mode"`
			Env       map[string]string `json:"env"`
		} `json:"callers"`
	}
	var golden struct {
		Candidates map[string][]string
		Regions    map[string]map[string]string
	}
	for _, input := range []struct {
		path   string
		target any
	}{
		{"slots/testdata/legacy-callers.json", &fixture},
		{"slots/testdata/legacy-v2-golden.json", &golden},
	} {
		data, err := os.ReadFile(input.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, input.target); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := slots.LoadCatalog("")
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.Callers) != 56 || len(golden.Candidates) != 56 || len(golden.Regions) != 56 {
		t.Fatal("canonical caller evidence coverage changed")
	}
	modes := map[string]int{}
	for _, caller := range fixture.Callers {
		modes[caller.ImageMode]++
		t.Run(caller.Source+"#"+caller.Test, func(t *testing.T) {
			environment, err := catalog.ResolveEnvironmentForDeployEnv(caller.Env["ARO_HCP_DEPLOY_ENV"])
			if err != nil {
				t.Fatal(err)
			}
			subscriptions := caller.Env["MULTISTAGE_PARAM_OVERRIDE_ALLOWED_SUBSCRIPTIONS"]
			if subscriptions == "" {
				subscriptions = caller.Env["ALLOWED_SUBSCRIPTIONS"]
			}
			split := func(value string) []string {
				if value == "" {
					return nil
				}
				return strings.Split(value, ",")
			}
			request := acquireRequest{legacy: true, placement: caller.Env["MULTISTAGE_PARAM_OVERRIDE_LOCATION"]}
			plan, err := planAcquisition(catalog, environment, request, split(subscriptions), caller.Env["LOCATION_WEIGHTS"], "1234567890")
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(plan.pools))
			for _, pool := range plan.pools {
				names = append(names, pool.Name)
			}
			key := caller.Source + "#" + caller.Test
			if !reflect.DeepEqual(names, golden.Candidates[key]) {
				t.Fatalf("candidate order: got %v, want %v", names, golden.Candidates[key])
			}
			if !reflect.DeepEqual(plan.regions, golden.Regions[key]) {
				t.Fatalf("runtime regions: got %v, want %v", plan.regions, golden.Regions[key])
			}
		})
	}
	if modes["build"] != 31 || modes["import"] != 25 {
		t.Fatalf("image cohort drift: %v", modes)
	}
}
