package transactions

import (
	"strings"
	"testing"

	"github.com/keix/kowloon"
)

func TestConvert_Basic(t *testing.T) {
	raw := []byte(`{
		"text": "page",
		"document_type": "credit_card_statement",
		"fields": {"issuer": "smbc", "card_name": "platinum_preferred"},
		"transactions": [
			{
				"date": "2026-06-12",
				"merchant": "FamilyMart KLCC",
				"amount": "620",
				"currency": "JPY",
				"foreign_amount": "18.50",
				"foreign_currency": "MYR",
				"category": "convenience_store"
			},
			{
				"date": "2026/06/15",
				"merchant": "Petronas",
				"amount": "1500"
			}
		]
	}`)

	req := kowloon.IndexResultRequest{
		JobID:         "job_42",
		TenantID:      "keix",
		ResultURI:     "s3://bucket/key.json",
		ResultType:    kowloon.ResultTypeTransactions,
		SchemaVersion: SchemaVersion,
		ImportBatchID: "2026-06-30-initial",
	}

	records, err := (Schema{}).Convert(raw, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records=%d, want 2", len(records))
	}

	r0 := records[0]
	if r0.ID != "job_42:tx:0" {
		t.Errorf("ID=%q", r0.ID)
	}
	if r0.RecordType != kowloon.RecordTypeTransaction {
		t.Errorf("RecordType=%q", r0.RecordType)
	}
	if r0.SourceURI != "s3://bucket/key.json" || r0.SourceIndex != 0 {
		t.Errorf("source=%q[%d]", r0.SourceURI, r0.SourceIndex)
	}

	wantMeta := map[string]string{
		"tenant_id":        "keix",
		"date":             "2026-06-12",
		"year_month":       "2026-06",
		"merchant":         "FamilyMart KLCC",
		"amount":           "620",
		"currency":         "JPY",
		"foreign_amount":   "18.50",
		"foreign_currency": "MYR",
		"category":         "convenience_store",
		"issuer":           "smbc",
		"card_name":        "platinum_preferred",
		"import_batch_id":  "2026-06-30-initial",
	}
	for k, v := range wantMeta {
		if got := r0.Metadata[k]; got != v {
			t.Errorf("metadata[%q]=%q, want %q", k, got, v)
		}
	}

	if !strings.Contains(r0.Text, "FamilyMart KLCC") || !strings.Contains(r0.Text, "MYR") {
		t.Errorf("text missing expected tokens: %q", r0.Text)
	}

	r1 := records[1]
	if r1.Metadata["year_month"] != "2026-06" {
		t.Errorf("year_month (slash format)=%q", r1.Metadata["year_month"])
	}
	if r1.Metadata["currency"] != "JPY" {
		t.Errorf("default currency=%q, want JPY", r1.Metadata["currency"])
	}
	if _, ok := r1.Metadata["foreign_currency"]; ok {
		t.Error("foreign_currency should be absent when foreign_amount empty")
	}
}

func TestConvert_BadJSON(t *testing.T) {
	if _, err := (Schema{}).Convert([]byte(`not json`), kowloon.IndexResultRequest{}); err == nil {
		t.Fatal("want error")
	}
}

func TestYearMonth(t *testing.T) {
	cases := map[string]string{
		"2026-06-12": "2026-06",
		"2026/06/12": "2026-06",
		"26/06/12":   "2026-06",
		"26/06/22":   "2026-06",
		"":           "",
		"26":         "",
		"random":     "",
		"2026年6月12日": "",
	}
	for date, want := range cases {
		t.Run(date, func(t *testing.T) {
			if got := yearMonth(date); got != want {
				t.Errorf("yearMonth(%q)=%q, want %q", date, got, want)
			}
		})
	}
}

func TestConvert_EmptyTransactions(t *testing.T) {
	raw := []byte(`{"text":"","document_type":"other","transactions":[]}`)
	records, err := (Schema{}).Convert(raw, kowloon.IndexResultRequest{JobID: "j"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("records=%d, want 0", len(records))
	}
}
