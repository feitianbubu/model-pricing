package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// catalogModel is hand-maintained model metadata. Empty fields fall back to
// LiteLLM on the gateway; times are RFC 3339 with the vendor's own offset.
type catalogModel struct {
	Vendor          string `json:"vendor,omitempty"`
	Description     string `json:"description,omitempty"`
	Icon            string `json:"icon,omitempty"`
	Tags            string `json:"tags,omitempty"`
	Endpoints       string `json:"endpoints,omitempty"`
	NameRule        int    `json:"name_rule,omitempty"`
	Status          *int   `json:"status,omitempty"`
	ReleasedAt      string `json:"released_at,omitempty"`
	RetiredAt       string `json:"retired_at,omitempty"`
	DeprecationNote string `json:"deprecation_note,omitempty"`
	Source          string `json:"source,omitempty"`
}

type catalogVendor struct {
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Status      *int   `json:"status,omitempty"`
}

type catalogFile struct {
	Vendors map[string]catalogVendor `json:"vendors"`
	Models  map[string]catalogModel  `json:"models"`
}

type newapiModel struct {
	ModelName       string `json:"model_name"`
	VendorName      string `json:"vendor_name"`
	Description     string `json:"description"`
	Icon            string `json:"icon"`
	Tags            string `json:"tags"`
	Endpoints       string `json:"endpoints"`
	NameRule        int    `json:"name_rule"`
	Status          int    `json:"status"`
	ReleaseTime     int64  `json:"release_time"`
	DeprecatedTime  int64  `json:"deprecated_time"`
	DeprecationNote string `json:"deprecation_note"`
}

type newapiVendor struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Status      int    `json:"status"`
}

func unixOrZero(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0, err
	}
	return t.Unix(), nil
}

func statusOrOne(status *int) int {
	if status == nil {
		return 1
	}
	return *status
}

// writeCatalog publishes the gateway's metadata-sync format. Every locale gets
// the same file because the gateway requests one per UI language.
func writeCatalog(path, outDir string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var file catalogFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return 0, err
	}
	models := make([]newapiModel, 0, len(file.Models))
	for name, m := range file.Models {
		if m.Vendor != "" {
			if _, ok := file.Vendors[m.Vendor]; !ok {
				return 0, fmt.Errorf("model %q: vendor %q is not in vendors", name, m.Vendor)
			}
		}
		released, err := unixOrZero(m.ReleasedAt)
		if err != nil {
			return 0, fmt.Errorf("model %q: released_at: %w", name, err)
		}
		retired, err := unixOrZero(m.RetiredAt)
		if err != nil {
			return 0, fmt.Errorf("model %q: retired_at: %w", name, err)
		}
		models = append(models, newapiModel{
			ModelName: name, VendorName: m.Vendor, Description: m.Description, Icon: m.Icon,
			Tags: m.Tags, Endpoints: m.Endpoints, NameRule: m.NameRule, Status: statusOrOne(m.Status),
			ReleaseTime: released, DeprecatedTime: retired, DeprecationNote: m.DeprecationNote,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ModelName < models[j].ModelName })
	vendors := make([]newapiVendor, 0, len(file.Vendors))
	for name, v := range file.Vendors {
		vendors = append(vendors, newapiVendor{Name: name, Description: v.Description, Icon: v.Icon, Status: statusOrOne(v.Status)})
	}
	sort.Slice(vendors, func(i, j int) bool { return vendors[i].Name < vendors[j].Name })

	dirs := []string{filepath.Join(outDir, "api", "newapi")}
	for _, locale := range []string{"zh", "en", "ja"} {
		dirs = append(dirs, filepath.Join(outDir, "api", "i18n", locale, "newapi"))
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, err
		}
		if err := writeEnvelope(filepath.Join(dir, "models.json"), models); err != nil {
			return 0, err
		}
		if err := writeEnvelope(filepath.Join(dir, "vendors.json"), vendors); err != nil {
			return 0, err
		}
	}
	return len(models), nil
}

func writeEnvelope(path string, data any) error {
	encoded, err := json.MarshalIndent(map[string]any{"success": true, "message": "", "data": data}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}
