// Copyright 2026 Microsoft Corporation
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed
// under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
// CONDITIONS OF ANY KIND, either express or implied. See the License for the
// specific language governing permissions and limitations under the License.

// Package cifailures collects CI facts and builds deterministic failure reports.
// Extraction rules live in failurepatterns; this package owns provenance and IO.
package cifailures

import (
	"time"

	"github.com/Azure/ARO-HCP/tooling/hcpctl/pkg/failurepatterns"
)

const SchemaVersion = 2

// Limits apply only to presentation, never to extraction or population counts.
const (
	MaxPatterns           = 200
	MaxReferences         = 5
	MaxTests              = 10
	MaxEvidenceBytes      = 2048
	MaxDisplayBytes       = 1024
	MaxCoverageReferences = 20
	MaxRunReferences      = 20
)

type Window struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type Filters struct {
	Source    string `json:"source"`
	JobFilter string `json:"job_filter"`
}

type Metadata struct {
	Origin      string  `json:"origin"`
	Environment string  `json:"environment"`
	Window      Window  `json:"window"`
	Filters     Filters `json:"filters"`
}

// Observation preserves all available test timestamps and exact raw message.
// Repeated identical rows remain separate elements with the same fingerprint.
type Observation struct {
	Origin           string    `json:"origin"`
	Environment      string    `json:"environment"`
	BuildID          string    `json:"build_id"`
	Source           string    `json:"source"`
	TestID           string    `json:"test_id"`
	TestName         string    `json:"test_name"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at"`
	SetupFinishTime  time.Time `json:"setup_finish_time"`
	TestStartTime    time.Time `json:"test_start_time"`
	CleanupStartTime time.Time `json:"cleanup_start_time"`
	Text             string    `json:"text"`
	Fingerprint      string    `json:"fingerprint"`
	NameVariants     int64     `json:"name_variants"`
}

type Run struct {
	BuildID     string    `json:"build_id"`
	Environment string    `json:"environment,omitempty"`
	JobName     string    `json:"job_name"`
	ProwURL     string    `json:"prow_url"`
	FinishedAt  time.Time `json:"finished_at"`
	Failed      bool      `json:"failed"`
	Processed   bool      `json:"processed"`
}

type Bundle struct {
	SchemaVersion int           `json:"schema_version"`
	Metadata      Metadata      `json:"metadata"`
	Runs          []Run         `json:"runs"`
	Observations  []Observation `json:"observations"`
}

type OutcomeCounts struct {
	Matched         int `json:"matched"`
	Fallback        int `json:"fallback"`
	MissingEvidence int `json:"missing_evidence"`
}

type Reference struct {
	Fingerprint       string                     `json:"fingerprint"`
	BuildID           string                     `json:"build_id"`
	TestID            string                     `json:"test_id"`
	TestName          string                     `json:"test_name"`
	Multiplicity      int                        `json:"multiplicity"`
	Locations         []failurepatterns.Location `json:"locations,omitempty"`
	LocationsOmitted  int                        `json:"locations_omitted"`
	Evidence          string                     `json:"evidence,omitempty"`
	EvidenceTruncated bool                       `json:"evidence_truncated"`
}

type Pattern struct {
	Environment          string                   `json:"environment"`
	Source               string                   `json:"source"`
	Key                  string                   `json:"key"`
	Canonical            string                   `json:"canonical"`
	AffectedInputs       int                      `json:"affected_inputs"`
	AffectedRuns         int                      `json:"affected_runs"`
	TotalRuns            int                      `json:"total_runs"`
	ImpactPercent        float64                  `json:"impact_percent"`
	RunReferences        []Run                    `json:"run_references"`
	RunReferencesOmitted int                      `json:"run_references_omitted"`
	Findings             int                      `json:"findings"`
	SubjectFindings      int                      `json:"subject_findings"`
	Outcomes             OutcomeCounts            `json:"outcomes"`
	RuleIDs              []string                 `json:"rule_ids"`
	QualityReasons       []string                 `json:"quality_reasons"`
	Provider             string                   `json:"provider,omitempty"`
	OuterCode            string                   `json:"outer_code,omitempty"`
	DetailCode           string                   `json:"detail_code,omitempty"`
	Subject              *failurepatterns.Subject `json:"subject,omitempty"`
	ObservedNamesOmitted int                      `json:"observed_names_omitted"`
	Tests                []string                 `json:"tests"`
	TestsOmitted         int                      `json:"tests_omitted"`
	References           []Reference              `json:"references"`
	ReferencesOmitted    int                      `json:"references_omitted"`
}

type Coverage struct {
	QueryComplete                 bool           `json:"query_complete"`
	SelectedRuns                  int            `json:"selected_runs"`
	FailedRuns                    int            `json:"failed_runs"`
	ProcessedRuns                 int            `json:"processed_runs"`
	FailedRunsWithoutObservations int            `json:"failed_runs_without_observations"`
	MissingTestNames              int            `json:"missing_test_names"`
	AmbiguousTestNames            int            `json:"ambiguous_test_names"`
	UnspecifiedSourceInputs       int            `json:"unspecified_source_inputs"`
	Sources                       map[string]int `json:"sources"`
	FailedRunReferences           []Run          `json:"failed_run_references"`
	FailedRunReferencesOmitted    int            `json:"failed_run_references_omitted"`
	Limitations                   []string       `json:"limitations"`
}

// EnvironmentSummary counts the full selected population, before pattern limits.
type EnvironmentSummary struct {
	Environment                   string `json:"environment"`
	SelectedRuns                  int    `json:"selected_runs"`
	FailedRuns                    int    `json:"failed_runs"`
	AffectedRuns                  int    `json:"affected_runs"`
	FailedRunsWithoutObservations int    `json:"failed_runs_without_observations"`
}

type Report struct {
	SchemaVersion          int                  `json:"schema_version"`
	ExtractorRevision      string               `json:"extractor_revision"`
	Metadata               Metadata             `json:"metadata"`
	FailedInputs           int                  `json:"failed_inputs"`
	AffectedRuns           int                  `json:"affected_runs"`
	Findings               int                  `json:"findings"`
	SubjectFindings        int                  `json:"subject_findings"`
	Outcomes               OutcomeCounts        `json:"outcomes"`
	InputOutcomes          OutcomeCounts        `json:"input_outcomes"`
	InputBytes             int64                `json:"input_bytes"`
	Coverage               Coverage             `json:"coverage"`
	Patterns               []Pattern            `json:"patterns"`
	Environments           []EnvironmentSummary `json:"environments"`
	TotalPatterns          int                  `json:"total_patterns"`
	PatternsOmitted        int                  `json:"patterns_omitted"`
	DisplayValuesTruncated int                  `json:"display_values_truncated"`
	EvidenceIncluded       bool                 `json:"evidence_included"`
}
