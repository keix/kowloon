// Package transactions converts the transactions.v1 archive shape — the
// output of Lady Glass's enrich_transactions stage, flattened by
// ArchiveResult — into Kowloon records.
//
// One transaction row maps to one RecordTypeTransaction record, and the
// document as a whole maps to one RecordTypeSummary record. The embed
// text carries only the fields with semantic identity (merchant,
// description, category, country, foreign settlement); dates and
// amounts stay out of the text and live in metadata, where filtering
// already handles them. Keeping them out of the text stops every row
// from being unique, which is what lets the embedding cache collapse
// the "same merchant every month" shape of card statements.
//
// Source-fidelity strings stay verbatim in metadata; the converter does
// not parse amounts or normalise merchants. That lets Lady Glass keep
// using the metadata directly for sum / group-by without round-tripping
// through the embedder's lossy text form. The one deliberate exception
// is country: the ISO code is kept verbatim in metadata, but the embed
// text carries the English name ("Japan", not "JP") because two-letter
// codes are weak embedding signal.
package transactions

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/keix/kowloon"
)

// SchemaVersion is the schema_version this converter handles.
const SchemaVersion = "transactions.v1"

// revision is the converter's output revision, fed into the idempotency
// key (see schema.Schema.Revision). Bump it whenever Convert changes the
// records or embed text emitted for unchanged input, so a re-index of
// already-seen jobs re-runs instead of being skipped as a duplicate.
//
//	"1" — original: embed text was "date merchant amount currency ..."
//	"2" — embed text carries semantic identity only (merchant, desc,
//	      category, country), plus a per-document summary record.
const revision = "2"

// maxSummaryMerchants bounds the merchant list on the summary record so
// a statement with hundreds of distinct merchants cannot blow up the
// embed text.
const maxSummaryMerchants = 20

type Schema struct{}

func New() *Schema { return &Schema{} }

// Revision implements schema.Schema.
func (Schema) Revision() string { return revision }

type page struct {
	Text         string         `json:"text"`
	DocumentType string         `json:"document_type"`
	Fields       map[string]any `json:"fields,omitempty"`
	Transactions []transaction  `json:"transactions"`
}

type transaction struct {
	Date               string `json:"date"`
	Merchant           string `json:"merchant"`
	MerchantNormalized string `json:"merchant_normalized,omitempty"`
	Description        string `json:"description,omitempty"`
	Amount             string `json:"amount"`
	Currency           string `json:"currency,omitempty"`
	ForeignAmount      string `json:"foreign_amount,omitempty"`
	ForeignCurrency    string `json:"foreign_currency,omitempty"`
	Category           string `json:"category,omitempty"`
	Country            string `json:"country,omitempty"`
}

func (Schema) Convert(raw []byte, req kowloon.IndexResultRequest) ([]kowloon.Record, error) {
	var p page
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode transactions.v1: %w", err)
	}

	issuer := stringField(p.Fields, "issuer", "issuer_name")
	cardName := stringField(p.Fields, "card_name")

	records := make([]kowloon.Record, 0, len(p.Transactions)+1)
	for i, tx := range p.Transactions {
		records = append(records, buildRecord(req, p.DocumentType, tx, issuer, cardName, i))
	}
	if len(p.Transactions) > 0 {
		records = append(records, buildSummary(req, p, issuer, cardName))
	}
	return records, nil
}

func buildRecord(req kowloon.IndexResultRequest, documentType string, tx transaction, issuer, cardName string, idx int) kowloon.Record {
	currency := tx.Currency
	if currency == "" {
		currency = "JPY"
	}

	metadata := map[string]string{
		"tenant_id": req.TenantID,
		"date":      tx.Date,
		"merchant":  tx.Merchant,
		"amount":    tx.Amount,
		"currency":  currency,
	}
	if documentType != "" {
		metadata["document_type"] = documentType
	}
	if tx.MerchantNormalized != "" {
		metadata["merchant_normalized"] = tx.MerchantNormalized
	}
	if tx.Country != "" {
		metadata["country"] = tx.Country
	}
	if tx.ForeignAmount != "" {
		metadata["foreign_amount"] = tx.ForeignAmount
		metadata["foreign_currency"] = tx.ForeignCurrency
	}
	if tx.Category != "" {
		metadata["category"] = tx.Category
	}
	if issuer != "" {
		metadata["issuer"] = issuer
	}
	if cardName != "" {
		metadata["card_name"] = cardName
	}
	if req.ImportBatchID != "" {
		metadata["import_batch_id"] = req.ImportBatchID
	}
	if ym := yearMonth(tx.Date); ym != "" {
		metadata["year_month"] = ym
	}

	return kowloon.Record{
		ID:          fmt.Sprintf("%s:tx:%d", req.JobID, idx),
		RecordType:  kowloon.RecordTypeTransaction,
		Text:        embedText(tx),
		SourceURI:   req.ResultURI,
		SourceIndex: idx,
		Metadata:    metadata,
	}
}

// embedText builds the string handed to the embedding provider. Issuer
// and card name are deliberately absent: they are identical on every
// row of a job, so they would pull all of a statement's vectors in one
// direction while adding nothing that a metadata filter cannot do.
func embedText(tx transaction) string {
	parts := make([]string, 0, 5)
	if m := merchantPart(tx); m != "" {
		parts = append(parts, m)
	}
	if tx.Description != "" {
		parts = append(parts, tx.Description)
	}
	if tx.Category != "" {
		parts = append(parts, tx.Category)
	}
	if name := countryName(tx.Country); name != "" {
		parts = append(parts, name)
	}
	if tx.ForeignCurrency != "" {
		parts = append(parts, "paid in "+tx.ForeignCurrency)
	}
	return strings.Join(parts, " | ")
}

// merchantPart combines the canonical and verbatim merchant spellings.
// Both matter: the canonical name anchors the vector for grouping-style
// queries, the verbatim OCR string is what /v1/resolve/merchant needs
// to match raw variants against. When they coincide (or no canonical
// exists) one copy is enough.
func merchantPart(tx transaction) string {
	norm := strings.TrimSpace(tx.MerchantNormalized)
	raw := strings.TrimSpace(tx.Merchant)
	switch {
	case norm == "":
		return raw
	case raw == "" || strings.EqualFold(norm, raw):
		return norm
	default:
		return norm + " (" + raw + ")"
	}
}

// countryName expands the ISO 3166-1 alpha-2 codes enrich_transactions
// emits into English names — two-letter codes carry little embedding
// signal. A code outside the table passes through verbatim rather than
// being dropped, so a new enrich country still contributes something
// until this table catches up.
func countryName(code string) string {
	c := strings.ToUpper(strings.TrimSpace(code))
	switch c {
	case "":
		return ""
	case "JP":
		return "Japan"
	case "MY":
		return "Malaysia"
	case "SG":
		return "Singapore"
	case "TH":
		return "Thailand"
	case "US":
		return "United States"
	default:
		return c
	}
}

// buildSummary emits the one document-level record for a job. Its text
// is what "that statement with the Malaysia trip" style queries land
// on; per-row detail stays on the transaction records. Totals are
// deliberately absent — computing one would mean parsing amount
// strings, which this converter does not do (Lady Glass owns
// aggregation against the S3 source of truth).
func buildSummary(req kowloon.IndexResultRequest, p page, issuer, cardName string) kowloon.Record {
	parts := make([]string, 0, 6)
	if p.DocumentType != "" {
		parts = append(parts, strings.ReplaceAll(p.DocumentType, "_", " "))
	}
	card := strings.TrimSpace(strings.TrimSpace(issuer) + " " + strings.TrimSpace(cardName))
	if card != "" {
		parts = append(parts, card)
	}
	ym := summaryYearMonth(p)
	if ym != "" {
		parts = append(parts, ym)
	}
	parts = append(parts, fmt.Sprintf("%d transactions", len(p.Transactions)))
	if merchants := summaryMerchants(p.Transactions); len(merchants) > 0 {
		parts = append(parts, "merchants: "+strings.Join(merchants, ", "))
	}
	if countries := summaryCountries(p.Transactions); len(countries) > 0 {
		parts = append(parts, "countries: "+strings.Join(countries, ", "))
	}

	metadata := map[string]string{
		"tenant_id":         req.TenantID,
		"transaction_count": strconv.Itoa(len(p.Transactions)),
	}
	if p.DocumentType != "" {
		metadata["document_type"] = p.DocumentType
	}
	if issuer != "" {
		metadata["issuer"] = issuer
	}
	if cardName != "" {
		metadata["card_name"] = cardName
	}
	if ym != "" {
		metadata["year_month"] = ym
	}
	if req.ImportBatchID != "" {
		metadata["import_batch_id"] = req.ImportBatchID
	}

	return kowloon.Record{
		ID:         req.JobID + ":summary",
		RecordType: kowloon.RecordTypeSummary,
		Text:       strings.Join(parts, " | "),
		SourceURI:  req.ResultURI,
		// -1 marks "the whole document" — transaction records own the
		// non-negative indices into the source rows.
		SourceIndex: -1,
		Metadata:    metadata,
	}
}

// summaryYearMonth prefers the statement_date header field and falls
// back to the first transaction that yields a parseable date, so a
// statement whose header OCR failed still lands in a month partition.
func summaryYearMonth(p page) string {
	if ym := yearMonth(stringField(p.Fields, "statement_date")); ym != "" {
		return ym
	}
	for _, tx := range p.Transactions {
		if ym := yearMonth(tx.Date); ym != "" {
			return ym
		}
	}
	return ""
}

// summaryMerchants lists distinct merchants in row order, preferring
// the canonical spelling, capped at maxSummaryMerchants. Dedupe is
// case-insensitive so "GRAB" and "Grab" count once.
func summaryMerchants(txs []transaction) []string {
	seen := make(map[string]struct{}, len(txs))
	var out []string
	for _, tx := range txs {
		name := strings.TrimSpace(tx.MerchantNormalized)
		if name == "" {
			name = strings.TrimSpace(tx.Merchant)
		}
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
		if len(out) == maxSummaryMerchants {
			break
		}
	}
	return out
}

// summaryCountries lists distinct countries in row order, expanded to
// English names with the same table the transaction text uses.
func summaryCountries(txs []transaction) []string {
	seen := make(map[string]struct{}, 4)
	var out []string
	for _, tx := range txs {
		name := countryName(tx.Country)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func stringField(fields map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := fields[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// yearMonth extracts "YYYY-MM" from the date formats Lady Glass
// commonly emits across issuers: ISO ("2026-06-12"), Japanese slash
// ("2026/06/12"), and the credit-card-statement two-digit-year form
// ("26/06/12"). Two-digit years are assumed to be 20YY — Lady Glass's
// data horizon does not span centuries. Unrecognised formats return
// "" — missing metadata is preferred over a guess, since downstream
// filters key on this directly.
func yearMonth(date string) string {
	switch {
	case len(date) >= 7 && date[4] == '-':
		return date[:7]
	case len(date) >= 7 && date[4] == '/':
		return date[:4] + "-" + date[5:7]
	case len(date) >= 8 && date[2] == '/' && date[5] == '/':
		return "20" + date[:2] + "-" + date[3:5]
	}
	return ""
}
