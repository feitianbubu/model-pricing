package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScaleExprPrices(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want string
	}{
		{"flat token prices", `tier("standard", p * 0.3 + c * 1.2)`, `tier("standard", p * 0.48 + c * 1.92)`},
		{"len tiers keep thresholds", `len <= 272000 ? tier("0_272k", p * 2 + cr * 0.2 + cc * 2.5 + c * 12) : tier("272k_plus", p * 4 + cr * 0.4 + cc * 5 + c * 18)`, `len <= 272000 ? tier("0_272k", p * 3.2 + cr * 0.32 + cc * 4 + c * 19.2) : tier("272k_plus", p * 6.4 + cr * 0.64 + cc * 8 + c * 28.8)`},
		{"time windows keep probes", `weekday("UTC") >= 1 && weekday("UTC") <= 5 ? tier("peak", p * 0.3 + cr * 0.006 + c * 1.2) : tier("off_peak", p * 0.15 + c * 0.6)`, `weekday("UTC") >= 1 && weekday("UTC") <= 5 ? tier("peak", p * 0.48 + cr * 0.0096 + c * 1.92) : tier("off_peak", p * 0.24 + c * 0.96)`},
		{"request rule multiplier unchanged", `(tier("base", p * 5 + c * 25)) * (header("anthropic-beta") == "fast-mode" ? 6 : 1)`, `(tier("base", p * 8 + c * 40)) * (header("anthropic-beta") == "fast-mode" ? 6 : 1)`},
		{"fixed amount scales", `len <= 32000 ? tier("short", fixed(0.01)) : tier("long", p * 2 + c * 8)`, `len <= 32000 ? tier("short", fixed(0.016)) : tier("long", p * 3.2 + c * 12.8)`},
		{"task constant and usage terms", `tier("base", 0.1 + u("seconds") * 0.4 + u("clips") * 0.05)`, `tier("base", 0.16 + u("seconds") * 0.64 + u("clips") * 0.08)`},
		{"token overlay keeps divisor", `tier("base", u("tokens") * 9.8 / 1000000)`, `tier("base", u("tokens") * 15.68 / 1000000)`},
		{"image count multiplier unchanged", `tier("image", fixed(0.04)) * image_count`, `tier("image", fixed(0.064)) * image_count`},
		{"wrapped quantity factor", `tier("audio", max(len - ai, 0) * 0.43 + ai * 3.81 + c * 3.06)`, `tier("audio", max(len - ai, 0) * 0.688 + ai * 6.096 + c * 4.896)`},
		{"single scale per multiply chain", `tier("base", p * 2 * 0.9)`, `tier("base", p * 3.2 * 0.9)`},
		{"zero prices stay zero", `tier("standard", p * 0 + c * 0)`, `tier("standard", p * 0 + c * 0)`},
		{"version prefix preserved", `v1:tier("base", p * 2)`, `v1:tier("base", p * 3.2)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scaled, err := ScaleExprPrices(tc.expr, 1.6)
			require.NoError(t, err)
			assert.Equal(t, tc.want, scaled)
		})
	}
	t.Run("factor one returns input", func(t *testing.T) {
		scaled, err := ScaleExprPrices(`tier("base", p * 2)`, 1)
		require.NoError(t, err)
		assert.Equal(t, `tier("base", p * 2)`, scaled)
	})
	t.Run("unparsable expression errors", func(t *testing.T) {
		_, err := ScaleExprPrices(`tier("base", p * `, 1.6)
		require.Error(t, err)
	})
}

func TestExprHasOnlyZeroPrices(t *testing.T) {
	assert.True(t, ExprHasOnlyZeroPrices(`tier("standard", p * 0 + c * 0)`))
	assert.False(t, ExprHasOnlyZeroPrices(`tier("standard", p * 0 + c * 1.2)`))
	assert.False(t, ExprHasOnlyZeroPrices(`tier("base", fixed(0.04)) * image_count`))
	assert.False(t, ExprHasOnlyZeroPrices(`tier("base", p)`))
	assert.False(t, ExprHasOnlyZeroPrices(`broken (`))
}

func TestScaleExprPricesMultibyteTierName(t *testing.T) {
	// tier names with non-ASCII runes must not shift the literal offsets
	got, err := ScaleExprPrices(`u("r") == "480p" ? tier("480p·none", u("tokens") * 23 / 1000000) : tier("4k·video", u("tokens") * 14 / 1000000)`, 0.2)
	if err != nil {
		t.Fatal(err)
	}
	want := `u("r") == "480p" ? tier("480p·none", u("tokens") * 4.6 / 1000000) : tier("4k·video", u("tokens") * 2.8 / 1000000)`
	if got != want {
		t.Fatalf("got %s", got)
	}
}

func TestMergeOverrideFieldsCNY(t *testing.T) {
	fields := map[string]map[string]any{}
	usd := map[string]json.RawMessage{"model_ratio": json.RawMessage(`{"a": 1}`)}
	cny := map[string]json.RawMessage{
		"billing_expr": json.RawMessage(`{"b": "tier(\"s\", p * 8 + c * 28 + cr * 0.23) * (hour(\"UTC\") >= 9 ? 2 : 1)"}`),
		"model_price":  json.RawMessage(`{"c": 0.6}`),
	}
	if err := mergeOverrideFields(fields, usd, 1); err != nil {
		t.Fatal(err)
	}
	if err := mergeOverrideFields(fields, cny, 0.2); err != nil {
		t.Fatal(err)
	}
	if got := fields["billing_expr"]["b"]; got != `tier("s", p * 1.6 + c * 5.6 + cr * 0.046) * (hour("UTC") >= 9 ? 2 : 1)` {
		t.Fatalf("cny expr not divided by 5: %v", got)
	}
	if got := fields["model_price"]["c"]; got != 0.12 {
		t.Fatalf("cny price not divided by 5: %v", got)
	}
	dup := map[string]json.RawMessage{"model_ratio": json.RawMessage(`{"a": 5}`)}
	if err := mergeOverrideFields(fields, dup, 0.2); err == nil {
		t.Fatal("model priced in both sections must be rejected")
	}
}
