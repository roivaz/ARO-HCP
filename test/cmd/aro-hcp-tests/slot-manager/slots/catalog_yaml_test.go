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
	"strings"
	"testing"
)

func TestCatalogStrictWireTypes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, from, to string }{
		{"v2 unsupported", "version: 3", "version: 2"},
		{"version string", "version: 3", `version: "3"`},
		{"numeric name", "name: shard0", "name: 123"},
		{"null name", "name: shard0", "name: null"},
		{"numeric subscription", "e2e: dev-e2e", "e2e: 123"},
		{"string count", "slot_count: 2", `slot_count: "2"`},
		{"fraction count", "slot_count: 2", "slot_count: 2.5"},
		{"null count", "slot_count: 2", "slot_count: null"},
		{"scalar regions", "regions: [westus3, centralus, canadacentral]", "regions: westus3"},
		{"number region", "regions: [westus3, centralus, canadacentral]", "regions: [123]"},
		{"null regions", "regions: [westus3, centralus, canadacentral]", "regions: null"},
		{"obsolete mode", "regions:", "region_mode: weighted\n      regions:"},
		{"unknown placement", "regions: [westus3, centralus, canadacentral]", "placement: {default_region: uksouth, allow_undeclared: true, unknown: true}"},
		{"string boolean", "regions: [westus3, centralus, canadacentral]", `placement: {default_region: uksouth, allow_undeclared: "true"}`},
		{"numeric default", "regions: [westus3, centralus, canadacentral]", "placement: {default_region: 123, allow_undeclared: true}"},
		{"numeric provisioning", "provisioning_region: westus3", "provisioning_region: 123"},
		{"numeric allocation", "allocation: dedicated", "allocation: 123"},
		{"duplicate field", "slot_count: 2", "slot_count: 2\n      slot_count: 3"},
		{"multiple documents", "version: 3", "---\n{}\n---\nversion: 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadCatalogFromYAMLWithError(t, strings.Replace(dedicatedCatalog, tc.from, tc.to, 1))
			if err == nil {
				t.Fatal("accepted malformed catalog")
			}
		})
	}
	for _, labels := range []string{
		"null", "[]", "plain", "{123: value}", "{true: value}",
		"{slots.aro-hcp.azure.com/key: 123}", "{slots.aro-hcp.azure.com/key: true}",
		"{slots.aro-hcp.azure.com/key: null}", "{slots.aro-hcp.azure.com/key: []}",
		"{slots.aro-hcp.azure.com/key: {}}", "{slots.aro-hcp.azure.com/key: a, slots.aro-hcp.azure.com/key: b}",
	} {
		t.Run("labels="+labels, func(t *testing.T) {
			input := strings.Replace(dedicatedCatalog, "      slot_count: 2", "      labels: "+labels+"\n      slot_count: 2", 1)
			if _, err := loadCatalogFromYAMLWithError(t, input); err == nil {
				t.Fatal("accepted malformed labels")
			}
		})
	}
	for _, value := range []string{`""`, `"true"`, `"123"`} {
		input := strings.Replace(dedicatedCatalog, "      slot_count: 2", "      labels: {slots.aro-hcp.azure.com/key: "+value+"}\n      slot_count: 2", 1)
		loadCatalogFromYAML(t, input)
	}
}
