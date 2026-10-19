package models

import (
	"encoding/json"
	"time"
)

// FieldStatus represents the consensus & validation status of an individual field.
type FieldStatus string

const (
	StatusVerified     FieldStatus = "verified"      // Both engines agree after normalization and validation passes
	StatusConflict     FieldStatus = "conflict"      // Engines disagree; human review required
	StatusSingleSource FieldStatus = "single_source" // Only one engine extracted a non-empty value
	StatusUnverified   FieldStatus = "unverified"    // Validation failed or both engines empty
	StatusCorrected    FieldStatus = "corrected"     // Human reviewer corrected/approved
)

// InvoiceStatus represents the overall workflow status of an invoice.
type InvoiceStatus string

const (
	InvoiceAutoAccepted InvoiceStatus = "auto_accepted"
	InvoiceNeedsReview  InvoiceStatus = "needs_review"
	InvoiceReviewed     InvoiceStatus = "reviewed"
	InvoiceRejected     InvoiceStatus = "rejected"
)

// BoundingBox represents normalized or pixel coordinates [x1, y1, x2, y2] on a page.
type BoundingBox struct {
	Page int     `json:"page"`
	X1   float64 `json:"x1"`
	Y1   float64 `json:"y1"`
	X2   float64 `json:"x2"`
	Y2   float64 `json:"y2"`
	Text string  `json:"text,omitempty"`
}

// RawLineItem is the schema-constrained line item emitted by an extraction engine.
type RawLineItem struct {
	Description string  `json:"description"`
	Quantity    float64 `json:"quantity"`
	UnitPrice   float64 `json:"unit_price"`
	Amount      float64 `json:"amount"`
}

// ExtractedInvoice is the schema-constrained JSON output produced by Engine A and Engine B.
type ExtractedInvoice struct {
	VendorName    string        `json:"vendor_name"`
	VendorTaxID   string        `json:"vendor_tax_id,omitempty"`
	InvoiceNumber string        `json:"invoice_number"`
	InvoiceDate   string        `json:"invoice_date"`
	DueDate       string        `json:"due_date,omitempty"`
	Currency      string        `json:"currency"`
	Subtotal      float64       `json:"subtotal"`
	TaxTotal      float64       `json:"tax_total"`
	Total         float64       `json:"total"`
	BankDetails   string        `json:"bank_details,omitempty"`
	LineItems     []RawLineItem `json:"line_items"`
}

// ParseBlock represents a layout/text/table block emitted by the Engine A parser (PaddleOCR-VL-1.6).
type ParseBlock struct {
	Page       int         `json:"page"`
	BlockType  string      `json:"block_type"` // "text", "table", "header", "seal", "spotting"
	Text       string      `json:"text"`
	BBox       BoundingBox `json:"bbox"`
	Confidence float64     `json:"confidence"`
}

// DocumentParseResult is the output of Engine A's parser (PaddleOCR-VL-1.6).
type DocumentParseResult struct {
	Markdown        string       `json:"markdown"`
	Blocks          []ParseBlock `json:"blocks"`
	LayoutSignature []float64    `json:"layout_signature"`
	ParserBackend   string       `json:"parser_backend"`
	LatencyMs       int64        `json:"latency_ms"`
	PeakVRAMMiB     int64        `json:"peak_vram_mib,omitempty"`
}

// FieldResult stores per-field dual-engine candidates, normalized values, grounding, and consensus status.
type FieldResult struct {
	FieldName       string       `json:"field_name"`
	NormalizedValue string       `json:"normalized_value"`
	DisplayValue    string       `json:"display_value"`
	EngineAValue    string       `json:"engine_a_value"`
	EngineBValue    string       `json:"engine_b_value"`
	NormalizedA     string       `json:"normalized_a"`
	NormalizedB     string       `json:"normalized_b"`
	SourceEngine    string       `json:"source_engine"` // "consensus", "engine_a", "engine_b", "reviewer"
	Similarity      float64      `json:"similarity"`
	Confidence      float64      `json:"confidence"`
	Status          FieldStatus  `json:"status"`
	Grounded        bool         `json:"grounded"`
	Evidence        *BoundingBox `json:"evidence,omitempty"`
	Critical        bool         `json:"critical"`
	Notes           string       `json:"notes,omitempty"`
}

// LineItemResult represents a reconciled line item after row-matching between Engine A and Engine B.
type LineItemResult struct {
	RowIndex     int          `json:"row_index"`
	Description  string       `json:"description"`
	Quantity     float64      `json:"quantity"`
	UnitPrice    float64      `json:"unit_price"`
	Amount       float64      `json:"amount"`
	EngineAMatch *RawLineItem `json:"engine_a_match,omitempty"`
	EngineBMatch *RawLineItem `json:"engine_b_match,omitempty"`
	Status       FieldStatus  `json:"status"`
	Confidence   float64      `json:"confidence"`
	MathValid    bool         `json:"math_valid"`
	Evidence     *BoundingBox `json:"evidence,omitempty"`
}

// ArithmeticCheck captures a specific mathematical validation rule on the invoice.
type ArithmeticCheck struct {
	CheckName  string  `json:"check_name"`
	Passed     bool    `json:"passed"`
	Expected   float64 `json:"expected"`
	Actual     float64 `json:"actual"`
	Difference float64 `json:"difference"`
	Tolerance  float64 `json:"tolerance"`
	Details    string  `json:"details"`
}

// ValidationReport summarizes grounding and arithmetic checks for an invoice.
type ValidationReport struct {
	AllCriticalVerified bool              `json:"all_critical_verified"`
	AllCriticalGrounded bool              `json:"all_critical_grounded"`
	ArithmeticPassed    bool              `json:"arithmetic_passed"`
	Checks              []ArithmeticCheck `json:"checks"`
	GroundingRate       float64           `json:"grounding_rate"`
}

// FraudFlag represents an explainable anomaly or rule violation raised by the Fraud Audit module.
type FraudFlag struct {
	ID          int64     `json:"id,omitempty"`
	InvoiceID   string    `json:"invoice_id"`
	FlagType    string    `json:"flag_type"` // "duplicate_exact", "duplicate_fuzzy", "duplicate_phash", "vendor_outlier_z", "benford_anomaly", "rule_round_total", "rule_weekend_date", "rule_seq_gap", "rule_bank_change", "rule_math_mismatch"
	Severity    string    `json:"severity"`  // "high", "medium", "low", "info"
	Reason      string    `json:"reason"`
	MetricValue float64   `json:"metric_value,omitempty"`
	Threshold   float64   `json:"threshold,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// EngineRunAudit records raw outputs, model identifiers, and latency for each engine run.
type EngineRunAudit struct {
	ID           int64     `json:"id,omitempty"`
	InvoiceID    string    `json:"invoice_id"`
	EngineName   string    `json:"engine_name"` // "paddleocr_vl_1_6", "engine_a_text_llm", "engine_b_vlm"
	ModelID      string    `json:"model_id"`
	RawOutput    string    `json:"raw_output"`
	LatencyMs    int64     `json:"latency_ms"`
	PromptTokens int       `json:"prompt_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// PageAsset stores rendered page metadata for an ingested document.
type PageAsset struct {
	PageNumber int    `json:"page_number"`
	ImagePath  string `json:"image_path"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	PHash      uint64 `json:"phash"`
	PHashHex   string `json:"phash_hex"`
}

// InvoiceRecord is the full aggregate persisted in SQLite and returned to the UI/API.
type InvoiceRecord struct {
	ID               string               `json:"id"`
	Filename         string               `json:"filename"`
	FileHashSHA256   string               `json:"file_hash_sha256"`
	CompositeHash    string               `json:"composite_hash"`
	PrimaryPHash     string               `json:"primary_phash"`
	Status           InvoiceStatus        `json:"status"`
	OverallConf      float64              `json:"overall_confidence"`
	AgreementRate    float64              `json:"agreement_rate"`
	Pages            []PageAsset          `json:"pages"`
	ParseResult      *DocumentParseResult `json:"parse_result,omitempty"`
	EngineAOutput    *ExtractedInvoice    `json:"engine_a_output,omitempty"`
	EngineBOutput    *ExtractedInvoice    `json:"engine_b_output,omitempty"`
	Reconciled       ExtractedInvoice     `json:"reconciled"`
	Fields           []FieldResult        `json:"fields"`
	LineItems        []LineItemResult     `json:"line_items"`
	Validation       ValidationReport     `json:"validation"`
	FraudFlags       []FraudFlag          `json:"fraud_flags"`
	EngineRuns       []EngineRunAudit     `json:"engine_runs"`
	AdaptiveShotUsed int                  `json:"adaptive_shots_used"`
	TotalLatencyMs   int64                `json:"total_latency_ms"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
}

// ReviewSubmission represents a human reviewer's correction or approval of an invoice.
type ReviewSubmission struct {
	InvoiceID  string           `json:"invoice_id"`
	Reviewer   string           `json:"reviewer"`
	Corrected  ExtractedInvoice `json:"corrected"`
	Notes      string           `json:"notes"`
	ReviewedAt time.Time        `json:"reviewed_at"`
}

// AdaptiveExample represents a human-verified correction stored in adaptive memory for few-shot retrieval.
type AdaptiveExample struct {
	ID              int64            `json:"id"`
	InvoiceID       string           `json:"invoice_id"`
	VendorNorm      string           `json:"vendor_norm"`
	HeaderSnippet   string           `json:"header_snippet"`
	OCRExcerpt      string           `json:"ocr_excerpt"`
	LayoutSignature []float64        `json:"layout_signature"`
	CorrectedJSON   ExtractedInvoice `json:"corrected_json"`
	SimilarityScore float64          `json:"similarity_score,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
}

// BenfordDigitStat holds observed vs expected frequencies for leading digit d in {1..9}.
type BenfordDigitStat struct {
	Digit        int     `json:"digit"`
	ObservedCnt  int     `json:"observed_count"`
	ObservedFreq float64 `json:"observed_freq"`
	ExpectedFreq float64 `json:"expected_freq"`
	Deviation    float64 `json:"deviation"`
}

// BenfordReport is a population/batch-level Benford's Law analysis.
type BenfordReport struct {
	Scope          string             `json:"scope"` // "all_invoices" or vendor name
	SampleSize     int                `json:"sample_size"`
	MinRequired    int                `json:"min_required"`
	SufficientData bool               `json:"sufficient_data"`
	ChiSquare      float64            `json:"chi_square"`
	CriticalValue  float64            `json:"critical_value_p05"` // 15.507 for df=8 at alpha=0.05
	MAD            float64            `json:"mad"`                // Mean Absolute Deviation
	Conformity     string             `json:"conformity"`         // "insufficient_sample", "close_conformity", "acceptable_conformity", "marginally_acceptable", "nonconformity"
	Suspicious     bool               `json:"suspicious"`
	Digits         []BenfordDigitStat `json:"digits"`
}

// VendorStats summarizes historical invoice amounts for a specific vendor.
type VendorStats struct {
	VendorNorm      string   `json:"vendor_norm"`
	DisplayName     string   `json:"display_name"`
	InvoiceCount    int      `json:"invoice_count"`
	ColdStart       bool     `json:"cold_start"`
	MinHistoryReq   int      `json:"min_history_required"`
	MedianTotal     float64  `json:"median_total"`
	MADTotal        float64  `json:"mad_total"`
	MeanTotal       float64  `json:"mean_total"`
	StdDevTotal     float64  `json:"std_dev_total"`
	KnownBankDetail string   `json:"known_bank_details"`
	KnownTaxID      string   `json:"known_tax_id"`
	RecentNumbers   []string `json:"recent_invoice_numbers"`
}

// ExtractionJSONSchema returns the strict JSON Schema object passed to Ollama's `format` parameter.
func ExtractionJSONSchema() json.RawMessage {
	schema := `{
  "type": "object",
  "properties": {
    "vendor_name": { "type": "string", "description": "Full legal or trade name of the issuing vendor/supplier" },
    "vendor_tax_id": { "type": "string", "description": "Vendor tax ID, VAT, EIN, or GST number if present, else empty string" },
    "invoice_number": { "type": "string", "description": "Unique invoice identifier or number" },
    "invoice_date": { "type": "string", "description": "Invoice issue date (YYYY-MM-DD preferred)" },
    "due_date": { "type": "string", "description": "Payment due date if present, else empty string" },
    "currency": { "type": "string", "description": "3-letter ISO currency code such as USD, EUR, GBP, INR" },
    "subtotal": { "type": "number", "description": "Pre-tax subtotal amount as a decimal number" },
    "tax_total": { "type": "number", "description": "Total tax/VAT/GST amount as a decimal number (0 if none)" },
    "total": { "type": "number", "description": "Final grand total amount due as a decimal number" },
    "bank_details": { "type": "string", "description": "IBAN, routing/account number, or payment bank details if present, else empty string" },
    "line_items": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "description": { "type": "string" },
          "quantity": { "type": "number" },
          "unit_price": { "type": "number" },
          "amount": { "type": "number" }
        },
        "required": ["description", "quantity", "unit_price", "amount"],
        "additionalProperties": false
      }
    }
  },
  "required": [
    "vendor_name",
    "vendor_tax_id",
    "invoice_number",
    "invoice_date",
    "due_date",
    "currency",
    "subtotal",
    "tax_total",
    "total",
    "bank_details",
    "line_items"
  ],
  "additionalProperties": false
}`
	return json.RawMessage(schema)
}
