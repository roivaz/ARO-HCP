// Copyright 2026 Microsoft Corporation
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed
// under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
// CONDITIONS OF ANY KIND, either express or implied. See the License for the
// specific language governing permissions and limitations under the License.

package cifailures

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
)

func TestReportImpactUsesEverySelectedRunPerEnvironment(t *testing.T) {
	bundle := testBundle()
	bundle.Metadata.Environment = ""
	bundle.Runs[1].Failed = false
	for _, run := range []Run{
		{BuildID: "int-failure", Environment: "int", Failed: true},
		{BuildID: "int-missing", Environment: "int", Failed: true},
		{BuildID: "int-success", Environment: "int"},
		{BuildID: "prod-success", Environment: "prod"},
	} {
		run.JobName = "periodic-e2e"
		run.FinishedAt = bundle.Metadata.Window.From
		bundle.Runs = append(bundle.Runs, run)
	}
	observation := bundle.Observations[0]
	observation.BuildID, observation.Environment = "int-failure", "int"
	observation.Fingerprint = Fingerprint(observation)
	bundle.Observations = append(bundle.Observations, observation)
	report, err := BuildReport(bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []EnvironmentSummary{
		{Environment: "dev", SelectedRuns: 2, FailedRuns: 1, AffectedRuns: 1},
		{Environment: "int", SelectedRuns: 3, FailedRuns: 2, AffectedRuns: 1, FailedRunsWithoutObservations: 1},
		{Environment: "prod", SelectedRuns: 1},
	}
	if !reflect.DeepEqual(report.Environments, want) {
		t.Fatalf("environment populations: got %+v, want %+v", report.Environments, want)
	}
	if report.Coverage.SelectedRuns != 6 || report.AffectedRuns != 2 || report.Coverage.FailedRuns != 3 {
		t.Fatalf("wrong overall populations: %+v", report.Coverage)
	}
	for _, pattern := range report.Patterns {
		total := 2
		if pattern.Environment == "int" {
			total = 3
		}
		if pattern.TotalRuns != total || pattern.ImpactPercent != 100/float64(total) {
			t.Fatalf("impact excludes selected successes or missing observations: %+v", pattern)
		}
	}
}

func TestReportLegacyRunEnvironmentUsesRecordedMetadata(t *testing.T) {
	bundle := testBundle()
	bundle.SchemaVersion = 1
	bundle.Metadata.Environment = "recorded-legacy-environment"
	for i := range bundle.Runs {
		bundle.Runs[i].Environment = ""
		// Job names do not determine the environment of schema 1 recordings.
		bundle.Runs[i].JobName = "periodic-e2e-prod"
	}
	bundle.Observations[0].Environment = bundle.Metadata.Environment
	report, err := BuildReport(bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Environments) != 1 || report.Environments[0].Environment != bundle.Metadata.Environment || report.Environments[0].SelectedRuns != 2 {
		t.Fatalf("legacy population reclassified or lost: %+v", report.Environments)
	}
	if report.Patterns[0].TotalRuns != 2 || report.Patterns[0].ImpactPercent != 50 {
		t.Fatalf("legacy run population missing: %+v", report.Patterns[0])
	}
}

func TestRunReferencesAreDistinctAndIndependentOfInputSamples(t *testing.T) {
	bundle := testBundle()
	bundle.Runs, bundle.Observations = nil, nil
	const total = MaxRunReferences + 3
	for i := range total {
		id := fmt.Sprintf("run-%03d", i)
		run := Run{BuildID: id, Environment: "dev", JobName: "periodic-e2e", ProwURL: "https://prow.example/view/" + id,
			FinishedAt: bundle.Metadata.Window.From, Failed: true}
		bundle.Runs = append(bundle.Runs, run)
		observation := testBundle().Observations[0]
		observation.BuildID = id
		observation.Fingerprint = Fingerprint(observation)
		// Duplicate ingestion must affect inputs but never distinct runs.
		bundle.Observations = append(bundle.Observations, observation, observation)
	}
	report, err := BuildReport(bundle, true)
	if err != nil {
		t.Fatal(err)
	}
	pattern := report.Patterns[0]
	if pattern.AffectedRuns != total || pattern.AffectedInputs != total*2 || pattern.ImpactPercent != 100 {
		t.Fatalf("reference capping changed exact populations: %+v", pattern)
	}
	if len(pattern.RunReferences) != MaxRunReferences || pattern.RunReferencesOmitted != 3 {
		t.Fatalf("wrong affected-run boundary: %+v", pattern.RunReferences)
	}
	if len(pattern.References) != MaxReferences || pattern.ReferencesOmitted != total-MaxReferences {
		t.Fatalf("run list was conflated with input samples: %+v", pattern)
	}
	for i, run := range pattern.RunReferences {
		if run.BuildID != fmt.Sprintf("run-%03d", i) || run.ProwURL == "" {
			t.Fatalf("lost deterministic run identity or Prow URL: %+v", run)
		}
	}
	for i, j := 0, len(bundle.Runs)-1; i < j; i, j = i+1, j-1 {
		bundle.Runs[i], bundle.Runs[j] = bundle.Runs[j], bundle.Runs[i]
	}
	reversed, err := BuildReport(bundle, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, reversed) {
		t.Fatal("run input order changed report or references")
	}
}

func TestEmptyPopulationIsZeroSafe(t *testing.T) {
	bundle := testBundle()
	bundle.Runs, bundle.Observations = nil, nil
	report, err := BuildReport(bundle, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Coverage.SelectedRuns != 0 || report.AffectedRuns != 0 || len(report.Environments) != 0 || len(report.Patterns) != 0 {
		t.Fatalf("invented population for empty bundle: %+v", report)
	}
	for _, format := range []string{"html", "json"} {
		var output bytes.Buffer
		if err := Render(&output, report, format); err != nil {
			t.Fatalf("render empty %s report: %v", format, err)
		}
	}
}

func TestReportRankingTiesAreDeterministicAcrossEnvironments(t *testing.T) {
	bundle := testBundle()
	bundle.Metadata.Environment = ""
	bundle.Runs[1].Environment = "int"
	bundle.Observations = nil
	for _, run := range bundle.Runs {
		for _, source := range []string{"e2e", "rp-upgrade"} {
			for _, message := range []string{"alpha distinct failure", "zeta distinct failure"} {
				observation := testBundle().Observations[0]
				observation.BuildID, observation.Environment = run.BuildID, run.Environment
				observation.Source, observation.Text = source, message
				observation.Fingerprint = Fingerprint(observation)
				bundle.Observations = append(bundle.Observations, observation)
			}
		}
	}
	first, err := BuildReport(bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Patterns) != 8 {
		t.Fatalf("distinct ordinary failures merged: %+v", first.Patterns)
	}
	for i, pattern := range first.Patterns {
		environment := "dev"
		if i >= 4 {
			environment = "int"
		}
		source := "e2e"
		if i%4 >= 2 {
			source = "rp-upgrade"
		}
		if pattern.Environment != environment || pattern.Source != source {
			t.Fatalf("unstable environment/source tie at %d: %+v", i, pattern)
		}
		if i%2 == 1 && first.Patterns[i-1].Key >= pattern.Key {
			t.Fatalf("semantic key tie order is unstable: %+v", first.Patterns)
		}
	}
	for i, j := 0, len(bundle.Observations)-1; i < j; i, j = i+1, j-1 {
		bundle.Observations[i], bundle.Observations[j] = bundle.Observations[j], bundle.Observations[i]
	}
	second, err := BuildReport(bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("input ordering changed ranked patterns")
	}
}
