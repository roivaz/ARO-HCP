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
	"slices"
	"strings"
	"testing"
)

func TestSelectPoolsScalarContract(t *testing.T) {
	t.Parallel()
	const key = customLabelPrefix + "consumer"
	catalog := &Catalog{Environments: map[string]Environment{
		"dev": {Pools: []Pool{
			{Name: "first", Labels: map[string]string{key: "ci"}},
			{Name: "empty", Labels: map[string]string{key: ""}},
			{Name: "absent"},
		}},
		"prod": {Pools: []Pool{{Name: "foreign", Labels: map[string]string{customLabelPrefix + "foreign": "yes"}}}},
	}}
	for _, tc := range []struct {
		name, expression string
		want             []string
		wantError        bool
	}{
		{"empty selects scoped order", "", []string{"first", "empty", "absent"}, false},
		{"existence", "pool.name", []string{"first", "empty", "absent"}, false},
		{"equals", "pool.name=first", []string{"first"}, false},
		{"double equals", "pool.name==first", []string{"first"}, false},
		{"set scalar or", "pool.name in (absent,first)", []string{"first", "absent"}, false},
		{"and", "pool.name in (first,empty)," + key + "=ci", []string{"first"}, false},
		{"custom existence", key, []string{"first", "empty"}, false},
		{"empty not absent", key + "=", []string{"empty"}, false},
		{"negative absent", key + "!=ci", []string{"empty", "absent"}, false},
		{"notin absent", key + " notin (ci)", []string{"empty", "absent"}, false},
		{"absence", "!" + key, []string{"absent"}, false},
		{"existence guard", key + "," + key + "!=ci", []string{"empty"}, false},
		{"unknown negative", "!" + customLabelPrefix + "missing", nil, true},
		{"foreign key", customLabelPrefix + "foreign!=yes", nil, true},
		{"bare key", "consumer=ci", nil, true},
		{"greater", "pool.name>1", nil, true},
		{"less", "pool.name<1", nil, true},
		{"expression or", "pool.name=first || pool.name=empty", nil, true},
		{"malformed", "pool.name in (", nil, true},
		{"no matches", "pool.name=foreign", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pools, err := catalog.SelectPools("dev", tc.expression)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
			if tc.wantError {
				return
			}
			var names []string
			for _, pool := range pools {
				names = append(names, pool.Name)
			}
			if !slices.Equal(names, tc.want) {
				t.Fatalf("names = %v, want %v", names, tc.want)
			}
		})
	}
	if _, err := catalog.SelectPools("missing", ""); err == nil {
		t.Fatal("accepted unknown environment")
	}
}

func TestValidatePoolLabels(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, poolName, key, value string
		valid                      bool
	}{
		{"empty value", "pool", customLabelPrefix + "consumer", "", true},
		{"case preserved", "Pool", customLabelPrefix + "Consumer", "CI", true},
		{"reserved", "pool", "pool.name", "other", false},
		{"bare", "pool", "consumer", "ci", false},
		{"other prefix", "pool", "other.example/consumer", "ci", false},
		{"empty suffix", "pool", customLabelPrefix, "ci", false},
		{"long key", "pool", customLabelPrefix + strings.Repeat("x", 64), "ci", false},
		{"long value", "pool", customLabelPrefix + "consumer", strings.Repeat("x", 64), false},
		{"invalid value", "pool", customLabelPrefix + "consumer", "not a label", false},
		{"invalid projected name", "not a name", customLabelPrefix + "consumer", "ci", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePoolLabels("dev", Pool{Name: tc.poolName, Labels: map[string]string{tc.key: tc.value}})
			if (err == nil) != tc.valid {
				t.Fatalf("validation = %v, valid %t", err, tc.valid)
			}
		})
	}
}

func TestSelectorNoMatchExplainsRequirementsPerPool(t *testing.T) {
	t.Parallel()
	catalog := &Catalog{Environments: map[string]Environment{
		"dev": {Pools: []Pool{{Name: "first"}, {Name: "second"}}},
	}}
	for _, expression := range []string{"pool.name=missing", "pool.name!=first,pool.name!=second"} {
		t.Run(expression, func(t *testing.T) {
			_, err := catalog.SelectPools("dev", expression)
			if err == nil {
				t.Fatal("expected no-match error")
			}
			for _, want := range []string{`environment "dev"`, expression, `pool "first": selector mismatch`, `pool "second": selector mismatch`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
}
