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

// These identities retain an empty raw slot region independently of request policy.
// Generated from catalog revision 244323a9d3ca339755170a37008444d28d3fc637.
type poolIdentity struct {
	environment, name, subscription, resourceType string
}

var historicalEmptySlotRegions = []poolIdentity{
	{"dev", "shard0", "ARO HCP E2E Hosted Clusters (EA Subscription)", "aro-hcp-dev-shard0-slot"},
	{"dev", "shard1", "ARO HCP E2E Hosted Clusters 2 (EA Subscription)", "aro-hcp-dev-shard1-slot"},
	{"dev", "shard2", "ARO HCP E2E Hosted Clusters - Dev - 02", "aro-hcp-dev-shard2-slot"},
	{"dev", "shard3", "ARO HCP E2E Hosted Clusters - Dev - 03", "aro-hcp-dev-shard3-slot"},
	{"dev", "shard5", "ARO HCP E2E Hosted Clusters - Dev - 05 (MCA Sponsorship)", "aro-hcp-dev-shard5-slot"},
	{"dev", "shard6", "ARO HCP E2E Hosted Clusters - Dev - 06 (MCA Sponsorship)", "aro-hcp-dev-shard6-slot"},
	{"dev", "shard7", "ARO HCP E2E Hosted Clusters - Dev - 07 (MCA Sponsorship)", "aro-hcp-dev-shard7-slot"},
	{"dev", "shard8", "ARO HCP E2E Hosted Clusters - Dev - 08 (MCA Sponsorship)", "aro-hcp-dev-shard8-slot"},
	{"dev", "shard9", "ARO HCP E2E Hosted Clusters - Dev - 09 (MCA Sponsorship)", "aro-hcp-dev-shard9-slot"},
	{"dev", "shard10", "ARO HCP E2E Hosted Clusters - Dev - 10 (MCA Sponsorship)", "aro-hcp-dev-shard10-slot"},
	{"dev", "hypershift", "Hypershift Managed Azure", "aro-hcp-dev-hypershift-westus3-slot"},
	{"dev-xs", "shard4", "ARO HCP E2E Hosted Clusters - Dev - 04", "aro-hcp-dev-shard4-slot"},
}
