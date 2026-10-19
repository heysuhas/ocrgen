package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"ocrgen/internal/engines"
	"ocrgen/internal/fraud"
	"ocrgen/internal/ingest"
	"ocrgen/internal/memory"
	"ocrgen/internal/models"
	"ocrgen/internal/pipeline"
	"ocrgen/internal/store"
)

// Options configures a single invoice processing run (e.g., enabling/disabling Engine B or adaptive shots for ablations).
type ProcessOptions struct {
	DisableEngineB  bool
	DisableAdaptive bool
}

// Coordinator orchestrates ingestion, dual-engine extraction, consensus, validation, fraud auditing, and persistence.
type Coordinator struct {
	Ingestor     *ingest.Ingestor
	Parser       engines.Parser
	EngineA      engines.Extractor
	EngineB      engines.Extractor
	Store        *store.Store
	Auditor      *fraud.Auditor
	Retriever    *memory.Retriever
	ConsensusCfg pipeline.ConsensusConfig
}

func NewCoordinator(
	ing *ingest.Ingestor,
	parser engines.Parser,
	engA engines.Extractor,
	engB engines.Extractor,
	st *store.Store,
	aud *fraud.Auditor,
	ret *memory.Retriever,
) *Coordinator {
	return &Coordinator{
		Ingestor:     ing,
		Parser:       parser,
		EngineA:      engA,
		EngineB:      engB,
		Store:        st,
		Auditor:      aud,
		Retriever:    ret,
		ConsensusCfg: pipeline.DefaultConsensusConfig(),
	}
}

// ProcessFile runs the end-to-end pipeline on a local PDF or image file.
func (c *Coordinator) ProcessFile(ctx context.Context, invoiceID, filePath string, opts ProcessOptions) (*models.InvoiceRecord, error) {
	if invoiceID == "" {
		invoiceID = fmt.Sprintf("inv-%d", time.Now().UnixNano()/1e6)
	}
	doc, err := c.Ingestor.IngestFile(invoiceID, filePath)
	if err != nil {
		return nil, fmt.Errorf("ingest file: %w", err)
	}
	return c.processIngested(ctx, doc, opts)
}

// ProcessUpload runs the end-to-end pipeline on an uploaded HTTP file stream.
func (c *Coordinator) ProcessUpload(ctx context.Context, invoiceID, filename string, r io.Reader, opts ProcessOptions) (*models.InvoiceRecord, error) {
	if invoiceID == "" {
		invoiceID = fmt.Sprintf("inv-%d", time.Now().UnixNano()/1e6)
	}
	doc, err := c.Ingestor.IngestReader(invoiceID, filename, r)
	if err != nil {
		return nil, fmt.Errorf("ingest upload: %w", err)
	}
	return c.processIngested(ctx, doc, opts)
}

func (c *Coordinator) processIngested(ctx context.Context, doc *ingest.IngestedDocument, opts ProcessOptions) (*models.InvoiceRecord, error) {
	t0 := time.Now()
	now := t0.UTC()

	var pagePaths []string
	for _, p := range doc.Pages {
		pagePaths = append(pagePaths, p.ImagePath)
	}

	var engineRuns []models.EngineRunAudit

	// Step 1: Engine A Document Parser (PaddleOCR-VL-1.6)
	parseRes, err := c.Parser.ParseDocument(ctx, pagePaths, doc.OriginalPath)
	if err != nil {
		return nil, fmt.Errorf("paddleocr-vl-1.6 parse failed: %w", err)
	}
	parseBytes, _ := json.Marshal(parseRes)
	engineRuns = append(engineRuns, models.EngineRunAudit{
		InvoiceID:  doc.InvoiceID,
		EngineName: "paddleocr_vl_1_6",
		ModelID:    parseRes.ParserBackend,
		RawOutput:  string(parseBytes),
		LatencyMs:  parseRes.LatencyMs,
		CreatedAt:  now,
	})

	// Explicit VRAM Unload after PaddleOCR-VL-1.6 so Ollama has full 6 GB VRAM
	_ = c.Parser.UnloadVRAM(ctx)

	// Step 2: Retrieve Adaptive Few-Shot Examples (if enabled and available)
	var fewShots []models.AdaptiveExample
	if !opts.DisableAdaptive && c.Store != nil && c.Retriever != nil {
		if allEx, err := c.Store.ListAdaptiveExamples(); err == nil && len(allEx) > 0 {
			fewShots = c.Retriever.RetrieveTopK(parseRes, allEx)
		}
	}

	// Step 3: Engine A Text LLM Extraction (consumes ONLY PaddleOCR-VL-1.6 Markdown, never the image)
	respA, err := c.EngineA.Extract(ctx, engines.ExtractionInput{
		Markdown:        parseRes.Markdown,
		FewShotExamples: fewShots,
	})
	if err != nil {
		return nil, fmt.Errorf("engine A text extraction failed: %w", err)
	}
	engineRuns = append(engineRuns, models.EngineRunAudit{
		InvoiceID:    doc.InvoiceID,
		EngineName:   "engine_a_text_llm",
		ModelID:      respA.ModelID,
		RawOutput:    respA.RawOutput,
		LatencyMs:    respA.LatencyMs,
		PromptTokens: respA.PromptTokens,
		OutputTokens: respA.OutputTokens,
		CreatedAt:    time.Now().UTC(),
	})

	// Step 4: Engine B Vision VLM Extraction (consumes ONLY raw page images, never Engine A's OCR output)
	var outB *models.ExtractedInvoice
	if !opts.DisableEngineB && c.EngineB != nil {
		respB, err := c.EngineB.Extract(ctx, engines.ExtractionInput{
			PageImagePaths:  pagePaths,
			FewShotExamples: fewShots,
		})
		if err != nil {
			return nil, fmt.Errorf("engine B vision extraction failed: %w", err)
		}
		outB = &respB.Extracted
		engineRuns = append(engineRuns, models.EngineRunAudit{
			InvoiceID:    doc.InvoiceID,
			EngineName:   "engine_b_vlm",
			ModelID:      respB.ModelID,
			RawOutput:    respB.RawOutput,
			LatencyMs:    respB.LatencyMs,
			PromptTokens: respB.PromptTokens,
			OutputTokens: respB.OutputTokens,
			CreatedAt:    time.Now().UTC(),
		})
	}

	// Step 5: Pre-comparison Normalization + Field & Line-Item Consensus
	consOut := pipeline.Reconcile(&respA.Extracted, outB, c.ConsensusCfg)

	// Step 6: OCR Grounding + Decimal Arithmetic Validation
	fields, lineItems, valReport, invStatus := pipeline.ValidateAndGround(
		consOut.Reconciled,
		consOut.Fields,
		consOut.LineItems,
		parseRes,
		c.ConsensusCfg.AmountTolerance,
	)

	rec := &models.InvoiceRecord{
		ID:               doc.InvoiceID,
		Filename:         doc.Filename,
		FileHashSHA256:   doc.FileHashSHA256,
		CompositeHash:    pipeline.ComputeCompositeHash(consOut.Reconciled),
		PrimaryPHash:     doc.PrimaryPHash,
		Status:           invStatus,
		OverallConf:      consOut.OverallConf,
		AgreementRate:    consOut.AgreementRate,
		Pages:            doc.Pages,
		ParseResult:      parseRes,
		EngineAOutput:    &respA.Extracted,
		EngineBOutput:    outB,
		Reconciled:       consOut.Reconciled,
		Fields:           fields,
		LineItems:        lineItems,
		Validation:       valReport,
		EngineRuns:       engineRuns,
		AdaptiveShotUsed: len(fewShots),
		TotalLatencyMs:   time.Since(t0).Milliseconds(),
		CreatedAt:        now,
		UpdatedAt:        time.Now().UTC(),
	}

	// Step 7: Statistical Fraud Auditing
	if c.Auditor != nil && c.Store != nil {
		existing, _ := c.Store.ListInvoices("")
		vNorm := pipeline.NormalizeVendorName(rec.Reconciled.VendorName)
		vAmts, vNums, kBank, kTax, _, _ := c.Store.GetVendorHistory(vNorm)
		flags := c.Auditor.AuditInvoice(rec, existing, vAmts, vNums, kBank, kTax)
		rec.FraudFlags = flags
		// Any high-severity fraud flag forces human review
		for _, fl := range flags {
			if fl.Severity == "high" && rec.Status == models.InvoiceAutoAccepted {
				rec.Status = models.InvoiceNeedsReview
			}
		}
	}

	// Step 8: Persist to SQLite and update vendor stats if clean
	if c.Store != nil {
		if err := c.Store.SaveInvoice(rec); err != nil {
			return nil, fmt.Errorf("save invoice to sqlite: %w", err)
		}
		if len(rec.FraudFlags) == 0 {
			_ = c.Store.UpdateVendorHistory(rec.Reconciled)
		}
	}

	return rec, nil
}
