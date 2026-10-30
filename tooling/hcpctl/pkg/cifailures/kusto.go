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
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Azure/ARO-HCP/tooling/hcpctl/pkg/kusto"
	"github.com/Azure/azure-kusto-go/azkustodata"
	"github.com/Azure/azure-kusto-go/azkustodata/kql"
	"github.com/Azure/azure-kusto-go/azkustodata/query"
)

// QueryClient uses the SDK's fully materialized result API. Query consumes the
// entire response and returns late table/dataset failures, unlike a consumer
// that reads only the first primary table from IterativeQuery.
type QueryClient interface {
	Query(context.Context, string, azkustodata.Statement, ...azkustodata.QueryOption) (query.Dataset, error)
}

const schemaQuery = `union
(ciTestResults | getschema | extend TableName="ciTestResults"),
(ciTestNames | getschema | extend TableName="ciTestNames"),
(ciJobOutcomes | getschema | extend TableName="ciJobOutcomes"),
(ciProcessedJobs | getschema | extend TableName="ciProcessedJobs")
| project TableName, ColumnName, ColumnType`

// Parameter declarations are supplied by the SDK QueryParameters option.
// The leftmost failed-test population is never summarized or deduplicated.
const populationQuery = `let runs = materialize(
    ciJobOutcomes
    | summarize arg_max(finishedAt, jobName, prowUrl, failed) by buildId
    | where finishedAt >= windowFrom and finishedAt < windowTo
    | where isempty(jobFilter) or jobName contains_cs jobFilter
    | join kind=leftouter (ciProcessedJobs | summarize processedCount=count() by buildId) on buildId
    | project buildId, jobName, prowUrl, runFinishedAt=finishedAt, runFailed=failed, processed=coalesce(processedCount, 0) > 0
);
let names = ciTestNames
    | summarize name=take_any(name), nameVariants=count_distinct(name) by testId
    | extend name=iff(nameVariants == 1, name, "");
let observations = ciTestResults
    | where failed
    | where isempty(sourceFilter) or source == sourceFilter
    | join kind=inner runs on buildId
    | join kind=leftouter names on testId
    | project rowKind="observation", buildId, jobName, prowUrl, runFinishedAt, runFailed, processed,
        source, testId, testName=coalesce(name, ""), nameVariants=coalesce(nameVariants, 0),
        startedAt, finishedAt, setupFinishTime, testStartTime, cleanupStartTime, message;
union observations, (runs | extend rowKind="run")
| project rowKind, buildId, jobName, prowUrl, runFinishedAt, runFailed, processed,
    source, testId, testName, nameVariants, startedAt, finishedAt, setupFinishTime, testStartTime, cleanupStartTime, message`

var requiredSchema = map[string]map[string]string{
	"ciTestResults": {
		"buildId": "string", "testId": "string", "source": "string", "failed": "bool", "message": "string",
		"startedAt": "datetime", "finishedAt": "datetime", "setupFinishTime": "datetime",
		"testStartTime": "datetime", "cleanupStartTime": "datetime",
	},
	"ciTestNames":     {"testId": "string", "name": "string"},
	"ciJobOutcomes":   {"buildId": "string", "jobName": "string", "prowUrl": "string", "finishedAt": "datetime", "failed": "bool"},
	"ciProcessedJobs": {"buildId": "string"},
}

type schemaColumn struct {
	TableName  string
	ColumnName string
	ColumnType string
}

type populationRow struct {
	Kind             string    `kusto:"rowKind"`
	BuildID          string    `kusto:"buildId"`
	JobName          string    `kusto:"jobName"`
	ProwURL          string    `kusto:"prowUrl"`
	RunFinishedAt    time.Time `kusto:"runFinishedAt"`
	RunFailed        bool      `kusto:"runFailed"`
	Processed        bool      `kusto:"processed"`
	Source           string    `kusto:"source"`
	TestID           string    `kusto:"testId"`
	TestName         string    `kusto:"testName"`
	NameVariants     int64     `kusto:"nameVariants"`
	StartedAt        time.Time `kusto:"startedAt"`
	FinishedAt       time.Time `kusto:"finishedAt"`
	SetupFinishTime  time.Time `kusto:"setupFinishTime"`
	TestStartTime    time.Time `kusto:"testStartTime"`
	CleanupStartTime time.Time `kusto:"cleanupStartTime"`
	Message          string    `kusto:"message"`
}

// Collect bounds the entire collection (schema and population) by timeout. No
// report or bundle is returned if a query is partial, truncated, or undecodable.
func Collect(ctx context.Context, client QueryClient, database string, metadata Metadata, timeout time.Duration) (Bundle, error) {
	if err := metadata.Validate(); err != nil {
		return Bundle{}, err
	}
	if database == "" {
		return Bundle{}, fmt.Errorf("Kusto database is required")
	}
	if timeout <= 0 || timeout > 30*time.Minute {
		return Bundle{}, fmt.Errorf("timeout must be greater than zero and at most 30m")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	options := []azkustodata.QueryOption{azkustodata.NoTruncation(), azkustodata.ServerTimeout(timeout)}
	dataset, err := client.Query(ctx, database, kql.New(schemaQuery), options...)
	if err != nil {
		return Bundle{}, fmt.Errorf("inspect CI schema: %w", err)
	}
	columns, err := kusto.DecodePrimary[schemaColumn](dataset)
	if err != nil {
		return Bundle{}, fmt.Errorf("decode CI schema: %w", err)
	}
	if err := validateSchema(columns); err != nil {
		return Bundle{}, err
	}
	parameters := kql.NewParameters().AddDateTime("windowFrom", metadata.Window.From.UTC()).AddDateTime("windowTo", metadata.Window.To.UTC()).AddString("sourceFilter", metadata.Filters.Source).AddString("jobFilter", metadata.Filters.JobFilter)
	options = append(options, azkustodata.QueryParameters(parameters))
	dataset, err = client.Query(ctx, database, kql.New(populationQuery), options...)
	if err != nil {
		return Bundle{}, fmt.Errorf("collect complete CI results (partial results are not usable): %w", err)
	}
	rows, err := kusto.DecodePrimary[populationRow](dataset)
	if err != nil {
		return Bundle{}, fmt.Errorf("decode CI observations: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Bundle{}, fmt.Errorf("collect CI results: %w", err)
	}
	return assembleBundle(metadata, rows)
}

func validateSchema(columns []schemaColumn) error {
	actual := map[string]map[string]string{}
	for _, column := range columns {
		if actual[column.TableName] == nil {
			actual[column.TableName] = map[string]string{}
		}
		actual[column.TableName][column.ColumnName] = column.ColumnType
	}
	var missing []string
	for table, columns := range requiredSchema {
		for column, columnType := range columns {
			if actual[table][column] != columnType {
				missing = append(missing, table+"."+column+":"+columnType)
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("CI schema prerequisite missing or incompatible: %v; deploy the source-aware CI ingestion schema (ARO-HCP PR #7875) to this database before running failure-patterns; historical empty source values are supported but an absent source column is not", missing)
}

func assembleBundle(metadata Metadata, rows []populationRow) (Bundle, error) {
	metadata.Window.From = metadata.Window.From.UTC()
	metadata.Window.To = metadata.Window.To.UTC()
	bundle := Bundle{SchemaVersion: SchemaVersion, Metadata: metadata, Runs: []Run{}, Observations: []Observation{}}
	for _, row := range rows {
		environment := jobEnvironment(row.JobName)
		if metadata.Environment != "" && environment != metadata.Environment {
			continue
		}
		switch row.Kind {
		case "run":
			bundle.Runs = append(bundle.Runs, Run{BuildID: row.BuildID, Environment: environment, JobName: row.JobName, ProwURL: row.ProwURL,
				FinishedAt: row.RunFinishedAt.UTC(), Failed: row.RunFailed, Processed: row.Processed})
		case "observation":
			observation := Observation{Origin: metadata.Origin, Environment: environment, BuildID: row.BuildID,
				Source: row.Source, TestID: row.TestID, TestName: row.TestName, NameVariants: row.NameVariants,
				StartedAt: row.StartedAt.UTC(), FinishedAt: row.FinishedAt.UTC(), SetupFinishTime: row.SetupFinishTime.UTC(),
				TestStartTime: row.TestStartTime.UTC(), CleanupStartTime: row.CleanupStartTime.UTC(), Text: row.Message}
			observation.Fingerprint = Fingerprint(observation)
			bundle.Observations = append(bundle.Observations, observation)
		default:
			return Bundle{}, fmt.Errorf("unknown CI result row kind %q", row.Kind)
		}
	}
	sort.Slice(bundle.Runs, func(i, j int) bool { return bundle.Runs[i].BuildID < bundle.Runs[j].BuildID })
	sort.Slice(bundle.Observations, func(i, j int) bool { return bundle.Observations[i].Fingerprint < bundle.Observations[j].Fingerprint })
	if err := bundle.Validate(); err != nil {
		return Bundle{}, fmt.Errorf("validate collected CI facts: %w", err)
	}
	return bundle, nil
}
