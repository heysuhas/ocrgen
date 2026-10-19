package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ocrgen/internal/engines"
	"ocrgen/internal/fraud"
	"ocrgen/internal/ingest"
	"ocrgen/internal/memory"
	"ocrgen/internal/models"
	"ocrgen/internal/orchestrator"
	"ocrgen/internal/pipeline"
	"ocrgen/internal/store"
)

type BenchSample struct {
	ID          string                  `json:"id"`
	FilePath    string                  `json:"file_path"`
	Layout      string                  `json:"layout"`
	GroundTruth models.ExtractedInvoice `json:"ground_truth"`
}

type FieldEvalStats struct {
	FieldName      string
	Total          int
	EngineACorrect int
	EngineBCorrect int
	ConsCorrect    int
	VerifiedCount  int
	VerifiedRight  int
	EngineAF1      float64
	EngineBF1      float64
	ConsensusF1    float64
}

type FraudAnomalyCase struct {
	Name         string
	ExpectedFlag string
	ShouldFlag   bool
	Record       models.InvoiceRecord
	Existing     []models.InvoiceRecord
	VendorAmts   []float64
	VendorNums   []string
	KnownBank    string
	KnownTax     string
}

func main() {
	var (
		datasetDir = flag.String("dataset", "./eval/datasets/sample_bench", "Path to benchmark dataset directory")
		reportPath = flag.String("report", "./eval/reports/latest_report.md", "Output Markdown report path")
		hwPath     = flag.String("hw-profile", "./docs/hardware_profile.md", "Output Hardware Profile Markdown path")
		ollamaURL  = flag.String("ollama-url", "http://127.0.0.1:11434", "Ollama API base URL")
		textModel  = flag.String("text-model", "qwen3-vl:4b", "Ollama model for Engine A text extraction")
		vlmModel   = flag.String("vlm-model", "qwen3-vl:4b", "Ollama model for Engine B vision extraction")
		numCtx     = flag.Int("num-ctx", 16384, "Ollama context window")
		paddleURL  = flag.String("paddle-url", "http://127.0.0.1:8091", "PaddleOCR-VL-1.6 worker URL")
		workerPath = flag.String("worker-script", "./worker_paddle/server.py", "Worker script path")
	)
	flag.Parse()

	manifestFile := filepath.Join(*datasetDir, "ground_truth.json")
	rawManifest, err := os.ReadFile(manifestFile)
	if err != nil {
		log.Fatalf("read benchmark manifest %s: %v", manifestFile, err)
	}
	var samples []BenchSample
	if err := json.Unmarshal(rawManifest, &samples); err != nil {
		log.Fatalf("parse manifest: %v", err)
	}

	tmpDir, err := os.MkdirTemp("", "ocrgen_eval_*")
	if err != nil {
		log.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	ing, err := ingest.NewIngestor(ingest.DefaultConfig(filepath.Join(tmpDir, "pages")))
	if err != nil {
		log.Fatalf("init ingestor: %v", err)
	}
	st, err := store.NewStore(filepath.Join(tmpDir, "eval.db"))
	if err != nil {
		log.Fatalf("init store: %v", err)
	}
	defer st.Close()

	parser := engines.NewPaddleHTTPParser(*paddleURL, true, *workerPath, *ollamaURL, *vlmModel)
	defer parser.Close()

	engA := engines.NewOllamaExtractor(engines.OllamaConfig{
		BaseURL:    *ollamaURL,
		Model:      *textModel,
		NumCtx:     *numCtx,
		VisionMode: false,
	})
	engB := engines.NewOllamaExtractor(engines.OllamaConfig{
		BaseURL:    *ollamaURL,
		Model:      *vlmModel,
		NumCtx:     *numCtx,
		VisionMode: true,
	})
	aud := fraud.NewAuditor(fraud.DefaultConfig())
	ret := memory.NewRetriever(2, 0.28)
	coord := orchestrator.NewCoordinator(ing, parser, engA, engB, st, aud, ret)

	fieldNames := []string{
		"vendor_name", "vendor_tax_id", "invoice_number", "invoice_date",
		"due_date", "currency", "subtotal", "tax_total", "total", "bank_details",
	}
	statsMap := make(map[string]*FieldEvalStats)
	for _, fn := range fieldNames {
		statsMap[fn] = &FieldEvalStats{FieldName: fn}
	}

	var (
		totalInvoices   = len(samples)
		autoAcceptedCnt = 0
		needsReviewCnt  = 0
		sumAgreeRate    = 0.0
		sumParseMs      int64
		sumEngAMs       int64
		sumEngBMs       int64
		sumTotalMs      int64
		peakVRAMMiB     int64
	)

	ctx := context.Background()
	log.Printf("=== Running Dual-Engine Benchmark on %d invoices (text=%s, vision=%s, num_ctx=%d) ===", len(samples), *textModel, *vlmModel, *numCtx)

	for _, s := range samples {
		log.Printf("Processing %s (%s)...", s.ID, s.FilePath)
		rec, err := coord.ProcessFile(ctx, s.ID, s.FilePath, orchestrator.ProcessOptions{})
		if err != nil {
			log.Fatalf("process sample %s failed: %v", s.ID, err)
		}
		vram := queryGPUVRAM()
		if vram > peakVRAMMiB {
			peakVRAMMiB = vram
		}
		if rec.Status == models.InvoiceAutoAccepted {
			autoAcceptedCnt++
		} else {
			needsReviewCnt++
		}
		sumAgreeRate += rec.AgreementRate
		sumTotalMs += rec.TotalLatencyMs
		for _, er := range rec.EngineRuns {
			switch er.EngineName {
			case "paddleocr_vl_1_6":
				sumParseMs += er.LatencyMs
			case "engine_a_text_llm":
				sumEngAMs += er.LatencyMs
			case "engine_b_vlm":
				sumEngBMs += er.LatencyMs
			}
		}

		gtMap := normalizeInvoiceFields(s.GroundTruth)
		aMap := normalizeInvoiceFields(*rec.EngineAOutput)
		bMap := normalizeInvoiceFields(*rec.EngineBOutput)
		cMap := normalizeInvoiceFields(rec.Reconciled)

		statusByField := map[string]models.FieldStatus{}
		for _, f := range rec.Fields {
			statusByField[f.FieldName] = f.Status
		}

		for _, fn := range fieldNames {
			stField := statsMap[fn]
			stField.Total++
			gtVal := gtMap[fn]
			aOK := fieldMatches(fn, aMap[fn], gtVal)
			bOK := fieldMatches(fn, bMap[fn], gtVal)
			cOK := fieldMatches(fn, cMap[fn], gtVal)
			if aOK {
				stField.EngineACorrect++
			}
			if bOK {
				stField.EngineBCorrect++
			}
			if cOK {
				stField.ConsCorrect++
			}
			stField.EngineAF1 += tokenF1(aMap[fn], gtVal)
			stField.EngineBF1 += tokenF1(bMap[fn], gtVal)
			stField.ConsensusF1 += tokenF1(cMap[fn], gtVal)
			if statusByField[fn] == models.StatusVerified {
				stField.VerifiedCount++
				if cOK {
					stField.VerifiedRight++
				}
			}
		}
	}

	// Phase 4: Evaluate Fraud Audit Module on Labeled Synthetic Anomalies
	log.Printf("=== Phase 4: Running Statistical Fraud Audit Evaluation ===")
	fraudReportRows, benfordNat, benfordManip := evaluateFraudSuite(aud)

	// Phase 5: Evaluate Adaptive Few-Shot Memory Loop
	log.Printf("=== Phase 5: Evaluating Adaptive Few-Shot Memory Retrieval & Prompt Injection ===")
	adaptiveResult := evaluateAdaptiveLoop(st, ret)

	// Phase 6: Measure Go Backend Memory Footprint
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	goAllocMiB := float64(memStats.Alloc) / (1024 * 1024)
	goSysMiB := float64(memStats.Sys) / (1024 * 1024)
	if v := queryGPUVRAM(); v > peakVRAMMiB {
		peakVRAMMiB = v
	}

	// Build Comprehensive Markdown Report
	reportMD := buildEvaluationMarkdown(
		*textModel,
		*vlmModel,
		*numCtx,
		fieldNames,
		statsMap,
		totalInvoices,
		autoAcceptedCnt,
		needsReviewCnt,
		sumAgreeRate/float64(maxInt(1, totalInvoices)),
		sumParseMs/int64(maxInt(1, totalInvoices)),
		sumEngAMs/int64(maxInt(1, totalInvoices)),
		sumEngBMs/int64(maxInt(1, totalInvoices)),
		sumTotalMs/int64(maxInt(1, totalInvoices)),
		peakVRAMMiB,
		goAllocMiB,
		goSysMiB,
		fraudReportRows,
		benfordNat,
		benfordManip,
		adaptiveResult,
	)

	if err := os.MkdirAll(filepath.Dir(*reportPath), 0o755); err != nil {
		log.Fatalf("mkdir report dir: %v", err)
	}
	if err := os.WriteFile(*reportPath, []byte(reportMD), 0o644); err != nil {
		log.Fatalf("write report: %v", err)
	}
	if err := os.WriteFile(*hwPath, []byte(buildHardwareProfile(*textModel, *vlmModel, *numCtx, peakVRAMMiB, goAllocMiB, goSysMiB, sumParseMs/int64(maxInt(1, totalInvoices)), sumEngAMs/int64(maxInt(1, totalInvoices)), sumEngBMs/int64(maxInt(1, totalInvoices)), sumTotalMs/int64(maxInt(1, totalInvoices)))), 0o644); err != nil {
		log.Fatalf("write hardware profile: %v", err)
	}

	log.Printf("Evaluation complete! Reports written to %s and %s", *reportPath, *hwPath)
}

func normalizeInvoiceFields(inv models.ExtractedInvoice) map[string]string {
	return map[string]string{
		"vendor_name":    pipeline.NormalizeVendorName(inv.VendorName),
		"vendor_tax_id":  pipeline.NormalizeTaxID(inv.VendorTaxID),
		"invoice_number": pipeline.NormalizeInvoiceNumber(inv.InvoiceNumber),
		"invoice_date":   pipeline.NormalizeDate(inv.InvoiceDate),
		"due_date":       pipeline.NormalizeDate(inv.DueDate),
		"currency":       pipeline.NormalizeCurrency(inv.Currency),
		"subtotal":       pipeline.FormatAmount(inv.Subtotal),
		"tax_total":      pipeline.FormatAmount(inv.TaxTotal),
		"total":          pipeline.FormatAmount(inv.Total),
		"bank_details":   pipeline.NormalizeBankDetails(inv.BankDetails),
	}
}

func fieldMatches(fieldName, got, want string) bool {
	if got == want {
		return true
	}
	if fieldName == "vendor_name" {
		return pipeline.StringSimilarity(got, want) >= 0.85
	}
	return false
}

func tokenF1(got, want string) float64 {
	gt, wt := strings.Fields(got), strings.Fields(want)
	if len(gt) == 0 && len(wt) == 0 {
		return 1
	}
	if len(gt) == 0 || len(wt) == 0 {
		return 0
	}
	counts := map[string]int{}
	for _, t := range wt {
		counts[t]++
	}
	matched := 0
	for _, t := range gt {
		if counts[t] > 0 {
			matched++
			counts[t]--
		}
	}
	precision, recall := float64(matched)/float64(len(gt)), float64(matched)/float64(len(wt))
	if precision+recall == 0 {
		return 0
	}
	return 2 * precision * recall / (precision + recall)
}

type fraudEvalRow struct {
	AnomalyType string
	TP          int
	FP          int
	FN          int
	Precision   float64
	Recall      float64
	F1          float64
}

func evaluateFraudSuite(aud *fraud.Auditor) ([]fraudEvalRow, models.BenfordReport, models.BenfordReport) {
	baseRec := models.InvoiceRecord{
		ID:             "clean-001",
		Filename:       "clean_001.pdf",
		FileHashSHA256: "sha_clean_001",
		PrimaryPHash:   "aaaa0000aaaa0000",
		Validation:     models.ValidationReport{ArithmeticPassed: true},
		Reconciled: models.ExtractedInvoice{
			VendorName:    "Acme Cloud Infrastructure Inc",
			VendorTaxID:   "US-94-3321908",
			InvoiceNumber: "INV-2026-1001",
			InvoiceDate:   "2026-05-04",
			Total:         500.00,
			TaxTotal:      40.00,
			BankDetails:   "US88ACME0009876543",
		},
	}
	baseRec.CompositeHash = pipeline.ComputeCompositeHash(baseRec.Reconciled)

	hist25 := make([]float64, 25)
	for i := range hist25 {
		hist25[i] = 480.0 + float64(i%8)*5.0
	}

	cases := []FraudAnomalyCase{
		{
			Name:         "Clean Baseline Invoice",
			ExpectedFlag: "",
			ShouldFlag:   false,
			Record: models.InvoiceRecord{
				ID:             "clean-002",
				FileHashSHA256: "sha_clean_002",
				PrimaryPHash:   "1111222233334444",
				Validation:     models.ValidationReport{ArithmeticPassed: true},
				Reconciled: models.ExtractedInvoice{
					VendorName:    "Acme Cloud Infrastructure Inc",
					VendorTaxID:   "US-94-3321908",
					InvoiceNumber: "INV-2026-1002",
					InvoiceDate:   "2026-05-05",
					Total:         505.00,
					TaxTotal:      40.40,
					BankDetails:   "US88ACME0009876543",
				},
			},
			Existing:   []models.InvoiceRecord{baseRec},
			VendorAmts: hist25,
			VendorNums: []string{"20261001"},
			KnownBank:  "US88ACME0009876543",
			KnownTax:   "US943321908",
		},
		{
			Name:         "Injected Exact Duplicate",
			ExpectedFlag: "duplicate_exact",
			ShouldFlag:   true,
			Record: models.InvoiceRecord{
				ID:             "anom-dup-1",
				FileHashSHA256: "sha_clean_001",
				PrimaryPHash:   "1234567890abcdef",
				Validation:     models.ValidationReport{ArithmeticPassed: true},
				Reconciled:     baseRec.Reconciled,
			},
			Existing:   []models.InvoiceRecord{baseRec},
			VendorAmts: hist25,
			VendorNums: []string{"20261001"},
			KnownBank:  "US88ACME0009876543",
			KnownTax:   "US943321908",
		},
		{
			Name:         "Injected Perceptual Hash Scan Duplicate",
			ExpectedFlag: "duplicate_phash",
			ShouldFlag:   true,
			Record: models.InvoiceRecord{
				ID:             "anom-dup-phash",
				FileHashSHA256: "sha_different_recompressed",
				PrimaryPHash:   "aaaa0000aaaa0001", // 1 bit difference
				Validation:     models.ValidationReport{ArithmeticPassed: true},
				Reconciled: models.ExtractedInvoice{
					VendorName:    "Acme Cloud Infrastructure Inc",
					VendorTaxID:   "US-94-3321908",
					InvoiceNumber: "INV-2026-1003",
					InvoiceDate:   "2026-05-06",
					Total:         510.00,
					TaxTotal:      40.80,
					BankDetails:   "US88ACME0009876543",
				},
			},
			Existing:   []models.InvoiceRecord{baseRec},
			VendorAmts: hist25,
			VendorNums: []string{"20261001"},
			KnownBank:  "US88ACME0009876543",
			KnownTax:   "US943321908",
		},
		{
			Name:         "Injected Vendor Amount Spike (Robust Z-Score)",
			ExpectedFlag: "vendor_outlier_z",
			ShouldFlag:   true,
			Record: models.InvoiceRecord{
				ID:             "anom-outlier-z",
				FileHashSHA256: "sha_outlier",
				PrimaryPHash:   "9999888877776666",
				Validation:     models.ValidationReport{ArithmeticPassed: true},
				Reconciled: models.ExtractedInvoice{
					VendorName:    "Acme Cloud Infrastructure Inc",
					VendorTaxID:   "US-94-3321908",
					InvoiceNumber: "INV-2026-1004",
					InvoiceDate:   "2026-05-07",
					Total:         9850.00,
					TaxTotal:      788.00,
					BankDetails:   "US88ACME0009876543",
				},
			},
			Existing:   []models.InvoiceRecord{baseRec},
			VendorAmts: hist25,
			VendorNums: []string{"20261001"},
			KnownBank:  "US88ACME0009876543",
			KnownTax:   "US943321908",
		},
		{
			Name:         "Injected Bank IBAN Diversion",
			ExpectedFlag: "rule_bank_change",
			ShouldFlag:   true,
			Record: models.InvoiceRecord{
				ID:             "anom-bank-change",
				FileHashSHA256: "sha_bank",
				PrimaryPHash:   "5555444433332222",
				Validation:     models.ValidationReport{ArithmeticPassed: true},
				Reconciled: models.ExtractedInvoice{
					VendorName:    "Acme Cloud Infrastructure Inc",
					VendorTaxID:   "US-94-3321908",
					InvoiceNumber: "INV-2026-1005",
					InvoiceDate:   "2026-05-08",
					Total:         505.00,
					TaxTotal:      40.40,
					BankDetails:   "GB99FRAUDACCOUNT0001",
				},
			},
			Existing:   []models.InvoiceRecord{baseRec},
			VendorAmts: hist25,
			VendorNums: []string{"20261001"},
			KnownBank:  "US88ACME0009876543",
			KnownTax:   "US943321908",
		},
		{
			Name:         "Injected Altered Total (Math Mismatch)",
			ExpectedFlag: "rule_math_mismatch",
			ShouldFlag:   true,
			Record: models.InvoiceRecord{
				ID:             "anom-math",
				FileHashSHA256: "sha_math",
				PrimaryPHash:   "2222333344445555",
				Validation: models.ValidationReport{
					ArithmeticPassed: false,
					Checks: []models.ArithmeticCheck{
						{CheckName: "subtotal_plus_tax_eq_total", Passed: false, Expected: 540, Actual: 1540, Difference: 1000, Tolerance: 0.02, Details: "tampered total"},
					},
				},
				Reconciled: models.ExtractedInvoice{
					VendorName:    "Acme Cloud Infrastructure Inc",
					VendorTaxID:   "US-94-3321908",
					InvoiceNumber: "INV-2026-1006",
					InvoiceDate:   "2026-05-08",
					Total:         505.00,
					TaxTotal:      40.40,
					BankDetails:   "US88ACME0009876543",
				},
			},
			Existing:   []models.InvoiceRecord{baseRec},
			VendorAmts: hist25,
			VendorNums: []string{"20261001"},
			KnownBank:  "US88ACME0009876543",
			KnownTax:   "US943321908",
		},
	}

	targetTypes := []string{"duplicate_exact", "duplicate_phash", "vendor_outlier_z", "rule_bank_change", "rule_math_mismatch"}
	counts := map[string]*fraudEvalRow{}
	for _, t := range targetTypes {
		counts[t] = &fraudEvalRow{AnomalyType: t}
	}

	for _, c := range cases {
		flags := aud.AuditInvoice(&c.Record, c.Existing, c.VendorAmts, c.VendorNums, c.KnownBank, c.KnownTax)
		gotSet := map[string]bool{}
		for _, fl := range flags {
			gotSet[fl.FlagType] = true
		}
		for _, t := range targetTypes {
			row := counts[t]
			want := c.ShouldFlag && c.ExpectedFlag == t
			got := gotSet[t]
			if want && got {
				row.TP++
			} else if !want && got {
				row.FP++
			} else if want && !got {
				row.FN++
			}
		}
	}

	var rows []fraudEvalRow
	for _, t := range targetTypes {
		r := counts[t]
		if r.TP+r.FP > 0 {
			r.Precision = float64(r.TP) / float64(r.TP+r.FP)
		}
		if r.TP+r.FN > 0 {
			r.Recall = float64(r.TP) / float64(r.TP+r.FN)
		}
		if r.Precision+r.Recall > 0 {
			r.F1 = 2 * r.Precision * r.Recall / (r.Precision + r.Recall)
		}
		rows = append(rows, *r)
	}

	var natAmts []float64
	for i := 0; i < 120; i++ {
		natAmts = append(natAmts, math.Pow(10.0, float64(i)/40.0)*10.0)
	}
	benNat := aud.AnalyzeBenford("natural_120_batch", natAmts)

	var manipAmts []float64
	for i := 0; i < 90; i++ {
		manipAmts = append(manipAmts, 880.0+float64(i%90))
	}
	benManip := aud.AnalyzeBenford("threshold_splitting_90_batch", manipAmts)

	return rows, benNat, benManip
}

func evaluateAdaptiveLoop(st *store.Store, ret *memory.Retriever) string {
	// Store a human reviewer correction for a vendor with custom layout/tax formatting
	seedRec := &models.InvoiceRecord{
		ID:             "adapt-seed-1",
		Filename:       "vanguard_seed.pdf",
		FileHashSHA256: "sha_seed_1",
		PrimaryPHash:   "1234123412341234",
		Status:         models.InvoiceNeedsReview,
		ParseResult: &models.DocumentParseResult{
			Markdown:        "ISSUER: Vanguard Industrial Robotics Ltd | REG/VAT: GB774910221\nDOC REF: VG-2026-070",
			LayoutSignature: []float64{0.45, 0.45, 0.3, 0.3, 0.2, 0.2, 0.1, 0.1, 0, 0, 0, 0, 0, 0, 0, 0},
			Blocks: []models.ParseBlock{
				{Page: 1, BlockType: "header", Text: "ISSUER: Vanguard Industrial Robotics Ltd | REG/VAT: GB774910221"},
			},
		},
		Reconciled: models.ExtractedInvoice{
			VendorName:    "Vanguard Robotics",
			InvoiceNumber: "VG-2026-070",
			Total:         1104.00,
		},
	}
	_ = st.SaveInvoice(seedRec)

	_, _ = st.RecordReview(models.ReviewSubmission{
		InvoiceID: "adapt-seed-1",
		Reviewer:  "lead_auditor",
		Corrected: models.ExtractedInvoice{
			VendorName:    "Vanguard Industrial Robotics Ltd",
			VendorTaxID:   "GB774910221",
			InvoiceNumber: "VG-2026-070",
			InvoiceDate:   "2026-05-01",
			DueDate:       "2026-05-31",
			Currency:      "GBP",
			Subtotal:      920.00,
			TaxTotal:      184.00,
			Total:         1104.00,
			BankDetails:   "GB29NWBK60161331926819",
		},
		Notes: "Normalized full legal entity name and VAT ID",
	})

	allEx, _ := st.ListAdaptiveExamples()
	heldOutParse := &models.DocumentParseResult{
		Markdown:        "ISSUER: Vanguard Industrial Robotics Ltd | REG/VAT: GB774910221\nDOC REF: VG-2026-077",
		LayoutSignature: []float64{0.44, 0.46, 0.31, 0.29, 0.2, 0.2, 0.1, 0.1, 0, 0, 0, 0, 0, 0, 0, 0},
		Blocks: []models.ParseBlock{
			{Page: 1, BlockType: "header", Text: "ISSUER: Vanguard Industrial Robotics Ltd | REG/VAT: GB774910221"},
		},
	}
	retrieved := ret.RetrieveTopK(heldOutParse, allEx)
	if len(retrieved) > 0 {
		return fmt.Sprintf("Retrieved %d prior human correction(s) for vendor `%s` (similarity=%.3f, canonical entity=`%s`, VAT=`%s`)",
			len(retrieved), retrieved[0].VendorNorm, retrieved[0].SimilarityScore, retrieved[0].CorrectedJSON.VendorName, retrieved[0].CorrectedJSON.VendorTaxID)
	}
	return "No adaptive examples retrieved"
}

func buildEvaluationMarkdown(
	textModel string,
	vlmModel string,
	numCtx int,
	fieldNames []string,
	statsMap map[string]*FieldEvalStats,
	totalInvoices int,
	autoAccepted int,
	needsReview int,
	avgAgreement float64,
	avgParseMs int64,
	avgEngAMs int64,
	avgEngBMs int64,
	avgTotalMs int64,
	peakVRAMMiB int64,
	goAllocMiB float64,
	goSysMiB float64,
	fraudRows []fraudEvalRow,
	benNat models.BenfordReport,
	benManip models.BenfordReport,
	adaptiveSummary string,
) string {
	var sb strings.Builder
	sb.WriteString("# Evaluation Report\n\n")
	sb.WriteString(fmt.Sprintf("- **Generated:** `%s`\n", time.Now().UTC().Format(time.RFC3339)))
	sb.WriteString("- **GPU:** NVIDIA GeForce RTX 4050 Laptop GPU (`6,141 MiB` VRAM)\n")
	sb.WriteString("- **CPU / RAM:** AMD Ryzen 7 7445HS (12 threads), 14 GiB System RAM\n")
	sb.WriteString(fmt.Sprintf("- **Engine A Parser:** `PaddlePaddle/PaddleOCR-VL-1.6` Worker\n"))
	sb.WriteString(fmt.Sprintf("- **Engine A Text LLM:** `%s`; **Engine B Vision VLM:** `%s` via Ollama (`num_ctx=%d`, `think=false`, `temp=0`)\n\n", textModel, vlmModel, numCtx))

	sb.WriteString("## 1. Per-Field Accuracy: Engine A vs. Engine B vs. Dual-Engine Consensus\n\n")
	sb.WriteString("| Field | Samples | Engine A Exact | Engine A Token F1 | Engine B Exact | Engine B Token F1 | Consensus Exact | Consensus Token F1 | Auto-Verified Coverage | Accuracy on Verified Subset |\n")
	sb.WriteString("|---|---|---|---|---|---|---|\n")

	var totFields, totA, totB, totC, totVer, totVerRight int
	for _, fn := range fieldNames {
		st := statsMap[fn]
		totFields += st.Total
		totA += st.EngineACorrect
		totB += st.EngineBCorrect
		totC += st.ConsCorrect
		totVer += st.VerifiedCount
		totVerRight += st.VerifiedRight

		aAcc := pct(st.EngineACorrect, st.Total)
		bAcc := pct(st.EngineBCorrect, st.Total)
		cAcc := pct(st.ConsCorrect, st.Total)
		cov := pct(st.VerifiedCount, st.Total)
		verAcc := 100.0
		if st.VerifiedCount > 0 {
			verAcc = pct(st.VerifiedRight, st.VerifiedCount)
		}
		sb.WriteString(fmt.Sprintf("| `%s` | %d | %.1f%% | %.3f | %.1f%% | %.3f | **%.1f%%** | **%.3f** | %.1f%% (%d/%d) | **%.1f%%** |\n",
			fn, st.Total, aAcc, st.EngineAF1/float64(maxInt(1, st.Total)), bAcc, st.EngineBF1/float64(maxInt(1, st.Total)), cAcc, st.ConsensusF1/float64(maxInt(1, st.Total)), cov, st.VerifiedCount, st.Total, verAcc))
	}

	overallVerAcc := 100.0
	if totVer > 0 {
		overallVerAcc = pct(totVerRight, totVer)
	}
	var f1A, f1B, f1C float64
	for _, st := range statsMap {
		f1A += st.EngineAF1
		f1B += st.EngineBF1
		f1C += st.ConsensusF1
	}
	sb.WriteString(fmt.Sprintf("| **OVERALL (Micro-Avg)** | **%d** | **%.1f%%** | **%.3f** | **%.1f%%** | **%.3f** | **%.1f%%** | **%.3f** | **%.1f%% (%d/%d)** | **%.1f%%** |\n\n",
		totFields, pct(totA, totFields), f1A/float64(maxInt(1, totFields)), pct(totB, totFields), f1B/float64(maxInt(1, totFields)), pct(totC, totFields), f1C/float64(maxInt(1, totFields)), pct(totVer, totFields), totVer, totFields, overallVerAcc))

	sb.WriteString("## 2. Consensus & Workflow Metrics\n\n")
	sb.WriteString(fmt.Sprintf("- **Inter-Engine Agreement Rate:** `%.1f%%`\n", avgAgreement*100.0))
	sb.WriteString(fmt.Sprintf("- **Auto-Accepted Invoices:** `%d / %d (%.1f%%)`\n", autoAccepted, totalInvoices, pct(autoAccepted, totalInvoices)))
	sb.WriteString(fmt.Sprintf("- **Human Review Rate:** `%d / %d (%.1f%%)`\n\n", needsReview, totalInvoices, pct(needsReview, totalInvoices)))

	sb.WriteString("## 3. Fraud Audit Precision & Recall on Injected Anomalies\n\n")
	sb.WriteString("| Anomaly Type | TP | FP | FN | Precision | Recall | F1 |\n")
	sb.WriteString("|---|---|---|---|---|---|---|\n")
	for _, r := range fraudRows {
		sb.WriteString(fmt.Sprintf("| `%s` | %d | %d | %d | %.2f | %.2f | **%.2f** |\n",
			r.AnomalyType, r.TP, r.FP, r.FN, r.Precision, r.Recall, r.F1))
	}
	sb.WriteString("\n### Batch-Level Benford's Law Evaluation\n")
	sb.WriteString(fmt.Sprintf("- **Natural Invoice Batch ($n=%d$):** $\\chi^2 = %.2f$ (crit $15.51$), $\\text{MAD} = %.4f$ → `%s` (Suspicious=`%v`)\n",
		benNat.SampleSize, benNat.ChiSquare, benNat.MAD, benNat.Conformity, benNat.Suspicious))
	sb.WriteString(fmt.Sprintf("- **Manipulated Threshold-Splitting Batch ($n=%d$):** $\\chi^2 = %.2f$ (crit $15.51$), $\\text{MAD} = %.4f$ → `%s` (Suspicious=`%v`)\n\n",
		benManip.SampleSize, benManip.ChiSquare, benManip.MAD, benManip.Conformity, benManip.Suspicious))

	sb.WriteString("## 4. Adaptive Few-Shot Memory Verification\n\n")
	sb.WriteString(fmt.Sprintf("- **Result:** %s\n\n", adaptiveSummary))

	sb.WriteString("## 5. Hardware, Latency & Memory Profile (RTX 4050 6 GB VRAM)\n\n")
	sb.WriteString("| Metric | Measured Value |\n")
	sb.WriteString("|---|---|\n")
	sb.WriteString(fmt.Sprintf("| **Go Backend Heap Alloc** | `%.2f MiB` |\n", goAllocMiB))
	sb.WriteString(fmt.Sprintf("| **Go Backend Total Sys RAM** | `%.2f MiB` |\n", goSysMiB))
	sb.WriteString(fmt.Sprintf("| **Peak GPU VRAM Used** | `%d MiB / 6141 MiB` |\n", peakVRAMMiB))
	sb.WriteString(fmt.Sprintf("| **Avg Stage 1 (`PaddleOCR-VL-1.6` Parse) Latency** | `%d ms` |\n", avgParseMs))
	sb.WriteString(fmt.Sprintf("| **Avg Stage 2 (Engine A Text LLM) Latency** | `%d ms` |\n", avgEngAMs))
	sb.WriteString(fmt.Sprintf("| **Avg Stage 3 (Engine B Vision VLM) Latency** | `%d ms` |\n", avgEngBMs))
	sb.WriteString(fmt.Sprintf("| **Avg Total End-to-End Invoice Latency** | `%d ms` |\n", avgTotalMs))

	return sb.String()
}

func pct(num, den int) float64 {
	if den <= 0 {
		return 0
	}
	return 100.0 * float64(num) / float64(den)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func buildHardwareProfile(textModel, vlmModel string, numCtx int, vram int64, heap, sys float64, parseMs, aMs, bMs, totalMs int64) string {
	return fmt.Sprintf(`# Hardware Profile

Generated: %s

Hardware identity is configured from PROJECT_STATUS.md; runtime measurements below are sampled during the latest evaluation. A zero VRAM value means nvidia-smi was unavailable.

- Engine A text model: %s
- Engine B vision model: %s
- Ollama context: %d

| Measurement | Value |
|---|---:|
| Peak GPU memory used | %d MiB |
| Go heap allocation | %.2f MiB |
| Go runtime system memory | %.2f MiB |
| Average parser latency | %d ms/invoice |
| Average Engine A latency | %d ms/invoice |
| Average Engine B latency | %d ms/invoice |
| Average total latency | %d ms/invoice |
`, time.Now().UTC().Format(time.RFC3339), textModel, vlmModel, numCtx, vram, heap, sys, parseMs, aMs, bMs, totalMs)
}

func queryGPUVRAM() int64 {
	out, err := exec.Command("nvidia-smi", "--query-gpu=memory.used", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return 0
	}
	var v int64
	_, _ = fmt.Sscanf(strings.TrimSpace(lines[0]), "%d", &v)
	return v
}
