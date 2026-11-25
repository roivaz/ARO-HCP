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

	"gopkg.in/yaml.v3"
)

// Explicit wire schemas prevent YAML's implicit scalar-to-string conversions.
// The VM family policy has its own strict decoder; it is intentionally delegated.
type yamlFields map[string]any
type yamlEntries struct{ value any }
type yamlSequence struct{ value any }

var catalogYAMLSchema = yamlFields{
	"version": "!!int",
	"asset_pools": yamlSequence{yamlFields{
		"name": "!!str", "kind": "!!str", "provisioning": "!!str",
		"boskos_resource_type": "!!str", "resource_name_prefix": "!!str",
		"infrastructure_identities": yamlFields{"provisioning_region": "!!str"},
	}},
	"environments": yamlEntries{yamlFields{
		"deployment_environment": yamlFields{"name": "!!str", "infrastructure_subscription": "!!str"},
		"pools": yamlSequence{yamlFields{
			"name": "!!str", "subscriptions": yamlFields{"e2e": "!!str"},
			"regions":       yamlSequence{"!!str"},
			"placement":     yamlFields{"default_region": "!!str", "allow_undeclared": "!!bool"},
			"labels":        yamlEntries{"!!str"},
			"resource_type": "!!str", "slot_count": "!!int", "identity_container_provisioning_count": "!!int",
			"vm_family_policy": nil,
			"slot_assets": yamlFields{
				"mock_identities": yamlFields{"allocation": "!!str"},
				"e2e_identities": yamlFields{
					"allocation": "!!str", "provisioning": "!!str", "provisioning_region": "!!str",
					"resource_group_prefix": "!!str", "resource_group_count": "!!int",
				},
				"infrastructure_identities": yamlFields{"allocation": "!!str", "asset_pool": "!!str", "units_per_slot": "!!int"},
			},
		}},
	}},
}

func validateCatalogYAML(data []byte) error {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	if len(document.Content) != 1 {
		return fmt.Errorf("catalog must contain one YAML document")
	}
	return validateYAMLNode(document.Content[0], catalogYAMLSchema, "catalog")
}

func validateYAMLNode(node *yaml.Node, schema any, path string) error {
	switch expected := schema.(type) {
	case nil:
		return nil
	case string:
		if node.Kind != yaml.ScalarNode || node.Tag != expected {
			return fmt.Errorf("%s must have YAML type %s", path, expected)
		}
		return nil
	case yamlSequence:
		if node.Kind != yaml.SequenceNode || node.Tag != "!!seq" {
			return fmt.Errorf("%s must be a sequence", path)
		}
		for i, child := range node.Content {
			if err := validateYAMLNode(child, expected.value, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	}
	if node.Kind != yaml.MappingNode || node.Tag != "!!map" {
		return fmt.Errorf("%s must be a mapping", path)
	}
	seen := make(map[string]bool, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return fmt.Errorf("%s keys must be strings", path)
		}
		if seen[key.Value] {
			return fmt.Errorf("%s has duplicate key %q", path, key.Value)
		}
		seen[key.Value] = true
		var childSchema any
		switch expected := schema.(type) {
		case yamlFields:
			var found bool
			childSchema, found = expected[key.Value]
			if !found {
				return fmt.Errorf("%s: field %s not found", path, key.Value)
			}
		case yamlEntries:
			childSchema = expected.value
		}
		if err := validateYAMLNode(node.Content[i+1], childSchema, path+"."+key.Value); err != nil {
			return err
		}
	}
	return nil
}
