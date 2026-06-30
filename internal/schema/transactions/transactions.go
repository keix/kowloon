// Package transactions converts the transactions.v1 archive shape — the
// output of Lady Glass's normalize-transactions stage — into Kowloon
// records. One transaction row in the source maps to one record;
// metadata captures the structured fields aggregation will need
// (date, amount, currency, year_month, issuer, import_batch_id).
//
// Source-fidelity strings stay verbatim; the converter does not parse
// amounts or normalise merchants. That lets Lady Glass keep using the
// metadata directly for sum / group-by without round-tripping through
// the embedder's lossy text form.
package transactions

import (
	"encoding/json"
	"fmt"

	"github.com/keix/kowloon"
)

// SchemaVersion is the schema_version this converter handles.
const SchemaVersion = "transactions.v1"

type Schema struct{}

func New() *Schema { return &Schema{} }

type page struct {
	Text         string         `json:"text"`
	DocumentType string         `json:"document_type"`
	Fields       map[string]any `json:"fields,omitempty"`
	Transactions []transaction  `json:"transactions"`
}

type transaction struct {
	Date            string `json:"date"`
	Merchant        string `json:"merchant"`
	Description     string `json:"description,omitempty"`
	Amount          string `json:"amount"`
	Currency        string `json:"currency,omitempty"`
	ForeignAmount   string `json:"foreign_amount,omitempty"`
	ForeignCurrency string `json:"foreign_currency,omitempty"`
	Category        string `json:"category,omitempty"`
}

func (Schema) Convert(raw []byte, req kowloon.IndexResultRequest) ([]kowloon.Record, error) {
	var p page
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode transactions.v1: %w", err)
	}

	issuer := stringField(p.Fields, "issuer", "issuer_name")
	cardName := stringField(p.Fields, "card_name")

	records := make([]kowloon.Record, 0, len(p.Transactions))
	for i, tx := range p.Transactions {
		records = append(records, buildRecord(req, tx, issuer, cardName, i))
	}
	return records, nil
}

func buildRecord(req kowloon.IndexResultRequest, tx transaction, issuer, cardName string, idx int) kowloon.Record {
	currency := tx.Currency
	if currency == "" {
		currency = "JPY"
	}

	text := tx.Date + " " + tx.Merchant + " " + tx.Amount + " " + currency
	if tx.ForeignAmount != "" {
		text += " " + tx.ForeignAmount + " " + tx.ForeignCurrency
	}
	if issuer != "" {
		text += " " + issuer
	}
	if cardName != "" {
		text += " " + cardName
	}
	if tx.Description != "" {
		text += " " + tx.Description
	}

	metadata := map[string]string{
		"tenant_id": req.TenantID,
		"date":      tx.Date,
		"merchant":  tx.Merchant,
		"amount":    tx.Amount,
		"currency":  currency,
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
		Text:        text,
		SourceURI:   req.ResultURI,
		SourceIndex: idx,
		Metadata:    metadata,
	}
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

// yearMonth extracts "YYYY-MM" from the two date formats Lady Glass
// commonly emits ("2026-06-12" and "2026/06/12"). Unrecognised formats
// return "" — missing metadata is preferred over a guess, since
// downstream filters key on this directly.
func yearMonth(date string) string {
	if len(date) < 7 {
		return ""
	}
	switch date[4] {
	case '-':
		return date[:7]
	case '/':
		return date[:4] + "-" + date[5:7]
	}
	return ""
}
