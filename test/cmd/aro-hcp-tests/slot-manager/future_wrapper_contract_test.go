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
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// Execute the proposed wrapper with a real subprocess boundary. The replacement
// binary records NUL-delimited argv and the alias environment; it does not
// duplicate wrapper dispatch or quoting logic.
func TestFutureAcquireWrapperContract(t *testing.T) {
	wrapper, err := filepath.Abs("slots/testdata/future-acquire-wrapper.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		env   []string
		args  []string
		alias string
		fails bool
	}{
		{name: "empty declarations remain legacy", env: []string{"POOL_SELECTOR=", "PLACEMENT_REGION=", "MULTISTAGE_PARAM_OVERRIDE_POOL_SELECTOR=", "MULTISTAGE_PARAM_OVERRIDE_PLACEMENT_REGION="}},
		{name: "selector is one literal argument", env: []string{"POOL_SELECTOR=pool.name in (shard0,shard1),slots.aro-hcp.azure.com/team!=unused"}, args: []string{"--pool-selector", "pool.name in (shard0,shard1),slots.aro-hcp.azure.com/team!=unused"}},
		{name: "no expansion or command evaluation", env: []string{"POOL_SELECTOR=pool.name=$(touch must-not-exist)*"}, args: []string{"--pool-selector", "pool.name=$(touch must-not-exist)*"}},
		{name: "invocation replaces baseline", env: []string{"POOL_SELECTOR=pool.name=shard0", "MULTISTAGE_PARAM_OVERRIDE_POOL_SELECTOR=pool.name in (shard1,shard2)", "PLACEMENT_REGION=westus3", "MULTISTAGE_PARAM_OVERRIDE_PLACEMENT_REGION=centralus"}, args: []string{"--pool-selector", "pool.name in (shard1,shard2)", "--placement-region", "centralus"}},
		{name: "alias translates and is removed", env: []string{"POOL_SELECTOR=pool.name", "MULTISTAGE_PARAM_OVERRIDE_LOCATION=westus3"}, args: []string{"--pool-selector", "pool.name", "--placement-region", "westus3"}},
		{name: "legacy alias stays inherited", env: []string{"MULTISTAGE_PARAM_OVERRIDE_LOCATION=westus3"}, alias: "westus3"},
		{name: "legacy override replaces list", env: []string{"ALLOWED_SUBSCRIPTIONS=baseline", "MULTISTAGE_PARAM_OVERRIDE_ALLOWED_SUBSCRIPTIONS=subscription one,subscription two", "ALLOWED_LOCATIONS=westus3", "CLUSTER_PROFILE_DIRS=/profile one,/profile two"}, args: []string{"--allowed-subscriptions", "subscription one,subscription two", "--allowed-locations", "westus3", "--cluster-profile-dirs", "/profile one,/profile two"}},
		{name: "mixed inherited baseline fails", env: []string{"POOL_SELECTOR=pool.name", "ALLOWED_SUBSCRIPTIONS=old baseline"}, fails: true},
		{name: "mixed inherited invocation fails", env: []string{"PLACEMENT_REGION=westus3", "MULTISTAGE_PARAM_OVERRIDE_ALLOWED_SUBSCRIPTIONS=old override"}, fails: true},
		{name: "mixed location filter fails", env: []string{"POOL_SELECTOR=pool.name", "ALLOWED_LOCATIONS=westus3"}, fails: true},
		{name: "duplicate placement fails even equal", env: []string{"PLACEMENT_REGION=westus3", "MULTISTAGE_PARAM_OVERRIDE_LOCATION=westus3"}, fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "test"), 0700); err != nil {
				t.Fatal(err)
			}
			capture := "#!/bin/bash\nprintf '%s\\0' \"$@\" > \"$CAPTURE\"\nprintf '%s' \"${MULTISTAGE_PARAM_OVERRIDE_LOCATION:-}\" > \"$ALIAS_CAPTURE\"\n"
			if err := os.WriteFile(filepath.Join(dir, "test", "aro-hcp-tests"), []byte(capture), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", wrapper)
			cmd.Dir = dir
			cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "ARO_HCP_DEPLOY_ENV=ci01", "SHARED_DIR=/shared dir", "CAPTURE=" + filepath.Join(dir, "argv"), "ALIAS_CAPTURE=" + filepath.Join(dir, "alias")}, tc.env...)
			output, err := cmd.CombinedOutput()
			if tc.fails {
				if err == nil {
					t.Fatal("conflicting controls reached binary")
				}
				if _, err := os.Stat(filepath.Join(dir, "argv")); !os.IsNotExist(err) {
					t.Fatal("binary invoked before rejection")
				}
				return
			}
			if err != nil {
				t.Fatalf("wrapper: %v: %s", err, output)
			}
			data, err := os.ReadFile(filepath.Join(dir, "argv"))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, arg := range bytes.Split(bytes.TrimSuffix(data, []byte{0}), []byte{0}) {
				got = append(got, string(arg))
			}
			want := append([]string{"slot-manager", "acquire", "--deploy-env", "ci01", "--shared-dir", "/shared dir"}, tc.args...)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("argv: got %q, want %q", got, want)
			}
			alias, err := os.ReadFile(filepath.Join(dir, "alias"))
			if err != nil {
				t.Fatal(err)
			}
			if string(alias) != tc.alias {
				t.Fatalf("alias: got %q, want %q", alias, tc.alias)
			}
			if _, err := os.Stat(filepath.Join(dir, "must-not-exist")); !os.IsNotExist(err) {
				t.Fatal("selector evaluated as shell code")
			}
		})
	}
}
