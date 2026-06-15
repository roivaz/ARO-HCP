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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/go-logr/logr"
	"github.com/spf13/cobra"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/Azure/ARO-HCP/tooling/hcpctl/pkg/agent"
	snapshotpkg "github.com/Azure/ARO-HCP/tooling/hcpctl/pkg/snapshot"
)

// RawAnalyzeOptions holds the unvalidated CLI options for the analyze subcommand.
type RawAnalyzeOptions struct {
	DataDir             string
	AROHCPPath          string
	HypershiftPath      string
	MaestroPath         string
	ClustersServicePath string
	ReviewRounds        int
	MaxRounds           int
	Output              string
	Model               string

	AuthMode        string
	GitHubTokenFile string
	ModelEndpoint   string
	ModelDeployment string

	Verbosity int
}

func defaultAnalyzeOptions() *RawAnalyzeOptions {
	return &RawAnalyzeOptions{
		ReviewRounds: 3,
		MaxRounds:    50,
		AuthMode:     agent.CopilotAuthModeLoggedIn,
	}
}

func bindAnalyzeOptions(opts *RawAnalyzeOptions, cmd *cobra.Command) error {
	cmd.Flags().StringVar(&opts.AROHCPPath, "aro-hcp", opts.AROHCPPath, "Path to ARO-HCP git checkout (required)")
	cmd.Flags().StringVar(&opts.HypershiftPath, "hypershift", opts.HypershiftPath, "Path to HyperShift git checkout (required)")
	cmd.Flags().StringVar(&opts.MaestroPath, "maestro", opts.MaestroPath, "Path to Maestro git checkout (required)")
	cmd.Flags().StringVar(&opts.ClustersServicePath, "clusters-service", opts.ClustersServicePath, "Path to Clusters Service git checkout (required)")
	cmd.Flags().IntVar(&opts.ReviewRounds, "review-rounds", opts.ReviewRounds, "Number of review rounds")
	cmd.Flags().IntVar(&opts.MaxRounds, "max-rounds", opts.MaxRounds, "Maximum validation rounds per cycle")
	cmd.Flags().StringVar(&opts.Output, "output", opts.Output, "Output directory for analysis results (defaults to data-dir)")
	cmd.Flags().StringVar(&opts.Model, "model", opts.Model, "Override the Copilot model")
	cmd.Flags().StringVar(&opts.AuthMode, "auth-mode", opts.AuthMode, "Copilot auth mode: logged-in, token, or byok")
	cmd.Flags().StringVar(&opts.GitHubTokenFile, "github-token-file", opts.GitHubTokenFile, "Path to file containing GitHub token (required for token auth mode)")
	cmd.Flags().StringVar(&opts.ModelEndpoint, "model-endpoint", opts.ModelEndpoint, "Azure AI Foundry endpoint URL (required for byok auth mode)")
	cmd.Flags().StringVar(&opts.ModelDeployment, "model-deployment", opts.ModelDeployment, "Model deployment name (required for byok auth mode)")

	for _, flag := range []string{"aro-hcp", "hypershift", "maestro", "clusters-service"} {
		if err := cmd.MarkFlagRequired(flag); err != nil {
			return fmt.Errorf("failed to mark %s as required: %w", flag, err)
		}
	}
	return nil
}

type validatedAnalyzeOptions struct {
	dataDir       string
	testDirs      []string // populated when dataDir is a job-level directory (batch mode)
	worktreePaths map[string]string
	reviewRounds  int
	maxRounds     int
	outputDir     string
	agentConfig   *agent.AgentConfig
}

func (o *RawAnalyzeOptions) validate() (*validatedAnalyzeOptions, error) {
	if o.DataDir == "" {
		return nil, fmt.Errorf("data-dir argument is required")
	}

	// Detect whether this is a single-test directory (has manifest.json)
	// or a job-level directory (subdirectories with manifest.json).
	var testDirs []string
	manifestPath := filepath.Join(o.DataDir, "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		// No manifest.json at the top level — scan for subdirectories containing one.
		entries, dirErr := os.ReadDir(o.DataDir)
		if dirErr != nil {
			return nil, fmt.Errorf("cannot read data directory %q: %w", o.DataDir, dirErr)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			subManifest := filepath.Join(o.DataDir, entry.Name(), "manifest.json")
			if _, statErr := os.Stat(subManifest); statErr == nil {
				testDirs = append(testDirs, filepath.Join(o.DataDir, entry.Name()))
			}
		}
		if len(testDirs) == 0 {
			return nil, fmt.Errorf("data directory %q does not contain manifest.json and has no subdirectories with manifest.json", o.DataDir)
		}
	}

	// Verify all worktree paths exist.
	worktreePaths := map[string]string{
		"ARO-HCP":          o.AROHCPPath,
		"hypershift":       o.HypershiftPath,
		"maestro":          o.MaestroPath,
		"clusters-service": o.ClustersServicePath,
	}
	for name, path := range worktreePaths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("--%s path %q: %w", name, path, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("--%s path %q is not a directory", name, path)
		}
	}

	outputDir := o.Output
	if outputDir == "" {
		outputDir = o.DataDir
	}

	// Validate auth mode.
	agentCfg := &agent.AgentConfig{
		AuthMode:        o.AuthMode,
		GitHubTokenFile: o.GitHubTokenFile,
		ModelEndpoint:   o.ModelEndpoint,
		ModelDeployment: o.ModelDeployment,
		Model:           o.Model,
		MaxRounds:       o.MaxRounds,
		Verbosity:       o.Verbosity,
	}
	switch o.AuthMode {
	case agent.CopilotAuthModeLoggedIn, "":
		agentCfg.AuthMode = agent.CopilotAuthModeLoggedIn
	case agent.CopilotAuthModeToken:
		if o.GitHubTokenFile == "" {
			return nil, fmt.Errorf("--github-token-file is required when --auth-mode is %q", o.AuthMode)
		}
		if _, err := os.Stat(o.GitHubTokenFile); err != nil {
			return nil, fmt.Errorf("--github-token-file %q: %w", o.GitHubTokenFile, err)
		}
	case agent.CopilotAuthModeBYOK:
		if o.ModelEndpoint == "" {
			return nil, fmt.Errorf("--model-endpoint is required when --auth-mode is %q", o.AuthMode)
		}
		if o.ModelDeployment == "" {
			return nil, fmt.Errorf("--model-deployment is required when --auth-mode is %q", o.AuthMode)
		}
		// BYOK requires an Azure credential for Entra token acquisition.
		cred, err := azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{
			AdditionallyAllowedTenants:   []string{"*"},
			RequireAzureTokenCredentials: true,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure credential for BYOK auth: %w", err)
		}
		agentCfg.AzureCredential = cred
	default:
		return nil, fmt.Errorf("unknown --auth-mode %q, must be one of: logged-in, token, byok", o.AuthMode)
	}

	return &validatedAnalyzeOptions{
		dataDir:       o.DataDir,
		testDirs:      testDirs,
		worktreePaths: worktreePaths,
		reviewRounds:  o.ReviewRounds,
		maxRounds:     o.MaxRounds,
		outputDir:     outputDir,
		agentConfig:   agentCfg,
	}, nil
}

func (o *validatedAnalyzeOptions) run(ctx context.Context) error {
	if len(o.testDirs) > 0 {
		return o.runBatch(ctx)
	}
	return o.runSingle(ctx, o.dataDir, o.outputDir)
}

func (o *validatedAnalyzeOptions) runBatch(ctx context.Context) error {
	logger := logr.FromContextOrDiscard(ctx)
	total := len(o.testDirs)

	logger.Info("Batch mode: analyzing all tests in directory", "dir", o.dataDir, "tests", total)

	results := make([]testResult, 0, total)

	for i, testDir := range o.testDirs {
		if ctx.Err() != nil {
			break
		}

		testName := filepath.Base(testDir)
		logger.Info(fmt.Sprintf("[%d/%d] Analyzing test", i+1, total), "test", testName)

		testOutputDir := testDir
		if o.outputDir != o.dataDir {
			testOutputDir = filepath.Join(o.outputDir, testName)
		}

		err := o.runSingle(ctx, testDir, testOutputDir)

		tr := testResult{testName: testName, dir: testOutputDir, err: err}
		if err == nil {
			// Read back the analysis to extract root cause and summary for the job-level summary.
			analysisPath := filepath.Join(testOutputDir, "analysis.json")
			if data, readErr := os.ReadFile(analysisPath); readErr == nil {
				var chain agent.HydratedChain
				if jsonErr := json.Unmarshal(data, &chain); jsonErr == nil {
					tr.rootCause = chain.RootCause
					tr.summary = chain.Summary
				}
			}
		}
		results = append(results, tr)

		if err != nil {
			logger.Error(err, "Analysis failed for test, continuing", "test", testName)
		} else {
			logger.Info(fmt.Sprintf("[%d/%d] Analysis complete", i+1, total), "test", testName)
		}
	}

	// Write job-level summary.
	summaryPath := filepath.Join(o.outputDir, "summary.md")
	summary := renderBatchSummary(results)
	if err := os.MkdirAll(o.outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	if err := os.WriteFile(summaryPath, []byte(summary), 0o644); err != nil {
		return fmt.Errorf("failed to write summary.md: %w", err)
	}

	succeeded := 0
	failed := 0
	for _, r := range results {
		if r.err == nil {
			succeeded++
		} else {
			failed++
		}
	}
	logger.Info("Batch analysis complete",
		"total", total,
		"succeeded", succeeded,
		"failed", failed,
		"summary", summaryPath,
	)
	return nil
}

// testResult holds the outcome of a single test analysis in batch mode.
type testResult struct {
	testName  string
	dir       string
	rootCause string
	summary   string
	err       error
}

func renderBatchSummary(results []testResult) string {
	var sb strings.Builder
	sb.WriteString("# Job Analysis Summary\n\n")

	analyzed := 0
	errored := 0
	for _, r := range results {
		if r.err != nil {
			errored++
		} else {
			analyzed++
		}
	}

	sb.WriteString(fmt.Sprintf("**Total tests:** %d | **Analyzed:** %d | **Analysis errors:** %d\n\n",
		len(results), analyzed, errored))

	sb.WriteString("## Per-Test Results\n\n")
	for _, r := range results {
		if r.err != nil {
			sb.WriteString(fmt.Sprintf("### %s\n\n**Status:** Analysis error\n\n```\n%s\n```\n\n", r.testName, r.err.Error()))
		} else {
			sb.WriteString(fmt.Sprintf("### %s\n\n", r.testName))
			if r.rootCause != "" {
				sb.WriteString(fmt.Sprintf("**Root Cause:** %s\n\n", r.rootCause))
			}
			if r.summary != "" {
				sb.WriteString(fmt.Sprintf("%s\n\n", r.summary))
			}
		}
	}

	return sb.String()
}

func (o *validatedAnalyzeOptions) runSingle(ctx context.Context, dataDir, outputDir string) error {
	logger := logr.FromContextOrDiscard(ctx)

	// Read manifest.
	manifestData, err := os.ReadFile(filepath.Join(dataDir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("failed to read manifest: %w", err)
	}
	var manifest snapshotpkg.Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("failed to parse manifest: %w", err)
	}

	testError := readFileOrEmpty(filepath.Join(dataDir, "test_logs", "error.log"))
	testOutput := readFileOrEmpty(filepath.Join(dataDir, "test_logs", "output.log"))
	siblingTests := readFileOrEmpty(filepath.Join(dataDir, "sibling_tests.json"))

	// Create Azure credential for Kusto access.
	cred, err := azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{
		AdditionallyAllowedTenants:   []string{"*"},
		RequireAzureTokenCredentials: true,
	})
	if err != nil {
		return fmt.Errorf("failed to create Azure credential: %w", err)
	}

	// Create Kusto client from manifest info.
	kustoClient, err := agent.NewADXKustoClient(cred, manifest.KustoCluster, manifest.KustoDatabase)
	if err != nil {
		return fmt.Errorf("failed to create Kusto client: %w", err)
	}
	defer func() {
		if err := kustoClient.Close(); err != nil {
			logger.Error(err, "Failed to close Kusto client.")
		}
	}()
	cachedKustoClient := agent.NewCachingKustoClient(kustoClient)

	// Build Copilot session.
	kustoTool := agent.NewKustoTool(cachedKustoClient)
	systemMessage, err := agent.BuildSystemMessageConfig()
	if err != nil {
		return fmt.Errorf("failed to build system message config: %w", err)
	}

	// Set up workspace with symlinks in a temp directory.
	workspaceDir, cleanup, err := setupAnalysisWorkspace(dataDir, o.worktreePaths)
	if err != nil {
		return fmt.Errorf("failed to set up analysis workspace: %w", err)
	}
	defer cleanup()

	copilotClient, err := agent.NewCopilotClient(o.agentConfig)
	if err != nil {
		return fmt.Errorf("failed to create Copilot client: %w", err)
	}
	defer func() {
		if err := copilotClient.Stop(); err != nil {
			logger.Error(err, "Failed to stop Copilot client.")
		}
	}()

	session, err := copilotClient.CreateSession(ctx, logger, agent.SessionConfig{
		WorkingDirectory: workspaceDir,
		SystemMessage:    systemMessage,
		Tools:            []copilot.Tool{kustoTool},
	})
	if err != nil {
		return fmt.Errorf("failed to create Copilot session: %w", err)
	}
	var analysisErr error
	defer func() {
		session.SaveConversation(filepath.Join(outputDir, "conversation.json"))
		if analysisErr == nil {
			if err := session.Delete(ctx); err != nil {
				logger.Error(err, "Failed to delete Copilot session.")
			}
		} else {
			if err := session.Disconnect(); err != nil {
				logger.Error(err, "Failed to disconnect Copilot session.")
			}
		}
	}()

	result, err := agent.Analyze(ctx, logger, session, cachedKustoClient, agent.AnalyzeOptions{
		Manifest:            manifestData,
		TestName:            manifest.TestName,
		TestError:           testError,
		TestOutput:          testOutput,
		SiblingTests:        siblingTests,
		DataDir:             dataDir,
		WorktreePaths:       o.worktreePaths,
		KustoCluster:        manifest.KustoCluster,
		KustoDatabase:       manifest.KustoDatabase,
		MaxValidationRounds: o.maxRounds,
		ReviewRounds:        o.reviewRounds,
	})
	if err != nil {
		analysisErr = err
		return analysisErr
	}
	hydratedChain := result.HydratedChain

	// Write output.
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		analysisErr = fmt.Errorf("failed to create output directory: %w", err)
		return analysisErr
	}

	analysisJSON, err := json.MarshalIndent(hydratedChain, "", "  ")
	if err != nil {
		analysisErr = fmt.Errorf("failed to marshal analysis: %w", err)
		return analysisErr
	}
	if err := os.WriteFile(filepath.Join(outputDir, "analysis.json"), analysisJSON, 0o644); err != nil {
		analysisErr = fmt.Errorf("failed to write analysis.json: %w", err)
		return analysisErr
	}

	rendered := agent.RenderMarkdown(hydratedChain, manifest.TestName)
	if err := os.WriteFile(filepath.Join(outputDir, "analysis.md"), []byte(rendered), 0o644); err != nil {
		analysisErr = fmt.Errorf("failed to write analysis.md: %w", err)
		return analysisErr
	}

	logger.Info("Analysis complete.",
		"outputDir", outputDir,
		"analysisJSON", filepath.Join(outputDir, "analysis.json"),
		"analysisMarkdown", filepath.Join(outputDir, "analysis.md"),
	)

	return nil
}

// setupAnalysisWorkspace creates a temporary directory with symlinks to the
// data directory and source code worktrees. Returns the workspace path and
// a cleanup function that removes it.
func setupAnalysisWorkspace(dataDir string, worktreePaths map[string]string) (string, func(), error) {
	workspaceDir, err := os.MkdirTemp("", "hcpctl-analyze-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp workspace: %w", err)
	}

	cleanup := func() {
		os.RemoveAll(workspaceDir)
	}

	// Symlink data directory.
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to resolve data directory: %w", err)
	}
	if err := os.Symlink(absDataDir, filepath.Join(workspaceDir, "data")); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to symlink data directory: %w", err)
	}

	// Symlink source code worktrees.
	if len(worktreePaths) > 0 {
		srcDir := filepath.Join(workspaceDir, "src")
		if err := os.MkdirAll(srcDir, 0o755); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("failed to create src directory: %w", err)
		}
		for repo, repoPath := range worktreePaths {
			absPath, err := filepath.Abs(repoPath)
			if err != nil {
				cleanup()
				return "", nil, fmt.Errorf("failed to resolve path for %s: %w", repo, err)
			}
			if err := os.Symlink(absPath, filepath.Join(srcDir, repo)); err != nil {
				cleanup()
				return "", nil, fmt.Errorf("failed to symlink worktree for %s: %w", repo, err)
			}
		}
	}

	return workspaceDir, cleanup, nil
}

// readFileOrEmpty reads a file and returns its contents as a string,
// or an empty string if the file does not exist or cannot be read.
func readFileOrEmpty(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func newAnalyzeCommand() (*cobra.Command, error) {
	opts := defaultAnalyzeOptions()
	cmd := &cobra.Command{
		Use:   "analyze <data-dir>",
		Short: "Run LLM-driven root cause analysis on gathered diagnostic data",
		Long: `Analyze a previously gathered diagnostic snapshot using an LLM agent
(GitHub Copilot) to produce a structured root cause analysis.

The agent examines the manifest, test logs, and Kusto query results,
then iteratively refines its analysis through validation and review rounds.

Supports two modes based on the directory structure:
  - Single-test mode: point to a directory containing manifest.json
  - Batch mode: point to a job-level directory whose subdirectories each
    contain manifest.json (the output of "from-prow-job")

In batch mode, each test is analyzed sequentially and a summary.md is
written at the top level aggregating root causes across all tests.
Failures in individual test analyses do not abort the batch.

Requires a logged-in GitHub Copilot CLI session (gh auth login).

The four repository flags point to local git checkouts at the commits
that were deployed when the test ran. The agent uses these to read
source code for evidence in its causal chain.`,
		Example: `  # Analyze a single test snapshot
  hcpctl snapshot analyze ./snapshot-20250101-120000/periodic-ci-.../12345/TestFoo \
    --aro-hcp ~/code/ARO-HCP \
    --hypershift ~/code/hypershift \
    --maestro ~/code/maestro \
    --clusters-service ~/code/clusters-service

  # Analyze all failed tests from a job run (batch mode)
  hcpctl snapshot analyze ./snapshot-20250101-120000/periodic-ci-.../12345/ \
    --aro-hcp ~/code/ARO-HCP \
    --hypershift ~/code/hypershift \
    --maestro ~/code/maestro \
    --clusters-service ~/code/clusters-service

  # With custom model and output directory
  hcpctl snapshot analyze ./data \
    --aro-hcp ~/code/ARO-HCP \
    --hypershift ~/code/hypershift \
    --maestro ~/code/maestro \
    --clusters-service ~/code/clusters-service \
    --model claude-opus-4.7 \
    --output ./results`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.DataDir = args[0]
			verbosity, _ := cmd.Flags().GetInt("verbosity")
			opts.Verbosity = verbosity
			validated, err := opts.validate()
			if err != nil {
				return err
			}
			return validated.run(cmd.Context())
		},
	}
	if err := bindAnalyzeOptions(opts, cmd); err != nil {
		return nil, err
	}
	return cmd, nil
}
