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
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/go-logr/logr"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"sigs.k8s.io/yaml"

	"github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"

	"github.com/Azure/ARO-HCP/tooling/utilitytypes/timing"
)

const (
	gcsBucket          = "test-platform-results"
	localConfigPath    = "aro-hcp-provision-environment/artifacts/config.yaml"
	writeConfigPath    = "aro-hcp-write-config/artifacts/config.yaml"
	testStepPersistent = "aro-hcp-test-persistent"
	testStepLocal      = "aro-hcp-test-local"
)

// JobKind categorizes Prow jobs by their execution workflow.
type JobKind int

const (
	// JobKindLocalE2E is the DEV PR workflow that provisions an on-demand environment.
	// Uses aro-hcp-local-e2e workflow with aro-hcp-provision-environment and aro-hcp-test-local.
	JobKindLocalE2E JobKind = iota

	// JobKindPersistentE2E covers all persistent-environment runs: PR-triggered
	// presubmits (stage/int/prod), periodics, and rehearsals of any of these.
	// Uses aro-hcp-persistent-e2e workflow with aro-hcp-write-config and aro-hcp-test-persistent.
	JobKindPersistentE2E

	// JobKindEV2Gated covers EV2-triggered postsubmit jobs that carry ev2.rollout/*
	// annotations and can resolve config from the sdp-pipelines repo.
	// Uses aro-hcp-persistent-e2e or aro-hcp-e2e workflow with aro-hcp-test-persistent.
	JobKindEV2Gated
)

// ProwJobInfo holds the parsed information from a Prow job URL.
type ProwJobInfo struct {
	URL              string
	JobName          string // full job name as it appears in GCS (may include rehearsal prefix)
	CanonicalJobName string // job name with rehearsal prefix stripped, used for classification
	ProwID           string
	GCSPrefix        string
}

// IsRehearsal reports whether the job is a pj-rehearse wrapper around another job.
func (p *ProwJobInfo) IsRehearsal() bool {
	return p.JobName != p.CanonicalJobName
}

// Kind classifies the job by its execution workflow.
func (p *ProwJobInfo) Kind() JobKind {
	name := p.CanonicalJobName
	if strings.HasPrefix(name, "branch-ci") {
		return JobKindEV2Gated
	}
	// The DEV local-e2e job has "aro-hcp-e2e-parallel" as its test name.
	// Persistent-env jobs use names like "stage-e2e-parallel", "integration-e2e-parallel", "prod-e2e-parallel".
	if strings.HasSuffix(name, "aro-hcp-e2e-parallel") {
		return JobKindLocalE2E
	}
	return JobKindPersistentE2E
}

// stripRehearsalPrefix removes the "rehearse-<PR#>-" prefix that the
// pj-rehearse plugin prepends to rehearsed job names.
func stripRehearsalPrefix(jobName string) string {
	if !strings.HasPrefix(jobName, "rehearse-") {
		return jobName
	}
	rest := strings.TrimPrefix(jobName, "rehearse-")
	idx := strings.Index(rest, "-")
	if idx < 0 {
		return jobName
	}
	return rest[idx+1:]
}

// ProwJobConfig holds the Kusto connection info extracted from a Prow job's config.yaml.
type ProwJobConfig struct {
	Region          string
	KustoName       string
	HCPDatabase     string
	ServiceDatabase string

	// ServiceClusterName and ManagementClusterName are the AKS cluster names
	// used to filter Kusto queries to only relevant clusters. These are only
	// populated for PR (pull-ci) jobs where the shared Kusto database contains
	// data from multiple clusters.
	ServiceClusterName    string
	ManagementClusterName string
}

// prowJobMetadata is the minimal subset of a Kubernetes ProwJob object
// needed to extract ev2.rollout/* annotations from prowjob.json.
type prowJobMetadata struct {
	Metadata struct {
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
}

const (
	annotationCloud        = "ev2.rollout/cloud"
	annotationEnvironment  = "ev2.rollout/environment"
	annotationRegion       = "ev2.rollout/region"
	annotationSDPPipelines = "ev2.rollout/sdp-pipelines"
)

// ev2Annotations holds the extracted EV2 rollout annotations from a ProwJob.
type ev2Annotations struct {
	Cloud        string
	Environment  string
	Region       string
	SDPPipelines string // commit SHA in the sdp-pipelines repo
}

// extractEV2Annotations parses prowjob.json data and extracts the required
// ev2.rollout/* annotations. Returns an error listing any missing annotations.
func extractEV2Annotations(data []byte) (*ev2Annotations, error) {
	var pj prowJobMetadata
	if err := json.Unmarshal(data, &pj); err != nil {
		return nil, fmt.Errorf("failed to parse prowjob.json: %w", err)
	}

	required := []struct {
		key   string
		field *string
	}{
		{annotationCloud, nil},
		{annotationEnvironment, nil},
		{annotationRegion, nil},
		{annotationSDPPipelines, nil},
	}
	result := &ev2Annotations{}
	required[0].field = &result.Cloud
	required[1].field = &result.Environment
	required[2].field = &result.Region
	required[3].field = &result.SDPPipelines

	var missing []string
	for _, r := range required {
		v, ok := pj.Metadata.Annotations[r.key]
		if !ok || v == "" {
			missing = append(missing, r.key)
		} else {
			*r.field = v
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("prowjob.json is missing required ev2.rollout annotations: %s", strings.Join(missing, ", "))
	}
	return result, nil
}

// TestResult represents a single test with its metadata.
type TestResult struct {
	Name             string
	Output           string
	Error            string
	Failed           bool
	StartTime        time.Time
	EndTime          time.Time
	ResourceGroup    string // extracted from test output
	SetupFinishTime  time.Time
	TestStartTime    time.Time
	CleanupStartTime time.Time
}

// ParseProwURL extracts job name, Prow ID, GCS prefix, and PR status from a Prow job URL.
// Supports two formats:
//   - Periodic/postsubmit: https://prow.ci.openshift.org/view/gs/test-platform-results/logs/<job>/<prow-id>
//   - Presubmit (PR): https://prow.ci.openshift.org/view/gs/test-platform-results/pr-logs/pull/<org_repo>/<pr>/<job>/<prow-id>
func ParseProwURL(rawURL string) (*ProwJobInfo, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}

	var segments []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segments = append(segments, s)
		}
	}

	for i, seg := range segments {
		if seg == "pr-logs" {
			if i+5 >= len(segments) {
				return nil, fmt.Errorf("URL path must contain pr-logs/pull/<org_repo>/<pr>/<job>/<prow-id>, got %q", u.Path)
			}
			if segments[i+1] != "pull" {
				return nil, fmt.Errorf("expected \"pull\" after \"pr-logs\" in URL path, got %q", segments[i+1])
			}
			prowID := segments[i+5]
			if _, err := strconv.ParseUint(prowID, 10, 64); err != nil {
				return nil, fmt.Errorf("prow ID %q is not a valid number", prowID)
			}
			jobName := segments[i+4]
			return &ProwJobInfo{
				URL:              rawURL,
				JobName:          jobName,
				CanonicalJobName: stripRehearsalPrefix(jobName),
				ProwID:           prowID,
				GCSPrefix:        strings.Join(segments[i:i+6], "/"),
			}, nil
		}
		if seg == "logs" {
			if i+2 >= len(segments) {
				return nil, fmt.Errorf("URL path must contain logs/<job>/<prow-id>, got %q", u.Path)
			}
			prowID := segments[i+2]
			if _, err := strconv.ParseUint(prowID, 10, 64); err != nil {
				return nil, fmt.Errorf("prow ID %q is not a valid number", prowID)
			}
			jobName := segments[i+1]
			return &ProwJobInfo{
				URL:              rawURL,
				JobName:          jobName,
				CanonicalJobName: stripRehearsalPrefix(jobName),
				ProwID:           prowID,
				GCSPrefix:        strings.Join(segments[i:i+3], "/"),
			}, nil
		}
	}

	return nil, fmt.Errorf("URL path does not contain a \"logs\" or \"pr-logs\" segment: %q", u.Path)
}

// FetchProwJobConfig resolves the Kusto connection configuration for a Prow job.
//
// For local-e2e (DEV) jobs, it downloads config from the aro-hcp-provision-environment
// GCS artifact. For persistent-e2e jobs (PR, periodic, rehearsal), it downloads config
// from the aro-hcp-write-config GCS artifact. For EV2-gated jobs, it first tries the
// aro-hcp-write-config artifact and falls back to prowjob.json ev2.rollout/* annotations
// resolved through the sdp-pipelines repo.
func FetchProwJobConfig(ctx context.Context, info *ProwJobInfo, sdpPipelinesDir string) (*ProwJobConfig, error) {
	logger := logr.FromContextOrDiscard(ctx)

	switch info.Kind() {
	case JobKindLocalE2E:
		return fetchGCSConfig(ctx, info, localConfigPath, logger)
	case JobKindPersistentE2E:
		return fetchGCSConfig(ctx, info, writeConfigPath, logger)
	case JobKindEV2Gated:
		config, err := fetchGCSConfig(ctx, info, writeConfigPath, logger)
		if err != nil {
			logger.V(1).Info("GCS config not available for EV2 job, falling back to sdp-pipelines", "err", err)
			return fetchEV2JobConfig(ctx, info, sdpPipelinesDir, logger)
		}
		return config, nil
	default:
		return nil, fmt.Errorf("unknown job kind for %q", info.JobName)
	}
}

// fetchGCSConfig downloads a config.yaml from a step's GCS artifact directory
// and parses the Kusto connection info. The configRelPath is relative to the
// artifact directory (e.g. "aro-hcp-write-config/artifacts/config.yaml").
func fetchGCSConfig(ctx context.Context, info *ProwJobInfo, configRelPath string, logger logr.Logger) (*ProwJobConfig, error) {
	gcsClient, err := storage.NewClient(ctx, option.WithoutAuthentication())
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}
	defer gcsClient.Close()

	artifactDir, err := findArtifactDir(ctx, gcsClient, info.JobName, info.GCSPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to find artifact directory: %w", err)
	}
	logger.V(1).Info("Found artifact directory", "dir", artifactDir)

	configGCSPath := fmt.Sprintf("%s/artifacts/%s/%s", info.GCSPrefix, artifactDir, configRelPath)
	configData, err := downloadObject(ctx, gcsClient, configGCSPath)
	if err != nil {
		return nil, fmt.Errorf("failed to download config from %s: %w", configRelPath, err)
	}

	jobConfig, err := parseConfig(configData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config.yaml from %s: %w", configRelPath, err)
	}
	logger.V(1).Info("Parsed job config from GCS",
		"configPath", configRelPath,
		"region", jobConfig.Region,
		"kusto", jobConfig.KustoName,
		"serviceDB", jobConfig.ServiceDatabase,
		"hcpDB", jobConfig.HCPDatabase,
	)
	return jobConfig, nil
}

// fetchEV2JobConfig downloads prowjob.json from GCS, extracts the ev2.rollout/*
// annotations, and reads the rendered config from the sdp-pipelines repo at the
// annotated commit SHA. This is the fallback path for EV2-gated jobs.
func fetchEV2JobConfig(ctx context.Context, info *ProwJobInfo, sdpPipelinesDir string, logger logr.Logger) (*ProwJobConfig, error) {
	if sdpPipelinesDir == "" {
		return nil, fmt.Errorf("--sdp-pipelines-dir is required for non-PR jobs to resolve Kusto config from the sdp-pipelines repo")
	}

	gcsClient, err := storage.NewClient(ctx, option.WithoutAuthentication())
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}
	defer gcsClient.Close()

	// Download and parse prowjob.json for EV2 annotations.
	prowJobPath := fmt.Sprintf("%s/prowjob.json", info.GCSPrefix)
	prowJobData, err := downloadObject(ctx, gcsClient, prowJobPath)
	if err != nil {
		return nil, fmt.Errorf("failed to download prowjob.json: %w", err)
	}

	annotations, err := extractEV2Annotations(prowJobData)
	if err != nil {
		return nil, err
	}
	logger.V(1).Info("Extracted EV2 annotations",
		"cloud", annotations.Cloud,
		"environment", annotations.Environment,
		"region", annotations.Region,
		"sdpPipelines", annotations.SDPPipelines,
	)

	// Read the rendered config from the sdp-pipelines repo at the annotated commit.
	configPath := filepath.Join("hcp", "rendered", annotations.Cloud, annotations.Environment, annotations.Region+".yaml")
	gitRef := fmt.Sprintf("%s:%s", annotations.SDPPipelines, configPath)

	cmd := exec.CommandContext(ctx, "git", "show", gitRef)
	cmd.Dir = sdpPipelinesDir
	configData, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("failed to read %s from sdp-pipelines at commit %s: %s\n(try running 'git fetch' in %s)",
				configPath, annotations.SDPPipelines, strings.TrimSpace(string(exitErr.Stderr)), sdpPipelinesDir)
		}
		return nil, fmt.Errorf("failed to run git show in %s: %w", sdpPipelinesDir, err)
	}

	jobConfig, err := parseConfig(configData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse rendered config from sdp-pipelines: %w", err)
	}
	// Non-PR jobs don't need cluster name filtering since each environment
	// has its own Kusto database.
	jobConfig.ServiceClusterName = ""
	jobConfig.ManagementClusterName = ""

	logger.V(1).Info("Parsed non-PR job config from sdp-pipelines",
		"region", jobConfig.Region,
		"kusto", jobConfig.KustoName,
		"serviceDB", jobConfig.ServiceDatabase,
		"hcpDB", jobConfig.HCPDatabase,
		"sdpCommit", annotations.SDPPipelines,
	)
	return jobConfig, nil
}

// FetchProwJobTestResults downloads test results and timing metadata from a
// Prow job's GCS artifacts. This is independent of the config resolution path.
func FetchProwJobTestResults(ctx context.Context, info *ProwJobInfo) ([]TestResult, error) {
	logger := logr.FromContextOrDiscard(ctx)

	gcsClient, err := storage.NewClient(ctx, option.WithoutAuthentication())
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}
	defer gcsClient.Close()

	// Find the artifact directory.
	artifactDir, err := findArtifactDir(ctx, gcsClient, info.JobName, info.GCSPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to find artifact directory: %w", err)
	}
	logger.V(1).Info("Found artifact directory", "dir", artifactDir)

	artifactPrefix := fmt.Sprintf("%s/artifacts/%s", info.GCSPrefix, artifactDir)

	// Download test results.
	testStep := testStepPersistent
	if info.Kind() == JobKindLocalE2E {
		testStep = testStepLocal
	}
	testResultsPrefix := fmt.Sprintf("%s/%s/artifacts/extension_test_result_e2e_", artifactPrefix, testStep)
	testResultFiles, err := listObjects(ctx, gcsClient, testResultsPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list test result files: %w", err)
	}
	if len(testResultFiles) == 0 {
		return nil, fmt.Errorf("no extension_test_result_e2e_*.json files found under %s", testResultsPrefix)
	}

	var allResults extensiontests.ExtensionTestResults
	for _, objPath := range testResultFiles {
		data, err := downloadObject(ctx, gcsClient, objPath)
		if err != nil {
			logger.Error(err, "Failed to download test result file, skipping", "path", objPath)
			continue
		}
		var results extensiontests.ExtensionTestResults
		if err := json.Unmarshal(data, &results); err != nil {
			logger.Error(err, "Failed to parse test result file, skipping", "path", objPath)
			continue
		}
		allResults = append(allResults, results...)
	}

	// Convert all test results.
	var tests []TestResult
	numFailed := 0
	for _, result := range allResults {
		tr := TestResult{
			Name:   result.Name,
			Output: result.Output,
			Error:  result.Error,
			Failed: result.Result == extensiontests.ResultFailed,
		}
		if result.StartTime != nil {
			tr.StartTime = time.Time(*result.StartTime)
		}
		if result.EndTime != nil {
			tr.EndTime = time.Time(*result.EndTime)
		}
		tr.ResourceGroup = ExtractResourceGroup(result.Output)
		tests = append(tests, tr)
		if tr.Failed {
			numFailed++
		}
	}

	logger.Info("Found test results", "total", len(tests), "failed", numFailed)

	// Enrich test results with timing boundaries from timing metadata.
	testTimings := fetchTestTimings(ctx, gcsClient, artifactPrefix, logger)
	for i := range tests {
		if t, ok := testTimings[tests[i].Name]; ok {
			tests[i].SetupFinishTime = t.SetupFinishTime
			tests[i].TestStartTime = t.TestStartTime
			tests[i].CleanupStartTime = t.CleanupStartTime
		}
	}

	return tests, nil
}

const timingMetadataPath = "aro-hcp-gather-test-visualization/artifacts/test-timing/"

// testTimingBoundaries holds the derived phase boundaries for a single test.
type testTimingBoundaries struct {
	SetupFinishTime  time.Time
	TestStartTime    time.Time
	CleanupStartTime time.Time
}

// identityContainerStep reports whether a step name refers to identity container setup.
func identityContainerStep(name string) bool {
	return strings.Contains(strings.ToLower(name), "identity container")
}

// deriveSetupTestBoundary inspects the steps in the timing metadata and returns:
//   - setupFinishTime: the latest FinishedAt among steps whose name contains
//     "identity container" (setup steps).
//   - testStartTime: the earliest StartedAt among steps whose name does NOT
//     contain "identity container" (test steps).
//
// Either or both may be zero when the relevant steps are not present or their
// timestamps cannot be parsed.
func deriveSetupTestBoundary(steps []timing.StepTimingMetadata) (setupFinishTime, testStartTime time.Time) {
	for _, step := range steps {
		if identityContainerStep(step.Name) {
			if step.FinishedAt != "" {
				if t, err := time.Parse(time.RFC3339, step.FinishedAt); err == nil {
					if setupFinishTime.IsZero() || t.After(setupFinishTime) {
						setupFinishTime = t
					}
				}
			}
		} else {
			if step.StartedAt != "" {
				if t, err := time.Parse(time.RFC3339, step.StartedAt); err == nil {
					if testStartTime.IsZero() || t.Before(testStartTime) {
						testStartTime = t
					}
				}
			}
		}
	}
	return setupFinishTime, testStartTime
}

// fetchTestTimings downloads timing metadata files from the GCS bucket and
// returns a map from test name to the derived timing boundaries. The top-level
// finishedAt marks when cleanup began. Steps whose name contains "identity
// container" are treated as setup; the remaining steps are the test itself.
func fetchTestTimings(ctx context.Context, gcsClient *storage.Client, artifactPrefix string, logger logr.Logger) map[string]testTimingBoundaries {
	prefix := fmt.Sprintf("%s/%stiming-metadata-", artifactPrefix, timingMetadataPath)
	logger.V(1).Info("Fetching timing metadata", "prefix", prefix)
	files, err := listObjects(ctx, gcsClient, prefix)
	if err != nil {
		logger.V(1).Info("Could not list timing metadata files, skipping timing enrichment", "err", err)
		return nil
	}
	if len(files) == 0 {
		logger.V(1).Info("No timing metadata files found")
		return nil
	}

	result := make(map[string]testTimingBoundaries)
	for _, objPath := range files {
		fileName := objPath[strings.LastIndex(objPath, "/")+1:]
		if !strings.HasPrefix(fileName, "timing-metadata-") {
			continue
		}
		if !strings.HasSuffix(fileName, ".yaml") && !strings.HasSuffix(fileName, ".yaml.gz") {
			continue
		}

		data, err := downloadObject(ctx, gcsClient, objPath)
		if err != nil {
			logger.V(1).Info("Failed to download timing metadata file, skipping", "path", objPath, "err", err)
			continue
		}

		var timingBytes []byte
		if strings.HasSuffix(objPath, ".gz") {
			gzipReader, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				logger.V(1).Info("Failed to create gzip reader for timing metadata, skipping", "path", objPath, "err", err)
				continue
			}
			timingBytes, err = io.ReadAll(gzipReader)
			gzipReader.Close()
			if err != nil {
				logger.V(1).Info("Failed to decompress timing metadata, skipping", "path", objPath, "err", err)
				continue
			}
		} else {
			timingBytes = data
		}

		var tm timing.SpecTimingMetadata
		if err := yaml.Unmarshal(timingBytes, &tm); err != nil {
			logger.V(1).Info("Failed to unmarshal timing metadata, skipping", "path", objPath, "err", err)
			continue
		}

		testName := strings.Join(tm.Identifier, " ")
		boundaries := testTimingBoundaries{}

		if tm.FinishedAt != "" {
			t, err := time.Parse(time.RFC3339, tm.FinishedAt)
			if err == nil {
				boundaries.CleanupStartTime = t
			} else {
				logger.Error(err, "Failed to parse finishedAt from timing metadata, skipping", "path", objPath)
			}
		}

		boundaries.SetupFinishTime, boundaries.TestStartTime = deriveSetupTestBoundary(tm.Steps)
		result[testName] = boundaries
	}

	logger.V(1).Info("Loaded test timing boundaries from timing metadata", "count", len(result))
	return result
}

// resourceGroupRegex matches log lines like:
//
//	"msg"="creating resource group" "resourceGroup"="private-keyvault-gxsj99"
var resourceGroupRegex = regexp.MustCompile(`"resourceGroup"="([^"]+)"`)

// ExtractResourceGroup parses the resource group name from test output logs.
// Tests log a line like: "msg"="creating resource group" "resourceGroup"="<name>"
func ExtractResourceGroup(output string) string {
	matches := resourceGroupRegex.FindStringSubmatch(output)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}

// sourceConfig represents the fields we read from the Prow job's config.yaml.
type sourceConfig struct {
	Kusto sourceKusto `json:"kusto"`
	Svc   sourceAKS   `json:"svc"`
	Mgmt  sourceAKS   `json:"mgmt"`
}

type sourceKusto struct {
	KustoName                      string `json:"kustoName"`
	Location                       string `json:"location"`
	HostedControlPlaneLogsDatabase string `json:"hostedControlPlaneLogsDatabase"`
	ServiceLogsDatabase            string `json:"serviceLogsDatabase"`
}

type sourceAKS struct {
	AKS sourceAKSName `json:"aks"`
}

type sourceAKSName struct {
	Name string `json:"name"`
}

func parseConfig(data []byte) (*ProwJobConfig, error) {
	var src sourceConfig
	if err := yaml.Unmarshal(data, &src); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	return &ProwJobConfig{
		Region:                src.Kusto.Location,
		KustoName:             src.Kusto.KustoName,
		HCPDatabase:           src.Kusto.HostedControlPlaneLogsDatabase,
		ServiceDatabase:       src.Kusto.ServiceLogsDatabase,
		ServiceClusterName:    src.Svc.AKS.Name,
		ManagementClusterName: src.Mgmt.AKS.Name,
	}, nil
}

// findArtifactDir lists subdirectories under artifacts/ and returns the one
// whose name is a suffix of the job name. Longest match wins.
func findArtifactDir(ctx context.Context, gcsClient *storage.Client, jobName, gcsPrefix string) (string, error) {
	prefix := fmt.Sprintf("%s/artifacts/", gcsPrefix)
	it := gcsClient.Bucket(gcsBucket).Objects(ctx, &storage.Query{
		Prefix:    prefix,
		Delimiter: "/",
	})

	var bestMatch string
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return "", fmt.Errorf("failed to list objects: %w", err)
		}
		if attrs.Prefix == "" {
			continue
		}
		dir := strings.TrimPrefix(attrs.Prefix, prefix)
		dir = strings.TrimSuffix(dir, "/")
		if strings.HasSuffix(jobName, dir) {
			if len(dir) > len(bestMatch) {
				bestMatch = dir
			}
		}
	}

	if bestMatch == "" {
		return "", fmt.Errorf("no artifact directory found matching a suffix of job name %q under %s", jobName, prefix)
	}
	return bestMatch, nil
}

func listObjects(ctx context.Context, gcsClient *storage.Client, prefix string) ([]string, error) {
	it := gcsClient.Bucket(gcsBucket).Objects(ctx, &storage.Query{
		Prefix: prefix,
	})

	var objects []string
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}
		if attrs.Name != "" {
			objects = append(objects, attrs.Name)
		}
	}
	return objects, nil
}

func downloadObject(ctx context.Context, gcsClient *storage.Client, path string) ([]byte, error) {
	reader, err := gcsClient.Bucket(gcsBucket).Object(path).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open object %s: %w", path, err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read object %s: %w", path, err)
	}
	return data, nil
}

// SanitizeTestName replaces characters that are not alphanumeric, dashes, or
// underscores with underscores, producing a valid filesystem path component.
func SanitizeTestName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
