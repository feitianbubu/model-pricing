package main

import (
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
