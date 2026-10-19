package pipeline

import (
	"testing"

	"ocrgen/internal/models"
)

func TestNormalizeAndConsensusAndValidate(t *testing.T) {
	if got := NormalizeDate("May 14th, 2026"); got != "2026-05-14" {
		t.Fatalf("NormalizeDate failed: got %s", got)
	}
	if got := NormalizeVendorName("Acme Cloud Services, Inc."); got != "acme cloud services" {
		t.Fatalf("NormalizeVendorName failed: got '%s'", got)
	}
	if got := NormalizeInvoiceNumber("INV # 2026-0042"); got != "20260042" {
		t.Fatalf("NormalizeInvoiceNumber failed: got '%s'", got)
	}

	engA := &models.ExtractedInvoice{
		VendorName:    "Acme Cloud Services, Inc.",
		VendorTaxID:   "US-99-1234567",
		InvoiceNumber: "INV-2026-0042",
		InvoiceDate:   "2026-05-14",
		DueDate:       "2026-06-14",
		Currency:      "$",
		Subtotal:      1000.00,
		TaxTotal:      80.00,
		Total:         1080.00,
		LineItems: []models.RawLineItem{
			{Description: "GPU Cluster Node Rental", Quantity: 2, UnitPrice: 500.00, Amount: 1000.00},
		},
	}

	engB := &models.ExtractedInvoice{
		VendorName:    "ACME CLOUD SERVICES INC",
		VendorTaxID:   "US991234567",
		InvoiceNumber: "Invoice #2026-0042",
		InvoiceDate:   "May 14, 2026",
		DueDate:       "June 14, 2026",
		Currency:      "USD",
		Subtotal:      1000.00,
		TaxTotal:      80.00,
		Total:         1080.00,
		LineItems: []models.RawLineItem{
			{Description: "GPU Cluster Node Rental", Quantity: 2, UnitPrice: 500.00, Amount: 1000.00},
		},
	}

	cons := Reconcile(engA, engB, DefaultConsensusConfig())
	if cons.AgreementRate < 0.99 {
		t.Fatalf("expected 100%% agreement after normalization, got %.2f", cons.AgreementRate)
	}

	parseRes := &models.DocumentParseResult{
		Markdown: "Acme Cloud Services, Inc.\nTax ID: US-99-1234567\nInvoice #2026-0042\nDate: 2026-05-14\nDue: 2026-06-14\nGPU Cluster Node Rental 2 500.00 1000.00\nSubtotal: 1000.00\nTax: 80.00\nTotal: USD 1080.00",
		Blocks: []models.ParseBlock{
			{Page: 1, BlockType: "header", Text: "Acme Cloud Services, Inc. Invoice #2026-0042", BBox: models.BoundingBox{Page: 1, X1: 40, Y1: 40, X2: 500, Y2: 90}},
			{Page: 1, BlockType: "text", Text: "Date: 2026-05-14 Due: 2026-06-14 Tax ID: US-99-1234567", BBox: models.BoundingBox{Page: 1, X1: 40, Y1: 100, X2: 500, Y2: 140}},
			{Page: 1, BlockType: "table", Text: "GPU Cluster Node Rental | 2 | 500.00 | 1000.00", BBox: models.BoundingBox{Page: 1, X1: 40, Y1: 200, X2: 700, Y2: 260}},
			{Page: 1, BlockType: "text", Text: "Subtotal: 1000.00 Tax: 80.00 Total: USD 1080.00", BBox: models.BoundingBox{Page: 1, X1: 400, Y1: 300, X2: 700, Y2: 380}},
		},
	}

	fields, lines, valReport, status := ValidateAndGround(cons.Reconciled, cons.Fields, cons.LineItems, parseRes, 0.02)
	if status != models.InvoiceAutoAccepted {
		t.Fatalf("expected auto_accepted status, got %s (report=%+v)", status, valReport)
	}
	if !valReport.ArithmeticPassed || !valReport.AllCriticalGrounded {
		t.Fatalf("expected arithmetic & grounding to pass: %+v", valReport)
	}
	if len(fields) == 0 || len(lines) != 1 {
		t.Fatalf("unexpected fields/lines count")
	}
}
