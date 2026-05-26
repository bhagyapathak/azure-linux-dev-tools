// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

import (
	"fmt"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
)

// =============================================================================
// label_generator: bridge between azldev's image config and the Catalog
// resolver in catalog.go. Holds nothing framework-specific — all dispatch
// and selection logic lives in catalog.go.
// =============================================================================

// ImageTestCapabilities is the inferred capability summary used by plan.go.
// It mirrors the image config's typed booleans plus publish-channel info.
type ImageTestCapabilities struct {
	MachineBootable          bool     `json:"machineBootable"`
	Container                bool     `json:"container"`
	Systemd                  bool     `json:"systemd"`
	RuntimePackageManagement bool     `json:"runtimePackageManagement"`
	CapabilityTokens         []string `json:"capabilityTokens"`
	PublishChannels          []string `json:"publishChannels"`
	ImageName                string   `json:"imageName"`
	Description              string   `json:"description"`
}

// generateTestLabelsNative resolves an (image, tier) pair against the
// Catalog loaded from the distro repo. distroRoot may be empty; in that case
// the azldev project directory (-C / azldev.toml) is tried first, then
// $AZLDEV_DISTRO_ROOT, then walking up from cwd.
func generateTestLabelsNative(env *azldev.Env, imageName, tier, distroRoot string) (*TestPlanResult, error) {
	if distroRoot == "" {
		distroRoot = env.ProjectDir()
	}
	cat, err := LoadCatalog(distroRoot)
	if err != nil {
		return nil, fmt.Errorf("loading distro catalog: %w", err)
	}

	caps, err := extractImageCapabilities(env, imageName)
	if err != nil {
		return nil, err
	}

	res, err := cat.Resolve(imageName, tier, caps.CapabilityTokens)
	if err != nil {
		return nil, err
	}

	selected := make([]string, 0, len(res.Labels))
	for _, l := range res.Labels {
		selected = append(selected, l.Name)
	}

	return &TestPlanResult{
		ImageName:             imageName,
		Tier:                  res.Tier,
		SelectedLabels:        selected,
		EstimatedDurationMins: res.EstimatedMinutes,
		FrameworkFilters:      res.FrameworkFilters(),
		ImageCapabilities:     convertImageCapabilitiesToInterface(caps),
		Resolution:            res,
	}, nil
}

// extractImageCapabilities reads the image's self-reported capabilities
// from the azldev project config.
func extractImageCapabilities(env *azldev.Env, imageName string) (*ImageTestCapabilities, error) {
	config := env.Config()
	if config == nil {
		return nil, fmt.Errorf("no configuration loaded")
	}
	img, exists := config.Images[imageName]
	if !exists {
		return nil, fmt.Errorf("image %q not found in project configuration", imageName)
	}

	return &ImageTestCapabilities{
		ImageName:                imageName,
		Description:              img.Description,
		MachineBootable:          img.Capabilities.IsMachineBootable(),
		Container:                img.Capabilities.IsContainer(),
		Systemd:                  img.Capabilities.IsSystemd(),
		RuntimePackageManagement: img.Capabilities.IsRuntimePackageManagement(),
		CapabilityTokens:         img.Capabilities.EnabledNames(),
		PublishChannels:          img.Publish.Channels,
	}, nil
}

// convertImageCapabilitiesToInterface keeps the historical JSON shape for
// `--format plan` output consumers.
func convertImageCapabilitiesToInterface(caps *ImageTestCapabilities) map[string]any {
	return map[string]any{
		"description":                caps.Description,
		"machine_bootable":           caps.MachineBootable,
		"container":                  caps.Container,
		"systemd":                    caps.Systemd,
		"runtime_package_management": caps.RuntimePackageManagement,
		"capability_tokens":          caps.CapabilityTokens,
		"publish_channels":           caps.PublishChannels,
	}
}

// formatExplain renders a Resolution as a human-readable block for the
// `--format explain` output mode.
func formatExplain(res *Resolution) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Image:        %s\n", res.ImageName)
	fmt.Fprintf(&b, "Tier:         %s\n", res.Tier)
	fmt.Fprintf(&b, "Capabilities: %v\n", res.ImageCaps)
	fmt.Fprintln(&b)

	if len(res.Labels) == 0 {
		fmt.Fprintln(&b, "No labels selected.")
	} else {
		fmt.Fprintf(&b, "Selected labels (%d):\n", len(res.Labels))
		byFw := map[string][]ResolvedLabel{}
		for _, l := range res.Labels {
			byFw[l.Framework] = append(byFw[l.Framework], l)
		}
		for _, fw := range []string{"tmt", "lisa", "openqa", "pytest", "image", "component", "pending"} {
			ls := byFw[fw]
			if len(ls) == 0 {
				continue
			}
			fmt.Fprintf(&b, "  [%s]\n", fw)
			for _, l := range ls {
				pendingTag := ""
				if l.Pending {
					pendingTag = " (pending: no filter yet)"
				}
				fmt.Fprintf(&b, "    - %-32s ~%dm  %s%s\n",
					l.Name, l.EstimatedMinutes, l.Description, pendingTag)
				if l.RetryCount > 0 || l.TimeoutMin > 0 {
					fmt.Fprintf(&b, "        policy: retry=%d timeout=%dm\n", l.RetryCount, l.TimeoutMin)
				}
				if l.Filter != nil && !l.Pending {
					fmt.Fprintf(&b, "        filter: %v\n", l.Filter)
				}
			}
		}
	}

	if len(res.Skipped) > 0 {
		fmt.Fprintf(&b, "\nSkipped (%d):\n", len(res.Skipped))
		for _, l := range res.Skipped {
			fmt.Fprintf(&b, "  - %-32s %s\n", l.Name, l.SkippedReason)
		}
	}

	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Estimated runtime: %d min\n", res.EstimatedMinutes)

	if len(res.Warnings) > 0 {
		fmt.Fprintln(&b, "\nWarnings:")
		for _, w := range res.Warnings {
			fmt.Fprintf(&b, "  ! %s\n", w)
		}
	}
	return b.String()
}
