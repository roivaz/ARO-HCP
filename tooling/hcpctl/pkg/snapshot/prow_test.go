// Copyright 2025 Microsoft Corporation
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

package snapshot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripRehearsalPrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "non-rehearsal job unchanged",
			input:    "pull-ci-Azure-ARO-HCP-main-stage-e2e-parallel",
			expected: "pull-ci-Azure-ARO-HCP-main-stage-e2e-parallel",
		},
		{
			name:     "rehearsal prefix stripped",
			input:    "rehearse-80467-pull-ci-Azure-ARO-HCP-main-stage-e2e-parallel",
			expected: "pull-ci-Azure-ARO-HCP-main-stage-e2e-parallel",
		},
		{
			name:     "periodic job unchanged",
			input:    "periodic-ci-Azure-ARO-HCP-main-periodic-stage-e2e-parallel",
			expected: "periodic-ci-Azure-ARO-HCP-main-periodic-stage-e2e-parallel",
		},
		{
			name:     "branch job unchanged",
			input:    "branch-ci-Azure-ARO-HCP-main-e2e-stage-e2e-parallel",
			expected: "branch-ci-Azure-ARO-HCP-main-e2e-stage-e2e-parallel",
		},
		{
			name:     "rehearse with no dash after number returns original",
			input:    "rehearse-12345",
			expected: "rehearse-12345",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := stripRehearsalPrefix(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestParseProwURL(t *testing.T) {
	tests := []struct {
		name             string
		url              string
		expectedJobName  string
		expectedCanonical string
		expectedProwID   string
		expectedPrefix   string
		expectErr        bool
	}{
		{
			name:              "PR presubmit URL",
			url:               "https://prow.ci.openshift.org/view/gs/test-platform-results/pr-logs/pull/Azure_ARO-HCP/9999/pull-ci-Azure-ARO-HCP-main-aro-hcp-e2e-parallel/1234567890",
			expectedJobName:   "pull-ci-Azure-ARO-HCP-main-aro-hcp-e2e-parallel",
			expectedCanonical: "pull-ci-Azure-ARO-HCP-main-aro-hcp-e2e-parallel",
			expectedProwID:    "1234567890",
			expectedPrefix:    "pr-logs/pull/Azure_ARO-HCP/9999/pull-ci-Azure-ARO-HCP-main-aro-hcp-e2e-parallel/1234567890",
		},
		{
			name:              "rehearsal URL from openshift/release PR",
			url:               "https://prow.ci.openshift.org/view/gs/test-platform-results/pr-logs/pull/openshift_release/80467/rehearse-80467-pull-ci-Azure-ARO-HCP-main-stage-e2e-parallel/2066499828505907200",
			expectedJobName:   "rehearse-80467-pull-ci-Azure-ARO-HCP-main-stage-e2e-parallel",
			expectedCanonical: "pull-ci-Azure-ARO-HCP-main-stage-e2e-parallel",
			expectedProwID:    "2066499828505907200",
			expectedPrefix:    "pr-logs/pull/openshift_release/80467/rehearse-80467-pull-ci-Azure-ARO-HCP-main-stage-e2e-parallel/2066499828505907200",
		},
		{
			name:              "periodic job URL",
			url:               "https://prow.ci.openshift.org/view/gs/test-platform-results/logs/periodic-ci-Azure-ARO-HCP-main-periodic-stage-e2e-parallel/1234567890",
			expectedJobName:   "periodic-ci-Azure-ARO-HCP-main-periodic-stage-e2e-parallel",
			expectedCanonical: "periodic-ci-Azure-ARO-HCP-main-periodic-stage-e2e-parallel",
			expectedProwID:    "1234567890",
			expectedPrefix:    "logs/periodic-ci-Azure-ARO-HCP-main-periodic-stage-e2e-parallel/1234567890",
		},
		{
			name:              "EV2 postsubmit URL",
			url:               "https://prow.ci.openshift.org/view/gs/test-platform-results/logs/branch-ci-Azure-ARO-HCP-main-e2e-stage-e2e-parallel/1234567890",
			expectedJobName:   "branch-ci-Azure-ARO-HCP-main-e2e-stage-e2e-parallel",
			expectedCanonical: "branch-ci-Azure-ARO-HCP-main-e2e-stage-e2e-parallel",
			expectedProwID:    "1234567890",
			expectedPrefix:    "logs/branch-ci-Azure-ARO-HCP-main-e2e-stage-e2e-parallel/1234567890",
		},
		{
			name:      "invalid URL missing logs segment",
			url:       "https://prow.ci.openshift.org/view/gs/test-platform-results/something-else/job/123",
			expectErr: true,
		},
		{
			name:      "invalid prow ID",
			url:       "https://prow.ci.openshift.org/view/gs/test-platform-results/logs/job-name/not-a-number",
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseProwURL(tc.url)
			if tc.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expectedJobName, info.JobName)
			assert.Equal(t, tc.expectedCanonical, info.CanonicalJobName)
			assert.Equal(t, tc.expectedProwID, info.ProwID)
			assert.Equal(t, tc.expectedPrefix, info.GCSPrefix)
		})
	}
}

func TestProwJobInfo_Kind(t *testing.T) {
	tests := []struct {
		name         string
		jobName      string
		expectedKind JobKind
	}{
		{
			name:         "DEV local e2e PR job",
			jobName:      "pull-ci-Azure-ARO-HCP-main-aro-hcp-e2e-parallel",
			expectedKind: JobKindLocalE2E,
		},
		{
			name:         "stage persistent e2e PR job",
			jobName:      "pull-ci-Azure-ARO-HCP-main-stage-e2e-parallel",
			expectedKind: JobKindPersistentE2E,
		},
		{
			name:         "integration persistent e2e PR job",
			jobName:      "pull-ci-Azure-ARO-HCP-main-integration-e2e-parallel",
			expectedKind: JobKindPersistentE2E,
		},
		{
			name:         "prod persistent e2e PR job",
			jobName:      "pull-ci-Azure-ARO-HCP-main-prod-e2e-parallel",
			expectedKind: JobKindPersistentE2E,
		},
		{
			name:         "periodic stage e2e job",
			jobName:      "periodic-ci-Azure-ARO-HCP-main-periodic-stage-e2e-parallel",
			expectedKind: JobKindPersistentE2E,
		},
		{
			name:         "EV2 gated postsubmit job",
			jobName:      "branch-ci-Azure-ARO-HCP-main-e2e-stage-e2e-parallel",
			expectedKind: JobKindEV2Gated,
		},
		{
			name:         "EV2 gated prod job",
			jobName:      "branch-ci-Azure-ARO-HCP-main-e2e-prod-e2e-parallel",
			expectedKind: JobKindEV2Gated,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := &ProwJobInfo{
				JobName:          tc.jobName,
				CanonicalJobName: stripRehearsalPrefix(tc.jobName),
			}
			assert.Equal(t, tc.expectedKind, info.Kind())
		})
	}
}

func TestProwJobInfo_IsRehearsal(t *testing.T) {
	tests := []struct {
		name       string
		jobName    string
		isRehearsal bool
	}{
		{
			name:        "regular PR job",
			jobName:     "pull-ci-Azure-ARO-HCP-main-stage-e2e-parallel",
			isRehearsal: false,
		},
		{
			name:        "rehearsal job",
			jobName:     "rehearse-80467-pull-ci-Azure-ARO-HCP-main-stage-e2e-parallel",
			isRehearsal: true,
		},
		{
			name:        "periodic job",
			jobName:     "periodic-ci-Azure-ARO-HCP-main-periodic-stage-e2e-parallel",
			isRehearsal: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := &ProwJobInfo{
				JobName:          tc.jobName,
				CanonicalJobName: stripRehearsalPrefix(tc.jobName),
			}
			assert.Equal(t, tc.isRehearsal, info.IsRehearsal())
		})
	}
}

func TestProwJobInfo_RehearsalKind(t *testing.T) {
	info := &ProwJobInfo{
		JobName:          "rehearse-80467-pull-ci-Azure-ARO-HCP-main-stage-e2e-parallel",
		CanonicalJobName: stripRehearsalPrefix("rehearse-80467-pull-ci-Azure-ARO-HCP-main-stage-e2e-parallel"),
	}
	assert.True(t, info.IsRehearsal())
	assert.Equal(t, JobKindPersistentE2E, info.Kind())
}
