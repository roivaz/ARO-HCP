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
	"errors"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func renderDocument(t *testing.T, report Report) *html.Node {
	t.Helper()
	var output bytes.Buffer
	if err := Render(&output, report, "html"); err != nil {
		t.Fatal(err)
	}
	document, err := html.Parse(&output)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func elements(node *html.Node, tag string) []*html.Node {
	var result []*html.Node
	var visit func(*html.Node)
	visit = func(current *html.Node) {
		if current.Type == html.ElementNode && current.Data == tag {
			result = append(result, current)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return result
}

func attribute(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func nodeText(node *html.Node) string {
	var output strings.Builder
	var visit func(*html.Node)
	visit = func(current *html.Node) {
		if current.Type == html.TextNode {
			output.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return output.String()
}

func TestHTMLShowsPopulationAndExpandableEvidence(t *testing.T) {
	bundle := testBundle()
	bundle.Observations[0].Text = "  context deadline exceeded\r\n<diagnostic>  "
	bundle.Observations[0].Fingerprint = Fingerprint(bundle.Observations[0])
	report, err := BuildReport(bundle, true)
	if err != nil {
		t.Fatal(err)
	}
	document := renderDocument(t, report)
	headings := elements(document, "th")
	want := []string{"Failure pattern", "Failed at", "Runs affected", "Impact"}
	if len(headings) != len(want) {
		t.Fatalf("got %d columns, want %d", len(headings), len(want))
	}
	for i, heading := range headings {
		if strings.TrimSpace(nodeText(heading)) != want[i] {
			t.Fatalf("unexpected table heading: %q", nodeText(heading))
		}
	}
	if attribute(headings[2], "aria-sort") != "descending" {
		t.Fatal("missing accessible default impact ordering")
	}
	pres := elements(document, "pre")
	if len(pres) != 1 || strings.ReplaceAll(nodeText(pres[0]), "\r\n", "\n") != strings.ReplaceAll(bundle.Observations[0].Text, "\r\n", "\n") {
		t.Fatal("raw example was not preserved as escaped readable text")
	}
	text := nodeText(document)
	for _, value := range []string{"50.0%", "1 of 2 total runs", "Full failure examples", "Contributing tests", "Affected runs", "Coverage gaps"} {
		if !strings.Contains(text, value) {
			t.Fatalf("report missing %q", value)
		}
	}
	for _, detail := range elements(document, "details") {
		for _, attr := range detail.Attr {
			if attr.Key == "open" {
				t.Fatal("evidence or technical details unexpectedly expanded by default")
			}
		}
	}
}

func TestHTMLDoesNotRenderEvidenceUnlessRequested(t *testing.T) {
	for _, include := range []bool{false, true} {
		t.Run(map[bool]string{false: "excluded", true: "included"}[include], func(t *testing.T) {
			bundle := testBundle()
			bundle.Observations[0].Text = "context deadline exceeded\nprivate-evidence-token"
			bundle.Observations[0].Fingerprint = Fingerprint(bundle.Observations[0])
			report, err := BuildReport(bundle, include)
			if err != nil {
				t.Fatal(err)
			}
			document := renderDocument(t, report)
			if strings.Contains(nodeText(document), "private-evidence-token") != include {
				t.Fatal("evidence flag was not respected")
			}
		})
	}
}

func TestHTMLFailureDataCannotCreateMarkup(t *testing.T) {
	report, err := BuildReport(testBundle(), true)
	if err != nil {
		t.Fatal(err)
	}
	attack := `</script><img src=x onerror="alert(1)"><svg onload="alert(2)">`
	report.Patterns[0].Canonical = attack
	report.Patterns[0].Environment = attack
	report.Environments[0].Environment = attack
	report.Patterns[0].Source = attack
	report.Patterns[0].Tests = []string{attack}
	report.Patterns[0].References[0].Evidence = attack
	report.Patterns[0].RunReferences[0].BuildID = attack
	report.Patterns[0].RunReferences[0].JobName = attack
	report.Patterns[0].RunReferences[0].ProwURL = "javascript:alert(3)"
	document := renderDocument(t, report)
	if len(elements(document, "img")) != 0 || len(elements(document, "svg")) != 0 || len(elements(document, "script")) != 1 {
		t.Fatal("failure data injected executable markup")
	}
	if !strings.Contains(nodeText(document), attack) {
		t.Fatal("safe rendering lost literal diagnostic data")
	}
	for _, row := range elements(document, "tr") {
		if attribute(row, "class") == "pattern" && attribute(row, "data-name") != attack {
			t.Fatal("pattern filter data was corrupted by attribute escaping")
		}
	}
}

func TestHTMLRunLinksAllowOnlyAbsoluteHTTP(t *testing.T) {
	cases := []struct {
		url  string
		link bool
	}{
		{"https://prow.example/view/1?x=1&y=2", true},
		{"http://prow.example/view/2", true},
		{"javascript:alert(1)", false},
		{"data:text/html,<script>alert(1)</script>", false},
		{"//prow.example/view/3", false},
		{"/view/4", false},
		{"https:///missing-host", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			report, err := BuildReport(testBundle(), false)
			if err != nil {
				t.Fatal(err)
			}
			report.Patterns[0].RunReferences[0].ProwURL = tc.url
			document := renderDocument(t, report)
			links := elements(document, "a")
			if (len(links) == 1) != tc.link {
				t.Fatalf("unsafe/missing link for %q: %d links", tc.url, len(links))
			}
			if tc.link && (attribute(links[0], "href") != tc.url || attribute(links[0], "rel") != "noopener noreferrer") {
				t.Fatal("Prow URL or safe new-tab relationship lost")
			}
		})
	}
}

func TestHTMLIsStandaloneAndJSONRetainsSharedModel(t *testing.T) {
	report, err := BuildReport(testBundle(), true)
	if err != nil {
		t.Fatal(err)
	}
	document := renderDocument(t, report)
	for _, tag := range []string{"script", "img", "iframe", "link", "source", "video", "audio", "object", "embed"} {
		for _, node := range elements(document, tag) {
			if attribute(node, "src") != "" || attribute(node, "href") != "" || attribute(node, "data") != "" {
				t.Fatalf("report requires external %s resource", tag)
			}
		}
	}
	if len(elements(document, "style")) != 1 || len(elements(document, "script")) != 1 {
		t.Fatal("offline styling or native interactions absent")
	}
	var output bytes.Buffer
	if err := Render(&output, report, "json"); err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, decoded) {
		t.Fatal("JSON rendering changed the shared report model")
	}
}

type failingReportWriter struct{}

func (failingReportWriter) Write([]byte) (int, error) {
	return 0, errors.New("writer unavailable")
}

func TestRenderPropagatesWriteErrorsAndRejectsUnknownFormats(t *testing.T) {
	for _, format := range []string{"html", "json", "xml"} {
		if err := Render(failingReportWriter{}, Report{}, format); err == nil {
			t.Fatalf("missing error for format %q", format)
		}
	}
}

func TestStageLabels(t *testing.T) {
	for source, want := range map[string]string{"e2e": "e2e", "observability": "alert", "rp-provision": "provision", "rp-upgrade": "upgrade", "": "unknown", "recorded-source": "recorded-source"} {
		if got := stageLabel(source); got != want {
			t.Errorf("stage for %q: got %q, want %q", source, got, want)
		}
	}
}
