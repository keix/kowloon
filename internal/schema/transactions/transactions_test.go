package transactions

import (
	"fmt"
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
				"category": "convenience_store",
				"country": "MY"
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
	// 2 transactions + 1 document summary.
	if len(records) != 3 {
		t.Fatalf("records=%d, want 3", len(records))
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
		"country":          "MY",
		"document_type":    "credit_card_statement",
		"issuer":           "smbc",
		"card_name":        "platinum_preferred",
		"import_batch_id":  "2026-06-30-initial",
	}
	for k, v := range wantMeta {
		if got := r0.Metadata[k]; got != v {
			t.Errorf("metadata[%q]=%q, want %q", k, got, v)
		}
	}

	// Semantic identity in, filterable noise out.
	for _, want := range []string{"FamilyMart KLCC", "convenience_store", "Malaysia", "paid in MYR"} {
		if !strings.Contains(r0.Text, want) {
			t.Errorf("text missing %q: %q", want, r0.Text)
		}
	}
	for _, absent := range []string{"2026-06-12", "620", "smbc", "platinum_preferred"} {
		if strings.Contains(r0.Text, absent) {
			t.Errorf("text should not contain %q: %q", absent, r0.Text)
		}
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
	if r1.Text != "Petronas" {
		t.Errorf("minimal row text=%q, want just the merchant", r1.Text)
	}
}

func TestConvert_Summary(t *testing.T) {
	raw := []byte(`{
		"document_type": "credit_card_statement",
		"fields": {"issuer": "smbc", "card_name": "platinum_preferred", "statement_date": "2026-07-10"},
		"transactions": [
			{"date": "2026-06-12", "merchant": "GRAB* A-123", "merchant_normalized": "Grab", "amount": "980", "country": "MY"},
			{"date": "2026-06-13", "merchant": "grab* B-456", "merchant_normalized": "Grab", "amount": "1200", "country": "MY"},
			{"date": "2026-06-20", "merchant": "スターバックスコーヒージャパン", "merchant_normalized": "Starbucks", "amount": "680", "country": "JP"}
		]
	}`)

	records, err := (Schema{}).Convert(raw, kowloon.IndexResultRequest{JobID: "job_7", TenantID: "keix"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 {
		t.Fatalf("records=%d, want 4", len(records))
	}

	s := records[3]
	if s.ID != "job_7:summary" {
		t.Errorf("ID=%q", s.ID)
	}
	if s.RecordType != kowloon.RecordTypeSummary {
		t.Errorf("RecordType=%q", s.RecordType)
	}
	if s.SourceIndex != -1 {
		t.Errorf("SourceIndex=%d, want -1", s.SourceIndex)
	}

	for _, want := range []string{
		"credit card statement",
		"smbc platinum_preferred",
		"2026-07",
		"3 transactions",
		"merchants: Grab, Starbucks",
		"countries: Malaysia, Japan",
	} {
		if !strings.Contains(s.Text, want) {
			t.Errorf("summary text missing %q: %q", want, s.Text)
		}
	}

	wantMeta := map[string]string{
		"tenant_id":         "keix",
		"document_type":     "credit_card_statement",
		"issuer":            "smbc",
		"card_name":         "platinum_preferred",
		"year_month":        "2026-07",
		"transaction_count": "3",
	}
	for k, v := range wantMeta {
		if got := s.Metadata[k]; got != v {
			t.Errorf("summary metadata[%q]=%q, want %q", k, got, v)
		}
	}
}

func TestConvert_SummaryYearMonthFallsBackToRows(t *testing.T) {
	raw := []byte(`{
		"document_type": "receipt",
		"transactions": [{"date": "2026/06/15", "merchant": "Petronas", "amount": "1500"}]
	}`)
	records, err := (Schema{}).Convert(raw, kowloon.IndexResultRequest{JobID: "j"})
	if err != nil {
		t.Fatal(err)
	}
	s := records[len(records)-1]
	if s.RecordType != kowloon.RecordTypeSummary {
		t.Fatalf("last record=%q, want summary", s.RecordType)
	}
	if s.Metadata["year_month"] != "2026-06" {
		t.Errorf("year_month=%q, want 2026-06 (from first row)", s.Metadata["year_month"])
	}
}

func TestEmbedText_MerchantNormalized(t *testing.T) {
	cases := []struct {
		tx   transaction
		want string
	}{
		{
			transaction{Merchant: "スターバックスコーヒージャパン", MerchantNormalized: "Starbucks", Category: "cafe", Country: "JP"},
			"Starbucks (スターバックスコーヒージャパン) | cafe | Japan",
		},
		{
			transaction{Merchant: "Starbucks", MerchantNormalized: "Starbucks"},
			"Starbucks",
		},
		{
			transaction{Merchant: "GRAB* A-123", MerchantNormalized: "Grab", Country: "MY", ForeignCurrency: "MYR"},
			"Grab (GRAB* A-123) | Malaysia | paid in MYR",
		},
		{
			transaction{Merchant: "Unknown Shop", Description: "coffee beans"},
			"Unknown Shop | coffee beans",
		},
	}
	for _, c := range cases {
		if got := embedText(c.tx); got != c.want {
			t.Errorf("embedText(%+v)=%q, want %q", c.tx, got, c.want)
		}
	}
}

func TestCountryName(t *testing.T) {
	cases := map[string]string{
		"JP": "Japan",
		"my": "Malaysia",
		"SG": "Singapore",
		"TH": "Thailand",
		"US": "United States",
		"FR": "FR",
		"":   "",
	}
	for code, want := range cases {
		if got := countryName(code); got != want {
			t.Errorf("countryName(%q)=%q, want %q", code, got, want)
		}
	}
}

func TestSummaryMerchants_Cap(t *testing.T) {
	txs := make([]transaction, 0, maxSummaryMerchants+5)
	for i := 0; i < maxSummaryMerchants+5; i++ {
		txs = append(txs, transaction{Merchant: fmt.Sprintf("Shop %02d", i)})
	}
	if got := len(summaryMerchants(txs)); got != maxSummaryMerchants {
		t.Errorf("len=%d, want %d", got, maxSummaryMerchants)
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
		t.Errorf("records=%d, want 0 (no summary for an empty document)", len(records))
	}
}
