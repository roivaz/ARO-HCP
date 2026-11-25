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
	"testing"

	"github.com/spf13/cobra"
)

func TestAcquireRequestDispatchMatrix(t *testing.T) {
	for _, tc := range []struct {
		name                string
		env                 map[string]string
		args                []string
		legacy              bool
		selector, placement string
		fail                bool
	}{
		{name: "empty defaults", legacy: true},
		{name: "common controls", args: []string{"--build-id=x", "--location-weights=x=1", "--deploy-env=ci01"}, legacy: true},
		{name: "legacy subscription flag", args: []string{"--allowed-subscriptions=x"}, legacy: true},
		{name: "retired location flag", args: []string{"--allowed-subscriptions=x", "--allowed-locations=y"}, fail: true},
		{name: "retired location env", env: map[string]string{"ALLOWED_LOCATIONS": "westus3"}, fail: true},
		{name: "retired location env with override alias", env: map[string]string{"ALLOWED_LOCATIONS": "westus3", "MULTISTAGE_PARAM_OVERRIDE_LOCATION": "westus3"}, fail: true},
		{name: "empty location flag on legacy route", args: []string{"--allowed-locations="}, legacy: true},
		{name: "explicit empty selector", args: []string{"--pool-selector="}},
		{name: "empty placement", args: []string{"--placement-region="}, fail: true},
		{name: "selector env", env: map[string]string{"POOL_SELECTOR": "pool.name"}, selector: "pool.name"},
		{name: "placement env", env: map[string]string{"PLACEMENT_REGION": "westus3"}, placement: "westus3"},
		{name: "selector precedence", env: map[string]string{"POOL_SELECTOR": "baseline", "MULTISTAGE_PARAM_OVERRIDE_POOL_SELECTOR": "override"}, args: []string{"--pool-selector=flag"}, selector: "flag"},
		{name: "selector override", env: map[string]string{"POOL_SELECTOR": "baseline", "MULTISTAGE_PARAM_OVERRIDE_POOL_SELECTOR": "override"}, selector: "override"},
		{name: "placement precedence", env: map[string]string{"PLACEMENT_REGION": "baseline", "MULTISTAGE_PARAM_OVERRIDE_PLACEMENT_REGION": "override"}, args: []string{"--placement-region=flag"}, placement: "flag"},
		{name: "placement override", env: map[string]string{"PLACEMENT_REGION": "baseline", "MULTISTAGE_PARAM_OVERRIDE_PLACEMENT_REGION": "override"}, placement: "override"},
		{name: "empty selector overrides env", env: map[string]string{"POOL_SELECTOR": "pool.name"}, args: []string{"--pool-selector="}},
		{name: "empty old subscription flag", args: []string{"--pool-selector=", "--allowed-subscriptions="}, fail: true},
		{name: "empty old location flag", args: []string{"--pool-selector=", "--allowed-locations="}, fail: true},
		{name: "old subscription env concealed by flag", env: map[string]string{"ALLOWED_SUBSCRIPTIONS": "old"}, args: []string{"--pool-selector=", "--allowed-subscriptions="}, fail: true},
		{name: "old subscription override env", env: map[string]string{"MULTISTAGE_PARAM_OVERRIDE_ALLOWED_SUBSCRIPTIONS": "old"}, args: []string{"--pool-selector="}, fail: true},
		{name: "old location env concealed by alias", env: map[string]string{"ALLOWED_LOCATIONS": "old", "MULTISTAGE_PARAM_OVERRIDE_LOCATION": "westus3"}, args: []string{"--pool-selector="}, fail: true},
		{name: "alias new", env: map[string]string{"MULTISTAGE_PARAM_OVERRIDE_LOCATION": "westus3"}, args: []string{"--pool-selector="}, placement: "westus3"},
		{name: "alias legacy", env: map[string]string{"MULTISTAGE_PARAM_OVERRIDE_LOCATION": "westus3"}, legacy: true, placement: "westus3"},
		{name: "duplicate equal", env: map[string]string{"MULTISTAGE_PARAM_OVERRIDE_LOCATION": "westus3", "PLACEMENT_REGION": "westus3"}, fail: true},
		{name: "duplicate unequal", env: map[string]string{"MULTISTAGE_PARAM_OVERRIDE_LOCATION": "westus3"}, args: []string{"--placement-region=eastus"}, fail: true},
		{name: "plain location ignored", env: map[string]string{"LOCATION": "westus3"}, legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"POOL_SELECTOR", "MULTISTAGE_PARAM_OVERRIDE_POOL_SELECTOR", "PLACEMENT_REGION", "MULTISTAGE_PARAM_OVERRIDE_PLACEMENT_REGION", "ALLOWED_SUBSCRIPTIONS", "MULTISTAGE_PARAM_OVERRIDE_ALLOWED_SUBSCRIPTIONS", "ALLOWED_LOCATIONS", "MULTISTAGE_PARAM_OVERRIDE_LOCATION", "LOCATION"} {
				t.Setenv(key, "")
			}
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			opts := DefaultAcquireOptions()
			cmd := &cobra.Command{}
			if err := BindAcquireOptions(opts, cmd); err != nil {
				t.Fatal(err)
			}
			if err := cmd.ParseFlags(tc.args); err != nil {
				t.Fatal(err)
			}
			got, err := opts.decodeAcquireRequest()
			if tc.fail {
				if err == nil {
					t.Fatal("expected rejection")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.legacy != tc.legacy || got.selector != tc.selector || got.placement != tc.placement {
				t.Fatalf("unexpected request: %+v", got)
			}
		})
	}
}

func TestLegacySubscriptionOverrideReplacesBaseline(t *testing.T) {
	t.Setenv("ALLOWED_SUBSCRIPTIONS", "baseline,other")
	t.Setenv("MULTISTAGE_PARAM_OVERRIDE_ALLOWED_SUBSCRIPTIONS", "override")
	opts := DefaultAcquireOptions()
	if !equalStrings(opts.AllowedSubscriptions, []string{"override"}) {
		t.Fatal(opts.AllowedSubscriptions)
	}
	cmd := &cobra.Command{}
	if err := BindAcquireOptions(opts, cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.ParseFlags([]string{"--allowed-subscriptions=flag"}); err != nil {
		t.Fatal(err)
	}
	if !equalStrings(opts.AllowedSubscriptions, []string{"flag"}) {
		t.Fatal(opts.AllowedSubscriptions)
	}
}
