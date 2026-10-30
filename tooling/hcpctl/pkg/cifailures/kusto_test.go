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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-kusto-go/azkustodata"
	"github.com/Azure/azure-kusto-go/azkustodata/query"
)

func TestSchemaRequiresSourceAndCorrectTypes(t *testing.T) {
	var columns []schemaColumn
	for table, names := range requiredSchema {
		for name, kind := range names {
			columns = append(columns, schemaColumn{table, name, kind})
		}
	}
	if err := validateSchema(columns); err != nil {
		t.Fatal(err)
	}
	for i, column := range columns {
		if column.TableName != "ciTestResults" || column.ColumnName != "source" {
			continue
		}
		columns[i].ColumnType = "dynamic"
		err := validateSchema(columns)
		if err == nil || !strings.Contains(err.Error(), "ciTestResults.source:string") || !strings.Contains(err.Error(), "#7875") {
			t.Fatalf("missing actionable source prerequisite: %v", err)
		}
		columns = append(columns[:i], columns[i+1:]...)
		if err := validateSchema(columns); err == nil {
			t.Fatal("accepted missing source column")
		}
		break
	}
}

func TestAssemblePreservesRepeatedCasesAndMissingNames(t *testing.T) {
	metadata := testBundle().Metadata
	run := populationRow{Kind: "run", BuildID: "run-1", JobName: "pull-ci-Azure-ARO-HCP-main-e2e-parallel", RunFinishedAt: metadata.Window.From, RunFailed: true, Processed: true}
	observation := populationRow{Kind: "observation", BuildID: "run-1", JobName: run.JobName, TestID: "same-id", Message: "failure"}
	bundle, err := assembleBundle(metadata, []populationRow{run, observation, observation})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Observations) != 2 {
		t.Fatal("deduplicated legitimate test cases")
	}
	if bundle.Observations[0].Fingerprint != bundle.Observations[1].Fingerprint {
		t.Fatal("identical inputs have different fingerprints")
	}
	if bundle.Observations[0].TestName != "" || bundle.Observations[0].Source != "" {
		t.Fatal("guessed missing name or historical source")
	}
	if !bundle.Observations[0].StartedAt.IsZero() {
		t.Fatal("zero test timing was changed")
	}
}

func TestTypedSDKParametersAndLateFailures(t *testing.T) {
	for _, mode := range []string{"success", "table-partial", "dataset-partial", "truncated-wire"} {
		t.Run(mode, func(t *testing.T) {
			metadata := testBundle().Metadata
			metadata.Environment = ""
			metadata.Filters = Filters{Source: "rp-upgrade\"; take 1", JobFilter: "job\"; take 1"}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodGet && request.URL.Path == "/v1/rest/auth/metadata" {
					// The unauthenticated SDK probes cloud metadata before querying.
					// An empty 404 uses its documented default cloud configuration.
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				if request.Method != http.MethodPost || request.URL.Path != "/v2/rest/query" {
					t.Errorf("unexpected SDK request %s %s", request.Method, request.URL.Path)
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				var body struct {
					CSL        string `json:"csl"`
					Properties struct {
						Options    map[string]any
						Parameters map[string]string
					} `json:"properties"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Error(err)
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				requests.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				if body.Properties.Options["notruncation"] != true || body.Properties.Options["servertimeout"] == nil {
					t.Error("missing completeness/timeout request properties")
				}
				if strings.Contains(body.CSL, "getschema") {
					var rows [][]any
					for table, columns := range requiredSchema {
						for name, kind := range columns {
							rows = append(rows, []any{table, name, kind})
						}
					}
					writeFrames(t, writer, []wireColumn{{"TableName", "string"}, {"ColumnName", "string"}, {"ColumnType", "string"}}, rows, "success")
					return
				}
				if strings.Contains(body.CSL, metadata.Filters.Source) || strings.Contains(body.CSL, metadata.Filters.JobFilter) {
					t.Error("user filters interpolated into KQL")
				}
				if len(body.Properties.Parameters) != 4 {
					t.Errorf("expected four typed parameters, got %+v", body.Properties.Parameters)
				}
				if !strings.Contains(body.CSL, "declare query_parameters(") {
					t.Error("missing SDK parameter declaration")
				}
				columns := []wireColumn{
					{"rowKind", "string"}, {"buildId", "string"}, {"jobName", "string"}, {"prowUrl", "string"},
					{"runFinishedAt", "datetime"}, {"runFailed", "bool"}, {"processed", "bool"},
					{"source", "string"}, {"testId", "string"}, {"testName", "string"}, {"nameVariants", "long"},
					{"startedAt", "datetime"}, {"finishedAt", "datetime"}, {"setupFinishTime", "datetime"},
					{"testStartTime", "datetime"}, {"cleanupStartTime", "datetime"}, {"message", "string"},
				}
				finished := metadata.Window.From.Format(time.RFC3339Nano)
				run := []any{"run", "run-1", metadata.Filters.JobFilter, "", finished, true, true, "", "", "", 0, nil, nil, nil, nil, nil, ""}
				observation := []any{"observation", "run-1", metadata.Filters.JobFilter, "", finished, true, true, metadata.Filters.Source, "test-1", "", 0, nil, nil, nil, nil, nil, "raw failure\n"}
				// Late failures follow actual rows, not just an empty primary table.
				writeFrames(t, writer, columns, [][]any{run, observation, observation}, mode)
			}))
			defer server.Close()
			client, err := azkustodata.New(azkustodata.NewConnectionStringBuilder(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			bundle, err := Collect(context.Background(), client, "ServiceLogs", metadata, time.Minute)
			if requests.Load() != 2 {
				t.Fatalf("expected schema and population queries, got %d (error: %v)", requests.Load(), err)
			}
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if len(bundle.Observations) != 2 || len(bundle.Runs) != 1 {
					t.Fatal("typed mapping lost rows")
				}
				if bundle.Observations[0].Text != "raw failure\n" || bundle.Observations[0].Source != metadata.Filters.Source || bundle.Observations[0].TestName != "" || !bundle.Observations[0].StartedAt.IsZero() {
					t.Fatal("typed mapping changed raw or missing fields")
				}
				return
			}
			if err == nil || bundle.SchemaVersion != 0 {
				t.Fatalf("accepted %s response: %+v, %v", mode, bundle, err)
			}
			if mode != "truncated-wire" && !strings.Contains(err.Error(), "LimitsExceeded") {
				t.Fatalf("expected real late query-limit failure, got %v", err)
			}
		})
	}
}

type wireColumn struct {
	Name string `json:"ColumnName"`
	Kind string `json:"ColumnType"`
}

func writeFrames(t *testing.T, writer http.ResponseWriter, columns []wireColumn, rows [][]any, mode string) {
	t.Helper()
	if rows == nil {
		rows = [][]any{}
	}
	encodedColumns, err := json.Marshal(columns)
	if err != nil {
		t.Fatal(err)
	}
	encodedRows, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	// Fragmented v2 is newline-delimited, with ordered JSON properties in
	// table headers. Maps or one Encode(frames) call do not model this protocol.
	header := `{"FrameType":"DataSetHeader","IsProgressive":false,"Version":"v2.0","IsFragmented":true,"ErrorReportingPlacement":"EndOfTable"}`
	if mode == "truncated-wire" {
		if _, err := fmt.Fprint(writer, "["+header+"\n,"+`{"FrameType":`); err != nil {
			t.Error(err)
		}
		return
	}
	const apiErrors = `[{"error":{"code":"LimitsExceeded","message":"E_QUERY_RESULT_SET_TOO_LARGE","@type":"KustoServicePartialQueryFailureLimitsExceededException","@message":"results truncated"}}]`
	tableErrors, datasetErrors := "", ""
	hasErrors := false
	if mode == "table-partial" {
		tableErrors = `,"OneApiErrors":` + apiErrors
	}
	if mode == "dataset-partial" {
		hasErrors = true
		datasetErrors = `,"OneApiErrors":` + apiErrors
	}
	frames := []string{
		header,
		`{"FrameType":"DataTable","TableId":0,"TableKind":"QueryProperties","TableName":"@ExtendedProperties","Columns":[{"ColumnName":"TableId","ColumnType":"int"},{"ColumnName":"Key","ColumnType":"string"},{"ColumnName":"Value","ColumnType":"dynamic"}],"Rows":[]}`,
		fmt.Sprintf(`{"FrameType":"TableHeader","TableId":1,"TableKind":"PrimaryResult","TableName":"PrimaryResult","Columns":%s}`, encodedColumns),
		fmt.Sprintf(`{"FrameType":"TableFragment","TableFragmentType":"DataAppend","TableId":1,"Rows":%s}`, encodedRows),
		fmt.Sprintf(`{"FrameType":"TableCompletion","TableId":1,"RowCount":%d%s}`, len(rows), tableErrors),
		fmt.Sprintf(`{"FrameType":"DataSetCompletion","HasErrors":%t,"Cancelled":false%s}`, hasErrors, datasetErrors),
	}
	if _, err := fmt.Fprint(writer, "["+strings.Join(frames, "\n,")+"\n]\n"); err != nil {
		t.Error(err)
	}
}

type deadlineQueryClient struct{ t *testing.T }

func (client deadlineQueryClient) Query(ctx context.Context, _ string, _ azkustodata.Statement, _ ...azkustodata.QueryOption) (query.Dataset, error) {
	client.t.Helper()
	if _, ok := ctx.Deadline(); !ok {
		client.t.Fatal("collection query has no explicit deadline")
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCollectionTimeoutIsBoundedAndReturnsNoBundle(t *testing.T) {
	bundle, err := Collect(context.Background(), deadlineQueryClient{t}, "ServiceLogs", testBundle().Metadata, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || bundle.SchemaVersion != 0 {
		t.Fatalf("timeout produced usable partial results: %+v, %v", bundle, err)
	}
	for _, timeout := range []time.Duration{0, -time.Second, 31 * time.Minute} {
		if _, err := Collect(context.Background(), nil, "ServiceLogs", testBundle().Metadata, timeout); err == nil {
			t.Fatalf("accepted unbounded timeout %s", timeout)
		}
	}
}

type schemaFailureQueryClient struct{ err error }

func (client schemaFailureQueryClient) Query(context.Context, string, azkustodata.Statement, ...azkustodata.QueryOption) (query.Dataset, error) {
	return nil, client.err
}

func TestSchemaTransportFailureDoesNotSuggestDeployment(t *testing.T) {
	for _, message := range []string{"authentication failed", "connection refused"} {
		cause := errors.New(message)
		_, err := Collect(context.Background(), schemaFailureQueryClient{cause}, "ServiceLogs", testBundle().Metadata, time.Minute)
		if !errors.Is(err, cause) || !strings.Contains(err.Error(), "inspect CI schema") || strings.Contains(err.Error(), "deploy") {
			t.Fatalf("misclassified schema query transport failure: %v", err)
		}
	}
}
