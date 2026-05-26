// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

import (
	"encoding/json"
	"fmt"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/spf13/cobra"
)

// TestLabelsOptions holds the options for the 'image labels' command.
type TestLabelsOptions struct {
	ImageName  string
	Tier       string
	Format     string
	DistroRoot string
}

// TestPlanResult is the JSON shape emitted by the 'image labels' command
// and consumed by 'image plan'. It is kept stable for compatibility.
type TestPlanResult struct {
	ImageName             string         `json:"image_name"`
	Tier                  string         `json:"tier"`
	SelectedLabels        []string       `json:"selected_labels"`
	EstimatedDurationMins int            `json:"estimated_duration_minutes"`
	FrameworkFilters      map[string]any `json:"framework_filters"`
	ImageCapabilities     map[string]any `json:"image_capabilities,omitempty"`

	// Resolution is the full, structured resolver output. It is included
	// in the JSON when --format=plan or --format=explain is used.
	Resolution *Resolution `json:"resolution,omitempty"`
}

func labelsOnAppInit(_ *azldev.App, parentCmd *cobra.Command) {
	parentCmd.AddCommand(NewImageLabelsCmd())
}

// NewImageLabelsCmd constructs a [cobra.Command] for the 'image labels' command.
func NewImageLabelsCmd() *cobra.Command {
	options := &TestLabelsOptions{}

	cmd := &cobra.Command{
		Use:   "labels [image-name] [tier]",
		Short: "Resolve test labels for an Azure Linux image at a validation tier",
		Long: `Resolve test labels for an Azure Linux image at a validation tier.

The catalog is loaded from two TOML files in the Azure Linux source repo:
  <distro>/base/tests/framework-labels.toml — label catalog
  <distro>/base/images/images.toml          — per-image test-workflow maps

The distro root is resolved (in order) from:
  1. --distro-root flag
  2. -C / azldev.toml project directory
  3. $AZLDEV_DISTRO_ROOT
  4. walking up from cwd for base/tests/framework-labels.toml

The tier is a key under [images.<image-name>.test-workflows] (e.g.
pr_validation, nightly_validation, release_validation).

Resolution evaluates each label against the image's self-reported
capabilities (from [images.<name>.capabilities] in images.toml), drops
labels whose required capabilities are not met, drops labels whose
framework is disabled in [frameworks], and produces a concrete list of
framework-native filters for the runners.`,
		Example: `  # Resolve a tier for an image.
  azldev image labels vm-base pr_validation

  # Explain selection in human-readable form.
  azldev image labels vm-base pr_validation --format explain

  # Point at a specific Azure Linux checkout.
  azldev image labels container-base pr_validation \
      --distro-root ~/azurelinux`,
		Args: cobra.ExactArgs(2),
		RunE: azldev.RunFuncWithExtraArgs(func(env *azldev.Env, args []string) (any, error) {
			options.ImageName = args[0]
			options.Tier = args[1]
			return nil, discoverTestLabels(env, options)
		}),
	}

	cmd.Flags().StringVar(&options.Format, "format", "labels",
		"Output format: labels | json | plan | explain")
	cmd.Flags().StringVar(&options.DistroRoot, "distro-root", "",
		"Path to the Azure Linux repo root (default: -C dir, $AZLDEV_DISTRO_ROOT, or auto-detected)")

	return cmd
}

// discoverTestLabels implements the core logic for the 'image labels' command.
func discoverTestLabels(env *azldev.Env, options *TestLabelsOptions) error {
	if env.Config() == nil {
		return fmt.Errorf("no configuration loaded")
	}

	result, err := generateTestLabelsNative(env, options.ImageName, options.Tier, options.DistroRoot)
	if err != nil {
		return fmt.Errorf("failed to resolve test labels: %w", err)
	}

	switch options.Format {
	case "labels":
		for _, label := range result.SelectedLabels {
			fmt.Println(label)
		}
	case "json":
		b, err := json.MarshalIndent(result.SelectedLabels, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal labels: %w", err)
		}
		fmt.Println(string(b))
	case "plan":
		b, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal plan: %w", err)
		}
		fmt.Println(string(b))
	case "explain":
		fmt.Print(formatExplain(result.Resolution))
	default:
		return fmt.Errorf("invalid format %q; must be one of: labels, json, plan, explain", options.Format)
	}
	return nil
}
