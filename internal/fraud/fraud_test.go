package fraud

import (
	"math"
	"testing"

	"ocrgen/internal/models"
	"ocrgen/internal/pipeline"
)

func TestFraudAuditModule(t *testing.T) {
	aud := NewAuditor(DefaultConfig())

	baseInv := models.InvoiceRecord{
		ID:             "inv-base",
		Filename:       "base.pdf",
		FileHashSHA256: "hash111",
		PrimaryPHash:   "f0f0f0f0f0f0f0f0",
		Reconciled: models.ExtractedInvoice{
			VendorName:    "Globex Logistics LLC",
			VendorTaxID:   "US-88-776655",
			InvoiceNumber: "GLX-1001",
			InvoiceDate:   "2026-05-04",
			Total:         420.50,
			BankDetails:   "US99GLOBEX0001",
		},
	}
	baseInv.CompositeHash = pipeline.ComputeCompositeHash(baseInv.Reconciled)

	// 1. Test Cold Start (< 20 vendor history should NOT flag outlier)
	shortHist := []float64{400, 410, 420, 405, 415}
	candCold := models.InvoiceRecord{
		ID:             "inv-cold",
		FileHashSHA256: "hash222",
		PrimaryPHash:   "0000000000000000",
		Validation:     models.ValidationReport{ArithmeticPassed: true},
		Reconciled: models.ExtractedInvoice{
			VendorName:    "Globex Logistics LLC",
			VendorTaxID:   "US-88-776655",
			InvoiceNumber: "GLX-1002",
			InvoiceDate:   "2026-05-05",
			Total:         9500.00, // Huge spike, but n=5 < 20 cold start
			BankDetails:   "US99GLOBEX0001",
		},
	}
	flagsCold := aud.AuditInvoice(&candCold, []models.InvoiceRecord{baseInv}, shortHist, []string{"GLX1001"}, "US99GLOBEX0001", "US88776655")
	for _, f := range flagsCold {
		if f.FlagType == "vendor_outlier_z" {
			t.Fatalf("expected cold-start guard to suppress vendor_outlier_z when n < 20")
		}
	}

	// 2. Test Vendor Outlier Z-score when n >= 20 + Bank Change + Weekend Date + Duplicate pHash
	fullHist := make([]float64, 25)
	for i := range fullHist {
		fullHist[i] = 400.0 + float64(i%10)*5.0
	}
	candAnom := models.InvoiceRecord{
		ID:             "inv-anom",
		FileHashSHA256: "hash333",
		PrimaryPHash:   "f0f0f0f0f0f0f0f1", // 1-bit Hamming distance from baseInv
		Validation: models.ValidationReport{
			ArithmeticPassed: false,
			Checks: []models.ArithmeticCheck{
				{CheckName: "subtotal_plus_tax_eq_total", Passed: false, Expected: 1000, Actual: 8500, Difference: 7500, Tolerance: 0.02, Details: "altered total"},
			},
		},
		Reconciled: models.ExtractedInvoice{
			VendorName:    "Globex Logistics LLC",
			VendorTaxID:   "US-88-776655",
			InvoiceNumber: "GLX-1900",
			InvoiceDate:   "2026-05-10", // Sunday
			Total:         8500.00,
			BankDetails:   "CH99HACKER9999", // Changed bank details!
		},
	}

	flags := aud.AuditInvoice(&candAnom, []models.InvoiceRecord{baseInv}, fullHist, []string{"GLX1001"}, "US99GLOBEX0001", "US88776655")
	seen := map[string]bool{}
	for _, f := range flags {
		seen[f.FlagType] = true
	}
	for _, reqType := range []string{"duplicate_phash", "vendor_outlier_z", "rule_bank_change", "rule_math_mismatch", "rule_weekend_date", "rule_seq_gap"} {
		if !seen[reqType] {
			t.Fatalf("expected fraud flag %s to be triggered, got flags=%+v", reqType, flags)
		}
	}

	// 3. Test Batch Benford's Law: natural log-uniform distribution (3 full decades) vs fabricated leading-9 distribution
	var naturalAmts []float64
	for i := 0; i < 120; i++ {
		naturalAmts = append(naturalAmts, math.Pow(10.0, float64(i)/40.0)*10.0)
	}
	repNat := aud.AnalyzeBenford("natural_batch", naturalAmts)
	if !repNat.SufficientData || repNat.Suspicious {
		t.Fatalf("expected natural Benford batch to conform, got %+v", repNat)
	}

	var fakeAmts []float64
	for i := 0; i < 80; i++ {
		fakeAmts = append(fakeAmts, 910.0+float64(i%80))
	}
	repFake := aud.AnalyzeBenford("manipulated_batch", fakeAmts)
	if !repFake.Suspicious || repFake.Conformity != "nonconformity" {
		t.Fatalf("expected manipulated leading-9 batch to fail Benford conformity, got %+v", repFake)
	}
}
