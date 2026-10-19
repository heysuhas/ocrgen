package store

import (
	"testing"

	"ocrgen/internal/models"
)

func TestSQLiteStoreAndFTS(t *testing.T) {
	s, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer s.Close()

	rec := &models.InvoiceRecord{
		ID:             "inv-001",
		Filename:       "acme_001.pdf",
		FileHashSHA256: "sha256_abc",
		PrimaryPHash:   "0123456789abcdef",
		Status:         models.InvoiceNeedsReview,
		OverallConf:    0.88,
		AgreementRate:  0.90,
		ParseResult: &models.DocumentParseResult{
			Markdown:        "Acme Robotics Corp Invoice INV-9001 Quantum Servo Motor",
			LayoutSignature: []float64{0.5, 0.5, 0.5, 0.5},
		},
		Reconciled: models.ExtractedInvoice{
			VendorName:    "Acme Robotics Corp",
			VendorTaxID:   "US-11-223344",
			InvoiceNumber: "INV-9001",
			InvoiceDate:   "2026-05-01",
			Currency:      "USD",
			Subtotal:      500.00,
			TaxTotal:      50.00,
			Total:         550.00,
			BankDetails:   "US00ACME12345",
			LineItems: []models.RawLineItem{
				{Description: "Quantum Servo Motor", Quantity: 1, UnitPrice: 500, Amount: 500},
			},
		},
		Fields: []models.FieldResult{
			{FieldName: "vendor_name", DisplayValue: "Acme Robotics Corp", NormalizedValue: "acme robotics", Status: models.StatusVerified, Confidence: 0.98, Critical: true},
		},
	}

	if err := s.SaveInvoice(rec); err != nil {
		t.Fatalf("SaveInvoice failed: %v", err)
	}

	got, err := s.GetInvoice("inv-001")
	if err != nil {
		t.Fatalf("GetInvoice failed: %v", err)
	}
	if got.Reconciled.VendorName != "Acme Robotics Corp" {
		t.Fatalf("unexpected vendor: %s", got.Reconciled.VendorName)
	}

	searchRes, err := s.SearchInvoices("Quantum Servo")
	if err != nil {
		t.Fatalf("SearchInvoices failed: %v", err)
	}
	if len(searchRes) != 1 || searchRes[0].ID != "inv-001" {
		t.Fatalf("expected 1 FTS5 match, got %d", len(searchRes))
	}

	// Test Review & Adaptive Memory persistence
	corr := got.Reconciled
	corr.Total = 555.00
	updated, err := s.RecordReview(models.ReviewSubmission{
		InvoiceID: "inv-001",
		Reviewer:  "alice",
		Corrected: corr,
		Notes:     "Fixed total",
	})
	if err != nil {
		t.Fatalf("RecordReview failed: %v", err)
	}
	if updated.Status != models.InvoiceReviewed || updated.Reconciled.Total != 555.00 {
		t.Fatalf("unexpected reviewed state: %+v", updated)
	}

	examples, err := s.ListAdaptiveExamples()
	if err != nil || len(examples) != 1 {
		t.Fatalf("expected 1 adaptive example, got %d (err=%v)", len(examples), err)
	}
}
