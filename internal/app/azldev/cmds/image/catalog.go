// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/pelletier/go-toml/v2"
)

// =============================================================================
// Catalog: data-driven test-label resolver fed by TOML files in the distro.
//
// Two TOMLs are consumed:
//   <distro>/base/tests/framework-labels.toml — label catalog
//   <distro>/base/images/images.toml          — per-image test-workflow map
//
// The resolver dispatches labels by which filter field is present
// (fmf_filter / lisa_criteria / openqa_suites / pytest_markers / pending),
// not by string prefix. New frameworks add a new field; no Go change in
// the dispatch core is required.
//
// Image-side mapping lives directly on each image:
//   [images.<name>.test-workflows]
//   pr_validation      = [...]
//   nightly_validation = [...]
//
// Execution policy (retry_count, timeout_min) lives on each label in
// framework-labels.toml, not on tiers.
// =============================================================================

// Label is one entry in framework-labels.toml. Each bound label declares
// its framework explicitly via the `type` field ("tmt", "lisa", "openqa",
// or "pytest"). A label may instead be `pending = true` with no `type`
// set — the resolver tracks it as pending and emits no filter.
type Label struct {
	Description          string   `toml:"description"`
	Type                 string   `toml:"type,omitempty"`
	EstimatedMinutes     int      `toml:"estimated_minutes,omitempty"`
	Pending              bool     `toml:"pending,omitempty"`
	RequiresCapabilities []string `toml:"requires_capabilities,omitempty"`

	// Execution policy — applied per-label by the runner.
	RetryCount int `toml:"retry_count,omitempty"`
	TimeoutMin int `toml:"timeout_min,omitempty"`

	// Framework-native filters.
	FmfFilter    string         `toml:"fmf_filter,omitempty"`
	LisaCriteria map[string]any `toml:"lisa_criteria,omitempty"`
	OpenqaSuites []string       `toml:"openqa_suites,omitempty"`

	// Pytest filter: marker expressions and optional file globs / args.
	// PytestMarkers is the primary dispatch signal (filled in = pytest label).
	PytestMarkers []string `toml:"pytest_markers,omitempty"`
	PytestFiles   []string `toml:"pytest_files,omitempty"`
	PytestArgs    []string `toml:"pytest_args,omitempty"`
}

// FrameworkConfig is one entry in [frameworks]. Enabled is a pointer so we
// can distinguish "unset" (default: enabled) from explicit `enabled = false`.
type FrameworkConfig struct {
	Enabled *bool `toml:"enabled,omitempty"`
}

// LabelsFile is the parsed shape of framework-labels.toml.
type LabelsFile struct {
	Metadata        map[string]any             `toml:"metadata"`
	TMTLabels       map[string]Label           `toml:"tmt_labels"`
	LISALabels      map[string]Label           `toml:"lisa_labels"`
	OpenqaLabels    map[string]Label           `toml:"openqa_labels"`
	PytestLabels    map[string]Label           `toml:"pytest_labels"`
	ImageLabels     map[string]Label           `toml:"image_labels"`
	ComponentLabels map[string]Label           `toml:"component_labels"`
	Frameworks      map[string]FrameworkConfig `toml:"frameworks,omitempty"`
}

// Catalog holds everything the resolver needs.
type Catalog struct {
	// DistroRoot is the directory containing base/tests/framework-labels.toml.
	DistroRoot string

	// Labels is a flat lookup table keyed by label name. Built by merging
	// all per-framework label maps from LabelsFile.
	Labels map[string]Label

	// Frameworks is the per-framework enablement table. Absent entries
	// default to enabled. A label whose framework is disabled is skipped
	// during resolution with a clear reason.
	Frameworks map[string]FrameworkConfig

	// ImageWorkflows[imageName][tier] = [label names].
	// Populated from `[images.<name>.test-workflows]` tables in images.toml.
	ImageWorkflows map[string]map[string][]string
}

// frameworkEnabled reports whether the given framework is enabled. A
// framework is enabled unless [frameworks].<name>.enabled is explicitly false.
func (c *Catalog) frameworkEnabled(fw string) bool {
	fc, ok := c.Frameworks[fw]
	if !ok || fc.Enabled == nil {
		return true
	}
	return *fc.Enabled
}

// LoadCatalog finds the TOML files under distroRoot and parses them.
// If distroRoot is empty, LoadCatalog tries:
//  1. $AZLDEV_DISTRO_ROOT
//  2. walks up from cwd looking for base/tests/framework-labels.toml
func LoadCatalog(distroRoot string) (*Catalog, error) {
	if distroRoot == "" {
		if env := os.Getenv("AZLDEV_DISTRO_ROOT"); env != "" {
			distroRoot = env
		}
	}
	if distroRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("cannot resolve cwd: %w", err)
		}
		found, err := findDistroRoot(cwd)
		if err != nil {
			return nil, err
		}
		distroRoot = found
	}

	abs, err := filepath.Abs(distroRoot)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve distro root %q: %w", distroRoot, err)
	}

	labelsPath := filepath.Join(abs, "base", "tests", "framework-labels.toml")
	imagesPath := filepath.Join(abs, "base", "images", "images.toml")

	var lf LabelsFile
	if err := loadTOML(labelsPath, &lf); err != nil {
		return nil, fmt.Errorf("loading labels: %w", err)
	}

	// Build the flat label table. Later sections do not override earlier
	// ones — we detect collisions and report them.
	labels := make(map[string]Label)
	for _, src := range []map[string]Label{
		lf.TMTLabels, lf.LISALabels, lf.OpenqaLabels, lf.PytestLabels,
		lf.ImageLabels, lf.ComponentLabels,
	} {
		for name, l := range src {
			if _, dup := labels[name]; dup {
				return nil, fmt.Errorf("duplicate label %q in %s", name, labelsPath)
			}
			labels[name] = l
		}
	}

	cat := &Catalog{
		DistroRoot:     abs,
		Labels:         labels,
		Frameworks:     lf.Frameworks,
		ImageWorkflows: map[string]map[string][]string{},
	}

	if err := loadImageWorkflows(imagesPath, cat); err != nil {
		return nil, fmt.Errorf("loading image workflows: %w", err)
	}

	return cat, nil
}

func loadTOML(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// findDistroRoot walks up from start looking for the labels TOML.
func findDistroRoot(start string) (string, error) {
	probe := filepath.Join("base", "tests", "framework-labels.toml")
	dir := start
	for i := 0; i < 32; i++ {
		if _, err := os.Stat(filepath.Join(dir, probe)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not locate %s by walking up from %s; pass --distro-root or set $AZLDEV_DISTRO_ROOT", probe, start)
}

// loadImageWorkflows extracts `[images.<name>.test-workflows]` blocks from
// images.toml. The rest of images.toml (kiwi definitions, capabilities,
// static test-suites) is parsed separately by azldev's project-config code.
func loadImageWorkflows(path string, cat *Catalog) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	// Parse loosely as a tree of maps; we only care about the
	// test-workflows subtable on each image.
	var raw struct {
		Images map[string]map[string]any `toml:"images"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	for imageName, imageBody := range raw.Images {
		tw, ok := imageBody["test-workflows"]
		if !ok {
			continue
		}
		twMap, ok := tw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: images.%s.test-workflows is not a table", path, imageName)
		}
		tiers := map[string][]string{}
		for tier, val := range twMap {
			arr, ok := val.([]any)
			if !ok {
				return fmt.Errorf("%s: images.%s.test-workflows.%s must be a list of label names",
					path, imageName, tier)
			}
			labels := make([]string, 0, len(arr))
			for _, v := range arr {
				s, ok := v.(string)
				if !ok {
					return fmt.Errorf("%s: images.%s.test-workflows.%s contains a non-string entry",
						path, imageName, tier)
				}
				labels = append(labels, s)
			}
			tiers[tier] = labels
		}
		if len(tiers) > 0 {
			cat.ImageWorkflows[imageName] = tiers
		}
	}
	return nil
}

// =============================================================================
// Resolution
// =============================================================================

// ResolvedLabel is one entry in the resolution output.
type ResolvedLabel struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	Framework        string `json:"framework"` // "tmt", "lisa", "openqa", "pytest", "image", "component", "pending"
	Filter           any    `json:"filter,omitempty"`
	EstimatedMinutes int    `json:"estimated_minutes"`
	RetryCount       int    `json:"retry_count,omitempty"`
	TimeoutMin       int    `json:"timeout_min,omitempty"`
	Pending          bool   `json:"pending,omitempty"`
	SkippedReason    string `json:"skipped_reason,omitempty"`
}

// Resolution is the output of resolving an image+tier.
type Resolution struct {
	Tier             string          `json:"tier"`
	ImageName        string          `json:"image_name"`
	ImageCaps        []string        `json:"image_capabilities"`
	Labels           []ResolvedLabel `json:"labels"`
	Skipped          []ResolvedLabel `json:"skipped,omitempty"`
	EstimatedMinutes int             `json:"estimated_minutes"`
	Warnings         []string        `json:"warnings,omitempty"`
}

// Resolve expands the given tier of an image against the supplied
// capability set. The label list is taken from
// `[images.<imageName>.test-workflows.<tier>]` in images.toml. Each label
// is then gated by its own `requires_capabilities` and by the framework
// kill switch.
func (c *Catalog) Resolve(imageName, tier string, imageCaps []string) (*Resolution, error) {
	tiers, ok := c.ImageWorkflows[imageName]
	if !ok {
		return nil, fmt.Errorf("image %q has no [images.%s.test-workflows] block in images.toml; known images: %v",
			imageName, imageName, c.knownImages())
	}
	labelList, ok := tiers[tier]
	if !ok {
		return nil, fmt.Errorf("image %q has no tier %q; known tiers for this image: %v",
			imageName, tier, sortedKeys(tiers))
	}

	capSet := sliceToSet(imageCaps)
	res := &Resolution{
		Tier:      tier,
		ImageName: imageName,
		ImageCaps: append([]string{}, imageCaps...),
	}

	// Warn on undeclared label references up-front for clearer output.
	for _, lbl := range labelList {
		if _, ok := c.Labels[lbl]; !ok {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("image %s tier %s references undeclared label %q", imageName, tier, lbl))
		}
	}

	seen := map[string]bool{}
	for _, lbl := range labelList {
		c.expand(lbl, capSet, seen, res)
	}

	for _, rl := range res.Labels {
		res.EstimatedMinutes += rl.EstimatedMinutes
	}

	sort.Slice(res.Labels, func(i, j int) bool { return res.Labels[i].Name < res.Labels[j].Name })
	sort.Slice(res.Skipped, func(i, j int) bool { return res.Skipped[i].Name < res.Skipped[j].Name })
	return res, nil
}

// expand gates a single label and emits it to res. There is no recursion —
// each call handles one label. The dedup map prevents double-counting when
// the same label appears twice in a tier's list.
func (c *Catalog) expand(name string, caps map[string]bool, seen map[string]bool, res *Resolution) {
	if seen[name] {
		return
	}
	seen[name] = true

	lbl, ok := c.Labels[name]
	if !ok {
		res.Skipped = append(res.Skipped, ResolvedLabel{
			Name:          name,
			Framework:     "unknown",
			SkippedReason: "label not declared in framework-labels.toml",
		})
		return
	}

	if missing := missingCaps(caps, lbl.RequiresCapabilities); len(missing) > 0 {
		res.Skipped = append(res.Skipped, ResolvedLabel{
			Name:          name,
			Description:   lbl.Description,
			Framework:     frameworkOf(lbl),
			SkippedReason: fmt.Sprintf("image lacks required capabilities: %v", missing),
		})
		return
	}

	fw := frameworkOf(lbl)
	if !c.frameworkEnabled(fw) {
		res.Skipped = append(res.Skipped, ResolvedLabel{
			Name:          name,
			Description:   lbl.Description,
			Framework:     fw,
			SkippedReason: fmt.Sprintf("framework %q is disabled", fw),
		})
		return
	}

	rl := ResolvedLabel{
		Name:             name,
		Description:      lbl.Description,
		Framework:        fw,
		EstimatedMinutes: lbl.EstimatedMinutes,
		RetryCount:       lbl.RetryCount,
		TimeoutMin:       lbl.TimeoutMin,
		Pending:          lbl.Pending,
	}
	switch fw {
	case "tmt":
		rl.Filter = lbl.FmfFilter
	case "lisa":
		rl.Filter = lbl.LisaCriteria
	case "openqa":
		rl.Filter = lbl.OpenqaSuites
	case "pytest":
		rl.Filter = map[string]any{
			"markers": lbl.PytestMarkers,
			"files":   lbl.PytestFiles,
			"args":    lbl.PytestArgs,
		}
	}
	res.Labels = append(res.Labels, rl)
}

// frameworkOf reports which framework a label belongs to. The explicit
// `type` field is the source of truth; pending labels without a type fall
// back to "pending".
func frameworkOf(l Label) string {
	if l.Type != "" {
		return l.Type
	}
	if l.Pending {
		return "pending"
	}
	return "unknown"
}

func (c *Catalog) knownImages() []string {
	out := make([]string, 0, len(c.ImageWorkflows))
	for k := range c.ImageWorkflows {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// =============================================================================
// Helpers
// =============================================================================

func sliceToSet(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

func missingCaps(have map[string]bool, want []string) []string {
	var missing []string
	for _, c := range want {
		if !have[c] {
			missing = append(missing, c)
		}
	}
	return missing
}

// FrameworkFilters groups resolved labels by framework and produces the
// command-line-ready filter for each.
func (r *Resolution) FrameworkFilters() map[string]any {
	out := map[string]any{
		"tmt":    "",
		"lisa":   map[string]any{},
		"openqa": []string{},
		"pytest": map[string]any{},
	}
	var tmtParts []string
	lisaLabels := []string{}
	openqaSuites := []string{}
	pytestMarkers := []string{}
	pytestFiles := []string{}
	pytestArgs := []string{}

	for _, l := range r.Labels {
		if l.Pending {
			continue
		}
		switch l.Framework {
		case "tmt":
			if s, ok := l.Filter.(string); ok && s != "" {
				tmtParts = append(tmtParts, s)
			}
		case "lisa":
			lisaLabels = append(lisaLabels, l.Name)
		case "openqa":
			if ss, ok := l.Filter.([]string); ok {
				openqaSuites = append(openqaSuites, ss...)
			}
		case "pytest":
			if f, ok := l.Filter.(map[string]any); ok {
				if m, ok := f["markers"].([]string); ok {
					pytestMarkers = append(pytestMarkers, m...)
				}
				if ff, ok := f["files"].([]string); ok {
					pytestFiles = append(pytestFiles, ff...)
				}
				if aa, ok := f["args"].([]string); ok {
					pytestArgs = append(pytestArgs, aa...)
				}
			}
		}
	}

	if len(tmtParts) > 0 {
		out["tmt"] = tmtParts
	}
	if len(lisaLabels) > 0 {
		out["lisa"] = map[string]any{"labels": lisaLabels}
	}
	if len(openqaSuites) > 0 {
		out["openqa"] = openqaSuites
	}
	if len(pytestMarkers) > 0 || len(pytestFiles) > 0 || len(pytestArgs) > 0 {
		py := map[string]any{}
		if len(pytestMarkers) > 0 {
			py["markers"] = dedupStrings(pytestMarkers)
		}
		if len(pytestFiles) > 0 {
			py["files"] = dedupStrings(pytestFiles)
		}
		if len(pytestArgs) > 0 {
			py["args"] = pytestArgs
		}
		out["pytest"] = py
	}
	return out
}

func dedupStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
