// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/spf13/cobra"
)

// WorkflowDefinition represents a test workflow configuration
type WorkflowDefinition struct {
	Description string   `toml:"description" json:"description"`
	Labels      []string `toml:"labels" json:"labels"`
	MaxDuration int      `toml:"max_duration_minutes" json:"maxDurationMinutes"`
	Priority    string   `toml:"priority" json:"priority"`
}

// WorkflowConfig represents the entire workflow configuration file
type WorkflowConfig struct {
	Metadata  WorkflowMetadata             `toml:"metadata" json:"metadata"`
	Workflows map[string]WorkflowDefinition `toml:"workflows" json:"workflows"`
}

// WorkflowMetadata represents metadata about the workflow configuration
type WorkflowMetadata struct {
	Version     string `toml:"version" json:"version"`
	Description string `toml:"description" json:"description"`
	Author      string `toml:"author" json:"author"`
}

// DefineWorkflowOptions holds the options for the 'test define-workflow' command
type DefineWorkflowOptions struct {
	ConfigFile string
	OutputDir  string
	Validate   bool
}

// NewDefineWorkflowCmd constructs a cobra.Command for the 'test define-workflow' command
func NewDefineWorkflowCmd() *cobra.Command {
	options := &DefineWorkflowOptions{}

	cmd := &cobra.Command{
		Use:   "define-workflow [workflow-config.toml]",
		Short: "Define test workflows from TOML configuration",
		Long: `Define test workflows from a TOML configuration file.

This command loads workflow definitions from a TOML file and stores them
for use by other azldev commands like 'image labels'. Workflows define
which test labels should be applied for different validation stages.

The TOML file should contain workflow definitions with labels and metadata.

Example workflow configuration:
  [metadata]
  version = "1.0.0"
  description = "Azure Linux test workflows"
  
  [workflows.pr_validation]
  description = "Fast validation for pull requests"
  labels = ["smoke_tests", "basic_functionality"]
  max_duration_minutes = 60
  priority = "high"`,
		Example: `  # Define workflows from a TOML file
  azldev test define-workflow workflows.toml

  # Validate workflow configuration without storing
  azldev test define-workflow workflows.toml --validate

  # Store workflows in specific directory
  azldev test define-workflow workflows.toml --output-dir ~/.azldev/workflows`,
		Args: cobra.ExactArgs(1),
		RunE: azldev.RunFuncWithExtraArgs(func(env *azldev.Env, args []string) (interface{}, error) {
			options.ConfigFile = args[0]
			return nil, defineWorkflow(env, options)
		}),
	}

	cmd.Flags().StringVar(&options.OutputDir, "output-dir", "", 
		"Directory to store workflow definitions (defaults to project .azldev/workflows)")
	cmd.Flags().BoolVar(&options.Validate, "validate", false,
		"Validate workflow configuration without storing")

	return cmd
}

// defineWorkflow implements the core logic for the 'test define-workflow' command
func defineWorkflow(env *azldev.Env, options *DefineWorkflowOptions) error {
	// Load and parse workflow configuration
	workflowConfig, err := loadWorkflowConfig(options.ConfigFile)
	if err != nil {
		return fmt.Errorf("failed to load workflow configuration: %w", err)
	}

	// Validate configuration
	if err := validateWorkflowConfig(workflowConfig); err != nil {
		return fmt.Errorf("invalid workflow configuration: %w", err)
	}

	fmt.Printf("✅ Loaded %d workflow definitions from %s\n", len(workflowConfig.Workflows), options.ConfigFile)

	// If validate-only mode, just print and exit
	if options.Validate {
		fmt.Println("📋 Workflow Configuration:")
		for name, workflow := range workflowConfig.Workflows {
			fmt.Printf("  %s: %s (labels: %v)\n", name, workflow.Description, workflow.Labels)
		}
		return nil
	}

	// Determine output directory
	outputDir := options.OutputDir
	if outputDir == "" {
		projectDir := env.ProjectDir()
		outputDir = filepath.Join(projectDir, ".azldev", "workflows")
	}

	// Ensure output directory exists
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create workflows directory: %w", err)
	}

	// Store workflow configuration
	outputPath := filepath.Join(outputDir, "workflows.json")
	if err := storeWorkflowConfig(workflowConfig, outputPath); err != nil {
		return fmt.Errorf("failed to store workflow configuration: %w", err)
	}

	fmt.Printf("📁 Stored workflow definitions to %s\n", outputPath)
	return nil
}

// loadWorkflowConfig loads workflow configuration from a TOML file
func loadWorkflowConfig(configPath string) (*WorkflowConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	config := &WorkflowConfig{}
	if err := parseSimpleTOML(string(data), config); err != nil {
		return nil, fmt.Errorf("failed to parse TOML: %w", err)
	}

	return config, nil
}

// parseSimpleTOML is a simple TOML parser for workflow configurations
func parseSimpleTOML(content string, config *WorkflowConfig) error {
	// Initialize maps
	config.Workflows = make(map[string]WorkflowDefinition)
	
	lines := strings.Split(content, "\n")
	var currentSection string
	var currentWorkflow *WorkflowDefinition
	var workflowName string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Section headers
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.Trim(line, "[]")
			
			// Save previous workflow if we were building one
			if currentWorkflow != nil && workflowName != "" {
				config.Workflows[workflowName] = *currentWorkflow
				currentWorkflow = nil
				workflowName = ""
			}

			if section == "metadata" {
				currentSection = "metadata"
			} else if section == "workflows" {
				// Handle [workflows] section - just switch context
				currentSection = "workflows"
			} else if strings.HasPrefix(section, "workflows.") {
				currentSection = "workflow"
				workflowName = strings.TrimPrefix(section, "workflows.")
				currentWorkflow = &WorkflowDefinition{}
			}
			continue
		}

		// Key-value pairs
		if strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			value = strings.Trim(value, `"`)

			switch currentSection {
			case "metadata":
				switch key {
				case "version":
					config.Metadata.Version = value
				case "description":
					config.Metadata.Description = value
				case "author":
					config.Metadata.Author = value
				}
			case "workflow":
				if currentWorkflow != nil {
					switch key {
					case "description":
						currentWorkflow.Description = value
					case "labels", "test_labels":  // Support both field names
						// Parse array: ["label1", "label2"]
						if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
							arrayContent := strings.Trim(value, "[]")
							if arrayContent != "" {
								labels := strings.Split(arrayContent, ",")
								for i, label := range labels {
									labels[i] = strings.Trim(strings.TrimSpace(label), `"`)
								}
								currentWorkflow.Labels = labels
							}
						}
					case "max_duration_minutes":
						// Simple integer parsing
						if duration := parseInt(value); duration > 0 {
							currentWorkflow.MaxDuration = duration
						}
					case "priority":
						currentWorkflow.Priority = value
					}
				}
			}
		}
	}

	// Save last workflow if exists
	if currentWorkflow != nil && workflowName != "" {
		config.Workflows[workflowName] = *currentWorkflow
	}

	return nil
}

// parseInt is a simple integer parser
func parseInt(s string) int {
	result := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			result = result*10 + int(r-'0')
		} else {
			return 0
		}
	}
	return result
}

// validateWorkflowConfig validates the workflow configuration
func validateWorkflowConfig(config *WorkflowConfig) error {
	if len(config.Workflows) == 0 {
		return fmt.Errorf("no workflows defined")
	}

	for name, workflow := range config.Workflows {
		if workflow.Description == "" {
			return fmt.Errorf("workflow %q missing description", name)
		}
		if len(workflow.Labels) == 0 {
			return fmt.Errorf("workflow %q has no labels defined", name)
		}
	}

	return nil
}

// storeWorkflowConfig stores workflow configuration as JSON
func storeWorkflowConfig(config *WorkflowConfig, outputPath string) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(outputPath, data, 0644)
}