package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"github.com/shopspring/decimal"
)

// quantityIdents are the billing quantity variables of the new-api v1
// expression grammar: a numeric literal multiplied against a subtree
// containing one is a USD price. Keep in sync with the gateway's
// pkg/billingexpr compile env ("u" is the task usage accessor).
var quantityIdents = map[string]bool{
	"p": true, "c": true, "len": true, "cr": true, "cc": true, "cc1h": true,
	"img": true, "img_cr": true, "img_o": true, "ai": true, "ao": true,
	"image_count": true, "u": true,
}

// ScaleExprPrices multiplies every USD amount in a billing expression by
// factor: tier-body price coefficients, constant addends, and fixed()
// amounts. Tier conditions, tier names, divisors, and request-rule
// multipliers are dimensionless and stay unchanged. Literals are replaced
// in place, so all other source text is preserved byte for byte.
func ScaleExprPrices(exprStr string, factor float64) (string, error) {
	if factor == 1 {
		return exprStr, nil
	}
	body, _ := strings.CutPrefix(exprStr, "v1:")
	prefix, _ := strings.CutSuffix(exprStr, body)
	tree, err := parser.Parse(body)
	if err != nil {
		return "", err
	}
	spans := make(map[int]int)
	collectPriceSpans(tree.Node, false, spans)
	factorDecimal := decimal.NewFromFloat(factor)
	var scaled strings.Builder
	scaled.WriteString(prefix)
	previous := 0
	for _, from := range slices.Sorted(maps.Keys(spans)) {
		to := spans[from]
		literal, err := decimal.NewFromString(body[from:to])
		if err != nil {
			return "", fmt.Errorf("price literal %q: %w", body[from:to], err)
		}
		scaled.WriteString(body[previous:from])
		scaled.WriteString(literal.Mul(factorDecimal).String())
		previous = to
	}
	scaled.WriteString(body[previous:])
	return scaled.String(), nil
}

// ExprHasOnlyZeroPrices reports whether the expression carries price
// literals and every one of them is zero — the shape of upstream
// placeholder entries generated from missing cost data, never a real
// free price.
func ExprHasOnlyZeroPrices(exprStr string) bool {
	body, _ := strings.CutPrefix(exprStr, "v1:")
	tree, err := parser.Parse(body)
	if err != nil {
		return false
	}
	spans := make(map[int]int)
	collectPriceSpans(tree.Node, false, spans)
	if len(spans) == 0 {
		return false
	}
	for from, to := range spans {
		literal, err := decimal.NewFromString(body[from:to])
		if err != nil || !literal.IsZero() {
			return false
		}
	}
	return true
}

// collectPriceSpans records the source spans of USD literals. Money only
// exists inside tier() cost bodies and fixed() amounts; condition subtrees
// are always dimensionless, even when nested in a tier body.
func collectPriceSpans(node ast.Node, inTierBody bool, spans map[int]int) {
	switch n := node.(type) {
	case *ast.CallNode:
		if identifier, ok := n.Callee.(*ast.IdentifierNode); ok {
			switch {
			case identifier.Value == "tier" && len(n.Arguments) == 2:
				collectPriceSpans(n.Arguments[1], true, spans)
				return
			case identifier.Value == "fixed" && len(n.Arguments) == 1:
				markPriceLiteral(n.Arguments[0], spans)
				return
			}
		}
		for _, argument := range n.Arguments {
			collectPriceSpans(argument, inTierBody, spans)
		}
	case *ast.ConditionalNode:
		collectPriceSpans(n.Cond, false, spans)
		collectPriceSpans(n.Exp1, inTierBody, spans)
		collectPriceSpans(n.Exp2, inTierBody, spans)
	case *ast.BinaryNode:
		if inTierBody {
			switch n.Operator {
			case "+", "-":
				for _, side := range []ast.Node{n.Left, n.Right} {
					if !markPriceLiteral(side, spans) {
						collectPriceSpans(side, true, spans)
					}
				}
				return
			case "*", "/":
				if scaleMultiplyChain(n, spans) {
					return
				}
			}
		}
		collectPriceSpans(n.Left, inTierBody, spans)
		collectPriceSpans(n.Right, inTierBody, spans)
	case *ast.UnaryNode:
		collectPriceSpans(n.Node, inTierBody, spans)
	}
}

// scaleMultiplyChain scales exactly one literal factor of a multiplication
// chain that multiplies a quantity, so dimensionless co-factors and nested
// terms cannot be scaled a second time.
func scaleMultiplyChain(root *ast.BinaryNode, spans map[int]int) bool {
	var factors []ast.Node
	var divisors []ast.Node
	var flatten func(node ast.Node)
	flatten = func(node ast.Node) {
		if binary, ok := node.(*ast.BinaryNode); ok {
			switch binary.Operator {
			case "*":
				flatten(binary.Left)
				flatten(binary.Right)
				return
			case "/":
				flatten(binary.Left)
				divisors = append(divisors, binary.Right)
				return
			}
		}
		factors = append(factors, node)
	}
	flatten(root)
	var literal ast.Node
	hasQuantity := false
	for _, factor := range factors {
		if literal == nil && isNumericLiteral(factor) {
			literal = factor
			continue
		}
		if subtreeHasQuantity(factor) {
			hasQuantity = true
		}
	}
	if literal == nil || !hasQuantity {
		return false
	}
	markPriceLiteral(literal, spans)
	for _, factor := range factors {
		if factor != literal {
			collectPriceSpans(factor, false, spans)
		}
	}
	for _, divisor := range divisors {
		collectPriceSpans(divisor, false, spans)
	}
	return true
}

func isNumericLiteral(node ast.Node) bool {
	switch node.(type) {
	case *ast.IntegerNode, *ast.FloatNode:
		return true
	}
	return false
}

func markPriceLiteral(node ast.Node, spans map[int]int) bool {
	if !isNumericLiteral(node) {
		return false
	}
	location := node.Location()
	spans[location.From] = location.To
	return true
}

func subtreeHasQuantity(node ast.Node) bool {
	return ast.Find(node, func(candidate ast.Node) bool {
		identifier, ok := candidate.(*ast.IdentifierNode)
		return ok && quantityIdents[identifier.Value]
	}) != nil
}
