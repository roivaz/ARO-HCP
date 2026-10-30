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
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/Azure/ARO-HCP/tooling/hcpctl/pkg/failurepatterns"
	"k8s.io/apimachinery/pkg/util/sets"
)

type groupKey struct{ environment, source, key string }
type referenceState struct {
	observation  *Observation
	multiplicity int
	locations    sets.Set[failurepatterns.Location]
}
type patternState struct {
	pattern    Pattern
	runs       sets.Set[string]
	tests      sets.Set[string]
	rules      sets.Set[string]
	quality    sets.Set[string]
	names      sets.Set[string]
	references map[string]*referenceState
}

// BuildReport counts every input before applying presentation limits. Fingerprint
// duplicates retain multiplicity; an input contributes at most once per pattern.
// The caller supplies Report.ExtractorRevision from its application build metadata.
func BuildReport(bundle Bundle, includeEvidence bool) (Report, error) {
	if err := bundle.Validate(); err != nil {
		return Report{}, fmt.Errorf("build failure report: %w", err)
	}
	report := Report{
		SchemaVersion: SchemaVersion,
		Metadata:      bundle.Metadata, FailedInputs: len(bundle.Observations), EvidenceIncluded: includeEvidence,
		Patterns: []Pattern{}, Environments: []EnvironmentSummary{},
		Coverage: Coverage{QueryComplete: true, SelectedRuns: len(bundle.Runs), Sources: map[string]int{},
			FailedRunReferences: []Run{}, Limitations: []string{
				"Query completeness is not telemetry completeness: ingestion may lag and artifacts or historical sources may be unavailable.",
				"A processed marker means processing completed, not that every artifact existed; artifact-level collection state is not available in this schema.",
				"Identical observations retain multiplicity: this schema cannot distinguish ingestion duplicates from legitimate repeated test cases.",
				"Failed runs without observations are counted within the selected source filter, not as proof of absent artifacts.",
				"E2E messages contain extension Error, not full Output; extraction cannot recover evidence that was not ingested.",
				"Per-pattern affected-input counts and input outcome counts can overlap; finding counts are not failed-test counts.",
			}},
	}
	if bundle.SchemaVersion == 1 {
		report.Coverage.Limitations = append(report.Coverage.Limitations,
			"Schema 1 replay retains its original report environment label and predates per-run classification; that historical label may include jobs from other environments.")
	}
	groups := map[groupKey]*patternState{}
	runByID := make(map[string]Run, len(bundle.Runs))
	environments := map[string]*EnvironmentSummary{}
	for _, run := range bundle.Runs {
		if bundle.SchemaVersion == 1 {
			run.Environment = bundle.Metadata.Environment
		}
		runByID[run.BuildID] = run
		summary := environments[run.Environment]
		if summary == nil {
			summary = &EnvironmentSummary{Environment: run.Environment}
			environments[run.Environment] = summary
		}
		summary.SelectedRuns++
		if run.Failed {
			summary.FailedRuns++
		}
	}
	runs := sets.New[string]()
	for i := range bundle.Observations {
		observation := &bundle.Observations[i]
		runs.Insert(observation.BuildID)
		report.InputBytes += int64(len(observation.Text))
		report.Coverage.Sources[observation.Source]++
		if observation.TestName == "" {
			report.Coverage.MissingTestNames++
		}
		if observation.NameVariants > 1 {
			report.Coverage.AmbiguousTestNames++
		}
		if observation.Source == "" {
			report.Coverage.UnspecifiedSourceInputs++
		}
		findings := failurepatterns.Extract(failurepatterns.Input{Text: observation.Text, TestName: observation.TestName, Source: observation.Source})
		seenPatterns := sets.New[groupKey]()
		seenOutcomes := sets.New[string]()
		for _, finding := range findings {
			key := groupKey{observation.Environment, observation.Source, finding.Key}
			state := groups[key]
			if state == nil {
				state = &patternState{
					pattern: Pattern{Environment: key.environment, Source: key.source, Key: key.key,
						Canonical: finding.Canonical, Provider: finding.Provider, OuterCode: finding.OuterCode,
						DetailCode: finding.DetailCode, References: []Reference{}},
					runs: sets.New[string](), tests: sets.New[string](), rules: sets.New[string](),
					quality: sets.New[string](), names: sets.New[string](), references: map[string]*referenceState{},
				}
				if finding.Subject != nil {
					state.pattern.Subject = &failurepatterns.Subject{Kind: finding.Subject.Kind, Name: finding.Subject.Name}
				}
				groups[key] = state
			}
			// Stable display even if several observations explain the same key differently.
			if finding.Canonical < state.pattern.Canonical {
				state.pattern.Canonical = finding.Canonical
			}
			if finding.Provider < state.pattern.Provider {
				state.pattern.Provider = finding.Provider
			}
			if finding.OuterCode < state.pattern.OuterCode {
				state.pattern.OuterCode = finding.OuterCode
			}
			if finding.DetailCode < state.pattern.DetailCode {
				state.pattern.DetailCode = finding.DetailCode
			}
			state.pattern.Findings++
			report.Findings++
			addOutcome(&state.pattern.Outcomes, finding.Outcome)
			addOutcome(&report.Outcomes, finding.Outcome)
			seenOutcomes.Insert(finding.Outcome)
			state.runs.Insert(observation.BuildID)
			state.tests.Insert(observation.TestName)
			state.rules.Insert(finding.RuleID)
			state.quality.Insert(finding.QualityReasons...)
			if observation.TestName == "" {
				state.quality.Insert("missing_test_name")
			}
			if observation.NameVariants > 1 {
				state.quality.Insert("ambiguous_test_name")
			}
			if finding.Subject != nil {
				state.pattern.SubjectFindings++
				report.SubjectFindings++
				state.names.Insert(finding.Subject.ObservedNames...)
			}
			reference := state.references[observation.Fingerprint]
			if reference == nil {
				reference = &referenceState{observation: observation, locations: sets.New[failurepatterns.Location]()}
				state.references[observation.Fingerprint] = reference
			}
			reference.locations.Insert(finding.Locations...)
			if !seenPatterns.Has(key) {
				state.pattern.AffectedInputs++
				reference.multiplicity++
				seenPatterns.Insert(key)
			}
		}
		for outcome := range seenOutcomes {
			addOutcome(&report.InputOutcomes, outcome)
		}
	}
	report.AffectedRuns = runs.Len()
	for _, original := range bundle.Runs {
		run := runByID[original.BuildID]
		summary := environments[run.Environment]
		if runs.Has(run.BuildID) {
			summary.AffectedRuns++
		}
		if run.Processed {
			report.Coverage.ProcessedRuns++
		}
		if !run.Failed {
			continue
		}
		report.Coverage.FailedRuns++
		if runs.Has(run.BuildID) {
			continue
		}
		report.Coverage.FailedRunsWithoutObservations++
		summary.FailedRunsWithoutObservations++
		report.Coverage.FailedRunReferences = append(report.Coverage.FailedRunReferences, run)
	}
	sort.Slice(report.Coverage.FailedRunReferences, func(i, j int) bool {
		return report.Coverage.FailedRunReferences[i].BuildID < report.Coverage.FailedRunReferences[j].BuildID
	})
	if len(report.Coverage.FailedRunReferences) > MaxCoverageReferences {
		report.Coverage.FailedRunReferencesOmitted = len(report.Coverage.FailedRunReferences) - MaxCoverageReferences
		report.Coverage.FailedRunReferences = report.Coverage.FailedRunReferences[:MaxCoverageReferences]
	}
	for _, summary := range environments {
		report.Environments = append(report.Environments, *summary)
	}
	sort.Slice(report.Environments, func(i, j int) bool {
		return report.Environments[i].Environment < report.Environments[j].Environment
	})
	for _, state := range groups {
		pattern := state.pattern
		pattern.AffectedRuns = state.runs.Len()
		pattern.TotalRuns = environments[pattern.Environment].SelectedRuns
		if pattern.TotalRuns > 0 {
			pattern.ImpactPercent = 100 * float64(pattern.AffectedRuns) / float64(pattern.TotalRuns)
		}
		runIDs := sets.List(state.runs)
		pattern.RunReferences = make([]Run, 0, min(len(runIDs), MaxRunReferences))
		if len(runIDs) > MaxRunReferences {
			pattern.RunReferencesOmitted = len(runIDs) - MaxRunReferences
			runIDs = runIDs[:MaxRunReferences]
		}
		for _, buildID := range runIDs {
			pattern.RunReferences = append(pattern.RunReferences, runByID[buildID])
		}
		pattern.RuleIDs = sets.List(state.rules)
		pattern.QualityReasons = sets.List(state.quality)
		pattern.Tests, pattern.TestsOmitted = boundedStrings(sets.List(state.tests), MaxTests)
		pattern.Canonical = display(pattern.Canonical, &report)
		for i := range pattern.Tests {
			pattern.Tests[i] = display(pattern.Tests[i], &report)
		}
		if pattern.Subject != nil {
			pattern.Subject.ObservedNames, pattern.ObservedNamesOmitted = boundedStrings(sets.List(state.names), MaxReferences)
			for i := range pattern.Subject.ObservedNames {
				pattern.Subject.ObservedNames[i] = display(pattern.Subject.ObservedNames[i], &report)
			}
		}
		fingerprints := make([]string, 0, len(state.references))
		for fingerprint := range state.references {
			fingerprints = append(fingerprints, fingerprint)
		}
		sort.Strings(fingerprints)
		fingerprints, pattern.ReferencesOmitted = boundedStrings(fingerprints, MaxReferences)
		for _, fingerprint := range fingerprints {
			ref := state.references[fingerprint]
			observation := ref.observation
			reference := Reference{Fingerprint: fingerprint, BuildID: observation.BuildID, TestID: observation.TestID,
				TestName: display(observation.TestName, &report), Multiplicity: ref.multiplicity}
			for location := range ref.locations {
				reference.Locations = append(reference.Locations, location)
			}
			sort.Slice(reference.Locations, func(i, j int) bool {
				if reference.Locations[i].Start != reference.Locations[j].Start {
					return reference.Locations[i].Start < reference.Locations[j].Start
				}
				return reference.Locations[i].End < reference.Locations[j].End
			})
			if len(reference.Locations) > MaxReferences {
				reference.LocationsOmitted = len(reference.Locations) - MaxReferences
				reference.Locations = reference.Locations[:MaxReferences]
			}
			if includeEvidence {
				reference.Evidence, reference.EvidenceTruncated = clip(observation.Text, MaxEvidenceBytes)
			}
			pattern.References = append(pattern.References, reference)
		}
		report.Patterns = append(report.Patterns, pattern)
	}
	sort.Slice(report.Patterns, func(i, j int) bool {
		a, b := report.Patterns[i], report.Patterns[j]
		if a.AffectedRuns != b.AffectedRuns {
			return a.AffectedRuns > b.AffectedRuns
		}
		if a.AffectedInputs != b.AffectedInputs {
			return a.AffectedInputs > b.AffectedInputs
		}
		if a.Environment != b.Environment {
			return a.Environment < b.Environment
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Key < b.Key
	})
	report.TotalPatterns = len(report.Patterns)
	if len(report.Patterns) > MaxPatterns {
		report.PatternsOmitted = len(report.Patterns) - MaxPatterns
		report.Patterns = report.Patterns[:MaxPatterns]
	}
	return report, nil
}

func addOutcome(counts *OutcomeCounts, outcome string) {
	switch outcome {
	case "matched":
		counts.Matched++
	case "fallback":
		counts.Fallback++
	case "missing_evidence":
		counts.MissingEvidence++
	}
}

func boundedStrings(values []string, limit int) ([]string, int) {
	if len(values) <= limit {
		return values, 0
	}
	return values[:limit], len(values) - limit
}

func display(value string, report *Report) string {
	text, truncated := clip(value, MaxDisplayBytes)
	if truncated {
		report.DisplayValuesTruncated++
	}
	return text
}

func clip(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end], true
}
