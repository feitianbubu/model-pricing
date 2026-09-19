// model-pricing regenerates a new-api ratio_config preset in this
// deployment's accounting unit (USDExchangeRate=5 as a bookkeeping unit,
// real rate REAL_USD_EXCHANGE_RATE=8): upstream real-USD prices are
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
	"sort"
	"strings"
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

type overrides struct {
	// Exclude drops upstream models entirely (zero-price junk entries or
	// models this deployment never serves).
	Exclude []string                  `json:"exclude"`
	Fields  map[string]map[string]any `json:"-"`
}

func main() {
	source := flag.String("source", defaultSource, "upstream ratio_config URL quoting real USD")
	factor := flag.Float64("factor", 1.6, "real-USD to accounting-USD multiplier (REAL_USD_EXCHANGE_RATE / USDExchangeRate)")
	overridesPath := flag.String("overrides", "data/overrides.json", "hand-maintained entries in accounting units")
	modelsSource := flag.String("models", "", "deployed model list (URL of /v1/models with MODELS_API_KEY env, or a local JSON file); upstream entries outside it are dropped, overrides always pass")
	out := flag.String("out", "docs/ratio_config.json", "output path served by GitHub Pages")
	flag.Parse()

	data, err := fetchRatioConfig(*source)
	if err != nil {
		log.Fatalf("fetch %s: %v", *source, err)
	}
	if expressions, ok := data["billing_expr"].(map[string]any); ok {
		for name, value := range expressions {
			expression, ok := value.(string)
			if !ok {
				log.Fatalf("model %q: billing_expr is not a string", name)
			}
			scaled, err := ScaleExprPrices(expression, *factor)
			if err != nil {
				log.Fatalf("model %q: scale billing_expr: %v", name, err)
			}
			expressions[name] = scaled
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

	local, err := loadOverrides(*overridesPath)
	if err != nil {
		log.Fatalf("load overrides: %v", err)
	}
	for _, name := range local.Exclude {
		for _, field := range syncFields {
			if entries, ok := data[field].(map[string]any); ok {
				delete(entries, name)
			}
		}
	}

	// The deployed model list keeps the preset strongly consistent with the
	// gateway: upstream models not deployed are dropped; overrides pass the
	// filter so a brand-new model can be priced here before its first sync.
	if *modelsSource != "" {
		deployed, err := loadModelList(*modelsSource)
		if err != nil {
			log.Fatalf("load model list %s: %v", *modelsSource, err)
		}
		for _, field := range syncFields {
			entries, ok := data[field].(map[string]any)
			if !ok {
				continue
			}
			for name := range entries {
				if !deployed[name] {
					delete(entries, name)
				}
			}
		}
		defer func() {
			priced := modelNames(data)
			var uncovered []string
			for name := range deployed {
				if _, ok := priced[name]; !ok {
					uncovered = append(uncovered, name)
				}
			}
			sort.Strings(uncovered)
			fmt.Printf("deployed %d models, priced %d, uncovered %d:\n", len(deployed), len(priced), len(uncovered))
			for _, name := range uncovered {
				fmt.Println("  " + name)
			}
			for field, entries := range local.Fields {
				for name := range entries {
					if !deployed[name] {
						fmt.Printf("warn: override %q (%s) is not in the deployed model list\n", name, field)
					}
				}
			}
		}()
	}

	for field, entries := range local.Fields {
		target, ok := data[field].(map[string]any)
		if !ok {
			target = make(map[string]any)
			data[field] = target
		}
		for name, value := range entries {
			target[name] = value
		}
	}

	payload, err := json.MarshalIndent(map[string]any{
		"success":      true,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"data":         data,
	}, "", "  ")
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

// loadModelList reads the deployed model names from an OpenAI /v1/models
// URL (Authorization from MODELS_API_KEY) or a local JSON file holding
// either the same shape or a plain string array.
func loadModelList(source string) (map[string]bool, error) {
	var raw []byte
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		request, err := http.NewRequest(http.MethodGet, source, nil)
		if err != nil {
			return nil, err
		}
		if key := os.Getenv("MODELS_API_KEY"); key != "" {
			request.Header.Set("Authorization", "Bearer "+key)
		}
		client := &http.Client{Timeout: 60 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("status %s", response.Status)
		}
		raw, err = io.ReadAll(io.LimitReader(response.Body, 32<<20))
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		raw, err = os.ReadFile(source)
		if err != nil {
			return nil, err
		}
	}
	names := make(map[string]bool)
	var plain []string
	if err := json.Unmarshal(raw, &plain); err == nil {
		for _, name := range plain {
			names[name] = true
		}
		return names, nil
	}
	var listing struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil, err
	}
	for _, item := range listing.Data {
		names[item.ID] = true
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("empty model list")
	}
	return names, nil
}

func loadOverrides(path string) (*overrides, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var full map[string]json.RawMessage
	if err := json.Unmarshal(raw, &full); err != nil {
		return nil, err
	}
	result := &overrides{Fields: make(map[string]map[string]any)}
	if exclude, ok := full["exclude"]; ok {
		if err := json.Unmarshal(exclude, &result.Exclude); err != nil {
			return nil, fmt.Errorf("exclude: %w", err)
		}
	}
	for _, field := range syncFields {
		raw, ok := full[field]
		if !ok {
			continue
		}
		entries := make(map[string]any)
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		if len(entries) > 0 {
			result.Fields[field] = entries
		}
	}
	return result, nil
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
