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
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"strings"
	"time"
)

//go:embed report.html
var reportHTML string

var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"stage":     stageLabel,
	"safeURL":   safeProwURL,
	"filter":    filterLabel,
	"timestamp": func(value time.Time) string { return value.Format(time.RFC3339Nano) },
	"join":      strings.Join,
	"preview": func(value string) string {
		text, truncated := clip(value, 180)
		if truncated {
			return text + "…"
		}
		return text
	},
}).Parse(reportHTML))

type environmentSection struct {
	Summary  EnvironmentSummary
	Patterns []Pattern
}

type htmlReport struct {
	Report
	Sections []environmentSection
}

// Render uses the same report for both formats; it performs no extraction.
func Render(writer io.Writer, report Report, format string) error {
	switch format {
	case "json":
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return fmt.Errorf("write JSON report: %w", err)
		}
		return nil
	case "html":
		view := htmlReport{Report: report, Sections: make([]environmentSection, len(report.Environments))}
		indices := make(map[string]int, len(report.Environments))
		for i, summary := range report.Environments {
			view.Sections[i].Summary = summary
			indices[summary.Environment] = i
		}
		for _, pattern := range report.Patterns {
			index, exists := indices[pattern.Environment]
			if !exists {
				return fmt.Errorf("render HTML report: pattern environment %q has no summary", pattern.Environment)
			}
			view.Sections[index].Patterns = append(view.Sections[index].Patterns, pattern)
		}
		if err := reportTemplate.Execute(writer, view); err != nil {
			return fmt.Errorf("write HTML report: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported report format %q", format)
	}
}

func stageLabel(source string) string {
	switch source {
	case "e2e":
		return "e2e"
	case "observability":
		return "alert"
	case "rp-provision":
		return "provision"
	case "rp-upgrade":
		return "upgrade"
	case "":
		return "unknown"
	default:
		return source
	}
}

// Reject executable and relative URLs even when the report is opened as a file.
// html/template independently escapes the accepted URL in its attribute context.
func safeProwURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return ""
	}
	return value
}

func filterLabel(value string) string {
	if value == "" {
		return "all"
	}
	return value
}
