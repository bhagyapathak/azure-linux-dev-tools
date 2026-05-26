// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/spf13/cobra"
)

// TestPlanOptions holds the options for the 'image plan' command.
type TestPlanOptions struct {
	ImageName  string
	Workflow   string
	Framework  string
	Execute    bool
	DryRun     bool
	DistroRoot string
}

func planOnAppInit(_ *azldev.App, parentCmd *cobra.Command) {
	parentCmd.AddCommand(NewImagePlanCmd())
}

// NewImagePlanCmd constructs a [cobra.Command] for the 'image plan' command.
func NewImagePlanCmd() *cobra.Command {
	options := &TestPlanOptions{}

	cmd := &cobra.Command{
		Use:   "plan [image-name] [workflow]",
		Short: "Generate and optionally execute a comprehensive test plan",
		Long: `Generate a comprehensive test plan for an Azure Linux image configuration.

Creates a detailed test execution plan including:
- Selected test labels based on image capabilities
- Framework-specific filters (TMT, LISA, OpenQA)
- Duration estimates and workflow compliance
- Execution commands for each framework

Optionally execute the plan using available test frameworks.

Supported workflows:
  - pr_validation: Fast feedback for pull requests (< 60 min)
  - merge_validation: Moderate testing for merge to main (< 120 min)
  - nightly_validation: Comprehensive nightly testing (< 480 min) 
  - release_validation: Complete pre-release testing (< 360 min)

Supported frameworks:
  - tmt: Test Management Tool with FMF metadata
  - lisa: Linux Integration Services Automation (Azure focus)
  - openqa: Open Source automated testing for operating systems`,
		Example: `  # Generate test plan for Azure VM image
  azldev image plan vm-azure pr_validation

  # Execute TMT tests only
  azldev image plan vm-azure merge_validation --framework tmt --execute

  # Dry run to see what would be executed
  azldev image plan desktop-gnome nightly_validation --dry-run

  # Execute all available frameworks
  azldev image plan container-base release_validation --execute`,
		Args: cobra.ExactArgs(2),
		RunE: azldev.RunFuncWithExtraArgs(func(env *azldev.Env, args []string) (interface{}, error) {
			if len(args) < 2 {
				return nil, fmt.Errorf("requires exactly 2 arguments: image-name and workflow")
			}
			options.ImageName = args[0]
			options.Workflow = args[1]
			return nil, generateTestPlan(env, options)
		}),
	}

	cmd.Flags().StringVar(&options.Framework, "framework", "",
		"Limit execution to specific framework: tmt, lisa, openqa")
	cmd.Flags().BoolVar(&options.Execute, "execute", false,
		"Execute the test plan using available frameworks")
	cmd.Flags().BoolVar(&options.DryRun, "dry-run", false,
		"Show what would be executed without running tests")
	cmd.Flags().StringVar(&options.DistroRoot, "distro-root", "",
		"Path to the Azure Linux repo root (default: $AZLDEV_DISTRO_ROOT or auto-detected)")

	return cmd
}

// generateTestPlan implements the core logic for the 'image plan' command.
func generateTestPlan(env *azldev.Env, options *TestPlanOptions) error {
	config := env.Config()
	if config == nil {
		return fmt.Errorf("no configuration loaded")
	}

	// Validate image exists in configuration
	image, exists := config.Images[options.ImageName]
	if !exists {
		return fmt.Errorf("image %q not found in project configuration", options.ImageName)
	}

	// Validate framework if specified
	if options.Framework != "" {
		validFrameworks := []string{"tmt", "lisa", "openqa"}
		frameworkValid := false
		for _, f := range validFrameworks {
			if f == options.Framework {
				frameworkValid = true
				break
			}
		}
		if !frameworkValid {
			return fmt.Errorf("invalid framework %q; must be one of: %v", options.Framework, validFrameworks)
		}
	}

	fmt.Printf("Generating test plan for %s (%s workflow)\n", options.ImageName, options.Workflow)
	fmt.Printf("Image: %s\n", image.Description)
	fmt.Printf("Capabilities: %v\n", image.Capabilities.EnabledNames())
	fmt.Printf("Publish channels: %v\n", image.Publish.Channels)
	fmt.Println()

	if options.Execute || options.DryRun {
		return executeTestPlan(env, options)
	}

	// Just generate and display the plan
	result, err := generateTestLabelsNative(env, options.ImageName, options.Workflow, options.DistroRoot)
	if err != nil {
		return fmt.Errorf("failed to generate test plan: %w", err)
	}

	fmt.Printf("📊 Test Plan Summary:\n")
	fmt.Printf("   Selected Labels: %d\n", len(result.SelectedLabels))
	fmt.Printf("   Estimated Duration: %d minutes\n", result.EstimatedDurationMins)
	fmt.Println()

	fmt.Printf("🏷️  Selected Test Labels:\n")
	for _, label := range result.SelectedLabels {
		fmt.Printf("   • %s\n", label)
	}
	fmt.Println()

	fmt.Printf("🔧 Framework Filters:\n")
	for framework, filter := range result.FrameworkFilters {
		if filter != nil && filter != "" && filter != "{}" {
			switch framework {
			case "tmt":
				fmt.Printf("   TMT: %v\n", filter)
			case "lisa": 
				filterBytes, _ := json.Marshal(filter)
				fmt.Printf("   LISA: %s\n", string(filterBytes))
			case "openqa":
				fmt.Printf("   OpenQA: %v\n", filter)
			}
		}
	}

	return nil
}

// executeTestPlan executes the test plan using the full orchestration tool.
func executeTestPlan(env *azldev.Env, options *TestPlanOptions) error {
	// Find the project root directory 
	projectRoot := env.ProjectDir()
	orchestrationDir := filepath.Join(projectRoot, "test-orchestration")
	
	// Check if full orchestration tool exists
	orchestrateTool := filepath.Join(orchestrationDir, "orchestrate.py")
	if exists, _ := fileutils.Exists(env.FS(), orchestrateTool); !exists {
		return fmt.Errorf("full orchestration tool not found at %s; using simple discovery only", orchestrateTool)
	}

	// Build command arguments
	args := []string{orchestrateTool, filepath.Join(projectRoot, "azldev.toml"), options.ImageName, options.Workflow}
	
	if options.Framework != "" {
		args = append(args, "--framework", options.Framework)
	}
	
	if options.Execute && !options.DryRun {
		args = append(args, "--execute")
	} else if options.DryRun {
		args = append(args, "--dry-run")
	}
	
	args = append(args, "--verbose")

	fmt.Printf("🚀 Executing: python3 %s\n", strings.Join(args[1:], " "))
	fmt.Println()

	// Execute the full orchestration tool
	cmd := exec.Command("python3", args[1:]...)
	cmd.Dir = orchestrationDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr 
	
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("test plan execution failed with exit code %d", exitErr.ExitCode())
		}
		return fmt.Errorf("failed to execute test plan: %w", err)
	}

	fmt.Println()
	fmt.Printf("✅ Test plan execution completed\n")

	return nil
}