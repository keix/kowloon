package milvus

import (
	"strings"
	"testing"

	"github.com/keix/kowloon"
)

func TestBuildExpr_RecordTypeOnly(t *testing.T) {
	got := buildExpr(kowloon.SearchRequest{RecordType: kowloon.RecordTypeTransaction})
	if got != `record_type == "transaction"` {
		t.Errorf("expr=%q", got)
	}
}

func TestBuildExpr_ScalarFiltersStayScalar(t *testing.T) {
	expr := buildExpr(kowloon.SearchRequest{
		RecordType: kowloon.RecordTypeTransaction,
		Filters: map[string]string{
			"tenant_id":  "keix",
			"year_month": "2026-06",
		},
	})
	// Map iteration order is not deterministic; assert on inclusion.
	for _, want := range []string{
		`record_type == "transaction"`,
		`tenant_id == "keix"`,
		`year_month == "2026-06"`,
	} {
		if !strings.Contains(expr, want) {
			t.Errorf("expr %q missing %q", expr, want)
		}
	}
}

func TestBuildExpr_UnknownFilterFallsThroughToJSON(t *testing.T) {
	expr := buildExpr(kowloon.SearchRequest{
		Filters: map[string]string{"merchant": "FamilyMart"},
	})
	if !strings.Contains(expr, `metadata["merchant"] == "FamilyMart"`) {
		t.Errorf("expr=%q, want JSON path filter", expr)
	}
}

func TestBuildExpr_Empty(t *testing.T) {
	if got := buildExpr(kowloon.SearchRequest{}); got != "" {
		t.Errorf("expr=%q, want empty", got)
	}
}
