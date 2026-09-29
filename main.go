// model-pricing regenerates a new-api ratio_config preset in this
// deployment's accounting unit (USDExchangeRate=5 as a bookkeeping unit,
// real exchange rate 8): upstream real-USD prices are
// multiplied by factor (8/5=1.6), then hand-maintained overrides —
// already authored in accounting units (official CNY / 5) — are merged on
// top. Any expression the scaler cannot parse aborts the build: publishing
// stale prices is recoverable, publishing wrong prices is not.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

const defaultSource = "https://basellm.github.io/llm-metadata/api/newapi/ratio_config-v1-base.json"

// moneyFields are absolute-price maps; the other numeric ratio_config
// fields are dimensionless multipliers and are never rescaled.
var moneyFields = []string{"model_ratio", "model_price"}

var syncFields = []string{
	"model_ratio", "completion_ratio", "cache_ratio", "create_cache_ratio",
	"image_ratio", "audio_ratio", "audio_completion_ratio", "model_price",
	"billing_mode", "billing_expr",
}

func main() {
	source := flag.String("source", defaultSource, "upstream ratio_config URL quoting real USD")
	factor := flag.Float64("factor", 1.6, "real-USD to accounting-USD multiplier (real exchange rate / USDExchangeRate)")
	cnyRate := flag.Float64("cny-rate", 5, "CNY to accounting-USD divisor (USDExchangeRate) applied to the overrides \"cny\" block")
	overridesPath := flag.String("overrides", "data/overrides.json", "hand-maintained entries in accounting units")
	modelsPath := flag.String("models", "data/models.json", "allowlist of published model names; the only thing that decides membership")
	out := flag.String("out", "docs/ratio_config.json", "output path served by GitHub Pages")
	flag.Parse()

	data, err := fetchRatioConfig(*source)
	if err != nil {
		log.Fatalf("fetch %s: %v", *source, err)
	}
	if expressions, ok := data["billing_expr"].(map[string]any); ok {
		var placeholders []string
		for name, value := range expressions {
			expression, ok := value.(string)
			if !ok {
				log.Fatalf("model %q: billing_expr is not a string", name)
			}
			// All-zero expressions are upstream placeholders for missing cost
			// data; drop them so a genuinely free model must be declared in
			// overrides instead of silently syncing a paid model to free.
			if ExprHasOnlyZeroPrices(expression) {
				placeholders = append(placeholders, name)
				continue
			}
			scaled, err := ScaleExprPrices(expression, *factor)
			if err != nil {
				log.Fatalf("model %q: scale billing_expr: %v", name, err)
			}
			expressions[name] = scaled
		}
		if len(placeholders) > 0 {
			sort.Strings(placeholders)
			fmt.Printf("dropped %d zero-price placeholder expressions: %v\n", len(placeholders), placeholders)
			modes, _ := data["billing_mode"].(map[string]any)
			for _, name := range placeholders {
				delete(expressions, name)
				delete(modes, name)
			}
		}
	}
	for _, field := range moneyFields {
		entries, ok := data[field].(map[string]any)
		if !ok {
			continue
		}
		for name, value := range entries {
			number, ok := value.(float64)
			if !ok {
				log.Fatalf("model %q: %s is not a number", name, field)
			}
			entries[name] = math.Round(number*(*factor)*1e6) / 1e6
		}
	}

	allowlist, err := loadAllowlist(*modelsPath)
	if err != nil {
		log.Fatalf("load allowlist %s: %v", *modelsPath, err)
	}
	local, err := loadOverrides(*overridesPath, *cnyRate)
	if err != nil {
		log.Fatalf("load overrides: %v", err)
	}
	// Membership comes only from the allowlist, never from channel state, so
	// a disabled channel cannot make a model flicker out of the preset.
	for _, field := range syncFields {
		entries, ok := data[field].(map[string]any)
		if !ok {
			continue
		}
		for name := range entries {
			if _, listed := allowlist[name]; !listed {
				delete(entries, name)
			}
		}
	}
	for field, entries := range local {
		target, ok := data[field].(map[string]any)
		if !ok {
			target = make(map[string]any)
			data[field] = target
		}
		for name, value := range entries {
			if _, listed := allowlist[name]; !listed {
				log.Fatalf("overrides %s: model %q is not in %s", field, name, *modelsPath)
			}
			target[name] = value
		}
	}
	priced := modelNames(data)
	var uncovered []string
	for name := range allowlist {
		if _, ok := priced[name]; !ok {
			uncovered = append(uncovered, name)
		}
	}
	sort.Strings(uncovered)
	fmt.Printf("allowlisted %d models, priced %d, uncovered %d: %v\n", len(allowlist), len(priced), len(uncovered), uncovered)

	// struct keeps the summary fields ahead of the large data map
	payload, err := json.MarshalIndent(struct {
		Success     bool           `json:"success"`
		GeneratedAt string         `json:"generated_at"`
		ModelCount  int            `json:"model_count"`
		Data        map[string]any `json:"data"`
	}{true, time.Now().UTC().Format(time.RFC3339), len(priced), data}, "", "  ")
	if err != nil {
		log.Fatalf("marshal output: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(*out, append(payload, '\n'), 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
	fmt.Printf("wrote %s (%d models, factor %v)\n", *out, len(modelNames(data)), *factor)
}

func fetchRatioConfig(url string) (map[string]any, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", response.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Success *bool          `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if envelope.Success != nil && !*envelope.Success {
		return nil, fmt.Errorf("upstream returned success=false")
	}
	if envelope.Data == nil {
		return nil, fmt.Errorf("no data field in upstream response")
	}
	return envelope.Data, nil
}

// loadAllowlist reads a JSON array of model names.
func loadAllowlist(path string) (map[string]struct{}, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("empty allowlist")
	}
	allowlist := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowlist[name] = struct{}{}
	}
	return allowlist, nil
}

func loadOverrides(path string, cnyRate float64) (map[string]map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var full map[string]json.RawMessage
	if err := json.Unmarshal(raw, &full); err != nil {
		return nil, err
	}
	fields := make(map[string]map[string]any)
	if err := mergeOverrideFields(fields, full, 1); err != nil {
		return nil, err
	}
	if rawCNY, ok := full["cny"]; ok {
		var cny map[string]json.RawMessage
		if err := json.Unmarshal(rawCNY, &cny); err != nil {
			return nil, fmt.Errorf("cny: %w", err)
		}
		if err := mergeOverrideFields(fields, cny, 1/cnyRate); err != nil {
			return nil, fmt.Errorf("cny: %w", err)
		}
	}
	return fields, nil
}

// mergeOverrideFields adds one overrides section to fields, scaling its
// money values (model_ratio, model_price, billing_expr prices) by factor.
// A model priced in both the accounting-USD and the CNY section is
// rejected: two sources for one price is always a mistake.
func mergeOverrideFields(fields map[string]map[string]any, section map[string]json.RawMessage, factor float64) error {
	for _, field := range syncFields {
		raw, ok := section[field]
		if !ok {
			continue
		}
		entries := make(map[string]any)
		if err := json.Unmarshal(raw, &entries); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		if len(entries) == 0 {
			continue
		}
		target := fields[field]
		if target == nil {
			target = make(map[string]any)
			fields[field] = target
		}
		for name, value := range entries {
			if _, dup := target[name]; dup {
				return fmt.Errorf("%s: model %q priced in both overrides sections", field, name)
			}
			if factor != 1 {
				switch {
				case field == "billing_expr":
					expression, ok := value.(string)
					if !ok {
						return fmt.Errorf("%s: model %q is not a string", field, name)
					}
					scaled, err := ScaleExprPrices(expression, factor)
					if err != nil {
						return fmt.Errorf("%s: model %q: %w", field, name, err)
					}
					value = scaled
				case slices.Contains(moneyFields, field):
					number, ok := value.(float64)
					if !ok {
						return fmt.Errorf("%s: model %q is not a number", field, name)
					}
					value = math.Round(number*factor*1e6) / 1e6
				}
			}
			target[name] = value
		}
	}
	return nil
}

func modelNames(data map[string]any) map[string]struct{} {
	names := make(map[string]struct{})
	for _, field := range syncFields {
		if entries, ok := data[field].(map[string]any); ok {
			for name := range entries {
				names[name] = struct{}{}
			}
		}
	}
	return names
}
