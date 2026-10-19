package memory

import (
	"testing"

	"ocrgen/internal/models"
)

func TestAdaptiveRetriever(t *testing.T) {
	ret := NewRetriever(2, 0.28)
	examples := []models.AdaptiveExample{
		{
			ID:              1,
			InvoiceID:       "inv-old-1",
			VendorNorm:      "stellar photonics",
			HeaderSnippet:   "Stellar Photonics GmbH VAT DE123456789 Munich",
			LayoutSignature: []float64{0.6, 0.4, 0.0, 0.0, 0.3, 0.3, 0.2, 0.2},
			CorrectedJSON: models.ExtractedInvoice{
				VendorName:  "Stellar Photonics GmbH",
				VendorTaxID: "DE123456789",
				Currency:    "EUR",
			},
		},
		{
			ID:              2,
			InvoiceID:       "inv-old-2",
			VendorNorm:      "northwind catering",
			HeaderSnippet:   "Northwind Catering Services Seattle WA",
			LayoutSignature: []float64{0.1, 0.1, 0.7, 0.5, 0.0, 0.0, 0.2, 0.1},
			CorrectedJSON: models.ExtractedInvoice{
				VendorName: "Northwind Catering Services",
				Currency:   "USD",
			},
		},
	}

	incoming := &models.DocumentParseResult{
		Markdown:        "Stellar Photonics GmbH\nVAT: DE123456789\nInvoice #SP-2026-88\nTotal: 1,450.00 EUR",
		LayoutSignature: []float64{0.58, 0.42, 0.0, 0.0, 0.31, 0.29, 0.2, 0.2},
		Blocks: []models.ParseBlock{
			{Page: 1, BlockType: "header", Text: "Stellar Photonics GmbH VAT DE123456789"},
		},
	}

	matches := ret.RetrieveTopK(incoming, examples)
	if len(matches) == 0 {
		t.Fatalf("expected at least 1 retrieved adaptive example")
	}
	if matches[0].VendorNorm != "stellar photonics" {
		t.Fatalf("expected top match to be stellar photonics, got %s", matches[0].VendorNorm)
	}
}
