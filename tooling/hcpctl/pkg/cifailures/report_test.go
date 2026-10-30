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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func testBundle() Bundle {
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	metadata := Metadata{Origin: "https://example.kusto.windows.net/ServiceLogs", Environment: "dev",
		Window: Window{From: from, To: from.Add(24 * time.Hour)}, Filters: Filters{JobFilter: "e2e"}}
	observation := Observation{Origin: metadata.Origin, Environment: metadata.Environment,
		BuildID: "run-1", Source: "e2e", TestID: "test-1", TestName: "create cluster", Text: "context deadline exceeded"}
	observation.Fingerprint = Fingerprint(observation)
	return Bundle{SchemaVersion: SchemaVersion, Metadata: metadata,
		Runs: []Run{{BuildID: "run-1", Environment: "dev", JobName: "periodic-e2e", FinishedAt: from, Failed: true, Processed: true},
			{BuildID: "run-2", Environment: "dev", JobName: "periodic-e2e", FinishedAt: from.Add(time.Hour), Failed: true}},
		Observations: []Observation{observation}}
}

func TestFingerprintExactVersionedInputs(t *testing.T) {
	original := testBundle().Observations[0]
	changes := []struct {
		name   string
		change func(*Observation)
	}{
		{"origin", func(o *Observation) { o.Origin += "/other" }},
		{"build", func(o *Observation) { o.BuildID += "x" }},
		{"source", func(o *Observation) { o.Source = "rp-upgrade" }},
		{"test ID", func(o *Observation) { o.TestID += "x" }},
		{"raw newline", func(o *Observation) { o.Text += "\n" }},
		{"raw whitespace", func(o *Observation) { o.Text = " " + o.Text }},
		{"started", func(o *Observation) { o.StartedAt = time.Unix(1, 0) }},
		{"finished", func(o *Observation) { o.FinishedAt = time.Unix(1, 0) }},
		{"setup", func(o *Observation) { o.SetupFinishTime = time.Unix(1, 0) }},
		{"test start", func(o *Observation) { o.TestStartTime = time.Unix(1, 0) }},
		{"cleanup", func(o *Observation) { o.CleanupStartTime = time.Unix(1, 0) }},
	}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			changed := original
			tc.change(&changed)
			if Fingerprint(changed) == original.Fingerprint {
				t.Fatal("identity ignored changed raw input")
			}
		})
	}
	left, right := original, original
	left.BuildID, left.Source = "a\x00b", "c"
	right.BuildID, right.Source = "a", "b\x00c"
	if Fingerprint(left) == Fingerprint(right) {
		t.Fatal("ambiguous delimiter encoding")
	}
	changed := original
	changed.Environment = "other"
	changed.Fingerprint = "ignored"
	changed.NameVariants = 2
	changed.TestName = "late-arriving corrected test name"
	if Fingerprint(changed) != original.Fingerprint {
		t.Fatal("report-only context changed identity")
	}
	left.StartedAt = time.Date(2026, 10, 28, 1, 0, 0, 0, time.FixedZone("one", 3600))
	right = left
	right.StartedAt = left.StartedAt.UTC()
	if Fingerprint(left) != Fingerprint(right) {
		t.Fatal("equivalent instants differ")
	}
	if !strings.HasPrefix(original.Fingerprint, "sha256:v1:") {
		t.Fatal("missing fingerprint version")
	}
}

func TestReportRetainsMultiplicityAndCoverage(t *testing.T) {
	bundle := testBundle()
	bundle.Observations = append(bundle.Observations, bundle.Observations[0])
	report, err := BuildReport(bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.FailedInputs != 2 || report.AffectedRuns != 1 || report.Findings != 2 {
		t.Fatalf("wrong population: %+v", report)
	}
	if len(report.Patterns) != 1 || report.Patterns[0].AffectedInputs != 2 || report.Patterns[0].AffectedRuns != 1 {
		t.Fatalf("wrong grouping: %+v", report.Patterns)
	}
	refs := report.Patterns[0].References
	if len(refs) != 1 || refs[0].Multiplicity != 2 || refs[0].Evidence != "" {
		t.Fatalf("wrong compact references: %+v", refs)
	}
	if report.Coverage.FailedRunsWithoutObservations != 1 || report.Coverage.ProcessedRuns != 1 || report.Coverage.FailedRunReferences[0].BuildID != "run-2" {
		t.Fatalf("wrong coverage: %+v", report.Coverage)
	}
}

func TestReportPreservesSourcesAndMissingNames(t *testing.T) {
	bundle := testBundle()
	bundle.Observations = nil
	for _, source := range []string{"", "e2e", "rp-provision", "rp-upgrade"} {
		o := testBundle().Observations[0]
		o.Source, o.TestName, o.Text = source, "", ""
		o.Fingerprint = Fingerprint(o)
		bundle.Observations = append(bundle.Observations, o)
	}
	report, err := BuildReport(bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalPatterns != 4 || report.Outcomes.MissingEvidence != 4 || report.InputOutcomes.MissingEvidence != 4 {
		t.Fatalf("lost source/outcome distinctions: %+v", report)
	}
	if report.Coverage.UnspecifiedSourceInputs != 1 || report.Coverage.MissingTestNames != 4 {
		t.Fatalf("wrong missing context coverage: %+v", report.Coverage)
	}
	if report.Patterns[0].Source != "" {
		t.Fatal("historical source was guessed or sort unstable")
	}
}

func TestReportBoundsAfterCountingAndIsOrderIndependent(t *testing.T) {
	bundle := testBundle()
	bundle.Observations = nil
	for i := range MaxReferences + 4 {
		o := testBundle().Observations[0]
		o.TestID = fmt.Sprint(i)
		bundle.Observations = append(bundle.Observations, o)
		bundle.Observations[i].Fingerprint = Fingerprint(o)
	}
	first, err := BuildReport(bundle, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.FailedInputs != MaxReferences+4 || first.Patterns[0].ReferencesOmitted != 4 || len(first.Patterns[0].References) != MaxReferences {
		t.Fatalf("sample limit altered population: %+v", first)
	}
	for i, j := 0, len(bundle.Observations)-1; i < j; i, j = i+1, j-1 {
		bundle.Observations[i], bundle.Observations[j] = bundle.Observations[j], bundle.Observations[i]
	}
	second, err := BuildReport(bundle, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("input order changed report")
	}
	text, truncated := clip(strings.Repeat("界", MaxEvidenceBytes), MaxEvidenceBytes)
	if !truncated || len(text) > MaxEvidenceBytes || !utf8.ValidString(text) {
		t.Fatal("evidence clipping split UTF-8")
	}
}

func TestReplayRetainsMetadataAndRawEvidence(t *testing.T) {
	bundle := testBundle()
	bundle.Observations[0].Text = "  diagnostic\r\n\t<secret>  "
	bundle.Observations[0].Fingerprint = Fingerprint(bundle.Observations[0])
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(bundle); err != nil {
		t.Fatal(err)
	}
	replayed, err := ReadBundle(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bundle, replayed) {
		t.Fatal("replay changed raw inputs or saved filters/window")
	}
	before, err := BuildReport(bundle, true)
	if err != nil {
		t.Fatal(err)
	}
	after, err := BuildReport(replayed, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("live/replay report diverged")
	}
}

func TestReplayRejectsInvalidInputs(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Bundle)
	}{
		{"schema", func(b *Bundle) { b.SchemaVersion++ }},
		{"fingerprint", func(b *Bundle) { b.Observations[0].Text += "changed" }},
		{"missing fingerprint", func(b *Bundle) { b.Observations[0].Fingerprint = "" }},
		{"duplicate run", func(b *Bundle) { b.Runs = append(b.Runs, b.Runs[0]) }},
		{"unknown run", func(b *Bundle) { b.Observations[0].BuildID = "unknown" }},
		{"exclusive end", func(b *Bundle) { b.Runs[0].FinishedAt = b.Metadata.Window.To }},
		{"before start", func(b *Bundle) { b.Runs[0].FinishedAt = b.Metadata.Window.From.Add(-time.Nanosecond) }},
		{"filter", func(b *Bundle) { b.Metadata.Filters.Source = "rp-upgrade" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle := testBundle()
			tc.mutate(&bundle)
			data, err := json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ReadBundle(bytes.NewReader(data)); err == nil {
				t.Fatal("accepted invalid replay")
			}
		})
	}
	data, err := json.Marshal(testBundle())
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"{}", "garbage"} {
		if _, err := ReadBundle(strings.NewReader(string(data) + suffix)); err == nil {
			t.Fatal("accepted trailing input")
		}
	}
}

func TestSaveBundleAtomicPrivateAndNoClobber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.json")
	bundle := testBundle()
	if err := SaveBundle(path, bundle); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe permissions %v", info.Mode())
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveBundle(path, bundle); err == nil {
		t.Fatal("overwrote existing raw evidence")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, current) {
		t.Fatal("existing file was corrupted")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatal("temporary file leaked")
	}
	if err := SaveBundle("-", bundle); err == nil {
		t.Fatal("allowed raw data on stdout")
	}
}

func TestAlertInputsCountSubjectsWithoutInflatingFailures(t *testing.T) {
	bundle := testBundle()
	o := &bundle.Observations[0]
	o.Source = "observability"
	o.TestName = "[aro-hcp-observability] [hcp] alert KubePodNotReady does not fire"
	o.Text = "alert fired 3 time(s)\nFiring 1:\n  Labels: alertname=\"KubePodNotReady\", pod=\"etcd-0\"\nFiring 2:\n  Labels: alertname=\"KubePodNotReady\", pod=\"router-767bb4bbcd-xqtnw\"\nFiring 3:\n  Labels: alertname=\"KubePodNotReady\", pod=\"etcd-1\"\n"
	o.Fingerprint = Fingerprint(*o)
	report, err := BuildReport(bundle, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.FailedInputs != 1 || report.AffectedRuns != 1 || report.TotalPatterns != 2 {
		t.Fatalf("inflated input/run counts: %+v", report)
	}
	if report.SubjectFindings != 2 {
		t.Fatalf("lost or inflated distinct subjects: %+v", report)
	}
	for _, pattern := range report.Patterns {
		if pattern.AffectedInputs != 1 || pattern.AffectedRuns != 1 {
			t.Fatalf("repeated subject inflated failed inputs: %+v", pattern)
		}
		if pattern.Subject == nil || (pattern.Subject.Name != "etcd" && pattern.Subject.Name != "router") {
			t.Fatalf("lost subject: %+v", pattern)
		}
		if pattern.References[0].Fingerprint != o.Fingerprint {
			t.Fatal("finding detached from exact parent")
		}
		for _, span := range pattern.References[0].Locations {
			if span.Start < 0 || span.End > len(o.Text) || span.Start >= span.End || !strings.HasPrefix(o.Text[span.Start:span.End], "Firing") {
				t.Fatalf("invalid raw evidence span: %+v", span)
			}
		}
	}
}

func TestPatternLimitDoesNotLimitExtraction(t *testing.T) {
	bundle := testBundle()
	bundle.Observations = nil
	for i := range MaxPatterns + 3 {
		o := testBundle().Observations[0]
		// Distinct sources must not merge even when semantic evidence is identical.
		o.Source = fmt.Sprintf("source-%03d", i)
		o.Fingerprint = Fingerprint(o)
		bundle.Observations = append(bundle.Observations, o)
	}
	report, err := BuildReport(bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.FailedInputs != MaxPatterns+3 || report.TotalPatterns != MaxPatterns+3 || report.PatternsOmitted != 3 || len(report.Patterns) != MaxPatterns {
		t.Fatalf("presentation cap affected extraction: %+v", report)
	}
}

func TestFingerprintSurvivesLateNameEnrichment(t *testing.T) {
	observation := testBundle().Observations[0]
	observation.TestName = ""
	before := Fingerprint(observation)
	observation.TestName = "newly ingested test name"
	if Fingerprint(observation) != before {
		t.Fatal("name enrichment changed immutable observation identity")
	}
}

func TestPatternLimitRetainsHighestImpact(t *testing.T) {
	bundle := testBundle()
	bundle.Observations = nil
	for i := range MaxPatterns + 1 {
		observation := testBundle().Observations[0]
		observation.Source = fmt.Sprintf("source-%03d", i)
		observation.Fingerprint = Fingerprint(observation)
		bundle.Observations = append(bundle.Observations, observation)
	}
	// A pattern affecting two runs outranks arbitrarily many inputs on one run.
	highImpact := testBundle().Observations[0]
	highImpact.Source = "zz-high-impact"
	highImpact.Fingerprint = Fingerprint(highImpact)
	bundle.Observations = append(bundle.Observations, highImpact)
	highImpact.BuildID = "run-2"
	highImpact.Fingerprint = Fingerprint(highImpact)
	bundle.Observations = append(bundle.Observations, highImpact)
	duplicated := bundle.Observations[0]
	for range 10 {
		bundle.Observations = append(bundle.Observations, duplicated)
	}
	report, err := BuildReport(bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Patterns) != MaxPatterns || report.Patterns[0].Source != highImpact.Source || report.Patterns[0].AffectedRuns != 2 || report.Patterns[0].AffectedInputs != 2 {
		t.Fatalf("presentation cap omitted high-impact pattern: %+v", report.Patterns)
	}
	if report.Patterns[1].Source != "source-000" || report.PatternsOmitted != 2 {
		t.Fatalf("unstable tie breakers or wrong omitted count: %+v", report)
	}
}
