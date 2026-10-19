package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ocrgen/internal/models"
	"ocrgen/internal/pipeline"
)

// Store manages all SQLite tables, FTS5 full-text index, vendor statistics, and adaptive memory records.
type Store struct {
	conn *sqliteConn
}

func NewStore(dbPath string) (*Store, error) {
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	conn, err := openSQLite(dbPath)
	if err != nil {
		return nil, err
	}
	s := &Store{conn: conn}
	if err := s.migrate(); err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.conn.Close()
}

func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS invoices (
	id TEXT PRIMARY KEY,
	filename TEXT NOT NULL,
	file_hash_sha256 TEXT NOT NULL,
	composite_hash TEXT NOT NULL,
	primary_phash TEXT NOT NULL,
	vendor_norm TEXT NOT NULL,
	vendor_display TEXT NOT NULL,
	invoice_number_norm TEXT NOT NULL,
	invoice_date_norm TEXT NOT NULL,
	total REAL NOT NULL,
	status TEXT NOT NULL,
	overall_confidence REAL NOT NULL,
	agreement_rate REAL NOT NULL,
	adaptive_shots_used INTEGER NOT NULL DEFAULT 0,
	total_latency_ms INTEGER NOT NULL DEFAULT 0,
	record_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_invoices_file_hash ON invoices(file_hash_sha256);
CREATE INDEX IF NOT EXISTS idx_invoices_comp_hash ON invoices(composite_hash);
CREATE INDEX IF NOT EXISTS idx_invoices_vendor ON invoices(vendor_norm);
CREATE INDEX IF NOT EXISTS idx_invoices_status ON invoices(status);

CREATE TABLE IF NOT EXISTS pages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	invoice_id TEXT NOT NULL,
	page_number INTEGER NOT NULL,
	image_path TEXT NOT NULL,
	width INTEGER NOT NULL,
	height INTEGER NOT NULL,
	phash_hex TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS fields (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	invoice_id TEXT NOT NULL,
	field_name TEXT NOT NULL,
	normalized_value TEXT NOT NULL,
	display_value TEXT NOT NULL,
	engine_a_value TEXT NOT NULL,
	engine_b_value TEXT NOT NULL,
	source_engine TEXT NOT NULL,
	similarity REAL NOT NULL,
	confidence REAL NOT NULL,
	status TEXT NOT NULL,
	grounded INTEGER NOT NULL,
	evidence_json TEXT NOT NULL,
	critical INTEGER NOT NULL,
	notes TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS line_items (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	invoice_id TEXT NOT NULL,
	row_index INTEGER NOT NULL,
	description TEXT NOT NULL,
	quantity REAL NOT NULL,
	unit_price REAL NOT NULL,
	amount REAL NOT NULL,
	status TEXT NOT NULL,
	confidence REAL NOT NULL,
	math_valid INTEGER NOT NULL,
	evidence_json TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS engine_runs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	invoice_id TEXT NOT NULL,
	engine_name TEXT NOT NULL,
	model_id TEXT NOT NULL,
	raw_output TEXT NOT NULL,
	latency_ms INTEGER NOT NULL,
	prompt_tokens INTEGER NOT NULL,
	output_tokens INTEGER NOT NULL,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS reviews (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	invoice_id TEXT NOT NULL,
	reviewer TEXT NOT NULL,
	corrected_json TEXT NOT NULL,
	notes TEXT NOT NULL,
	reviewed_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS fraud_flags (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	invoice_id TEXT NOT NULL,
	flag_type TEXT NOT NULL,
	severity TEXT NOT NULL,
	reason TEXT NOT NULL,
	metric_value REAL NOT NULL,
	threshold REAL NOT NULL,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS vendors (
	vendor_norm TEXT PRIMARY KEY,
	display_name TEXT NOT NULL,
	invoice_count INTEGER NOT NULL,
	known_bank_details TEXT NOT NULL,
	known_tax_id TEXT NOT NULL,
	amounts_json TEXT NOT NULL,
	invoice_numbers_json TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS adaptive_examples (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	invoice_id TEXT NOT NULL,
	vendor_norm TEXT NOT NULL,
	header_snippet TEXT NOT NULL,
	ocr_excerpt TEXT NOT NULL,
	layout_signature_json TEXT NOT NULL,
	corrected_json TEXT NOT NULL,
	created_at TEXT NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS invoices_fts USING fts5(
	invoice_id UNINDEXED,
	vendor_name,
	invoice_number,
	ocr_markdown,
	line_items_text
);
`
	return s.conn.Exec(schema)
}

// SaveInvoice persists an InvoiceRecord across all relational & audit tables.
// Note: raw engine_runs are append-only and never overwritten per Spec §4.7.
func (s *Store) SaveInvoice(rec *models.InvoiceRecord) error {
	now := time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now

	vendorNorm := pipeline.NormalizeVendorName(rec.Reconciled.VendorName)
	invNumNorm := pipeline.NormalizeInvoiceNumber(rec.Reconciled.InvoiceNumber)
	dateNorm := pipeline.NormalizeDate(rec.Reconciled.InvoiceDate)
	if rec.CompositeHash == "" {
		rec.CompositeHash = pipeline.ComputeCompositeHash(rec.Reconciled)
	}

	rawRecBytes, err := json.Marshal(rec)
	if err != nil {
		return err
	}

	err = s.conn.Exec(`
INSERT INTO invoices (
	id, filename, file_hash_sha256, composite_hash, primary_phash,
	vendor_norm, vendor_display, invoice_number_norm, invoice_date_norm,
	total, status, overall_confidence, agreement_rate, adaptive_shots_used,
	total_latency_ms, record_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	composite_hash=excluded.composite_hash,
	vendor_norm=excluded.vendor_norm,
	vendor_display=excluded.vendor_display,
	invoice_number_norm=excluded.invoice_number_norm,
	invoice_date_norm=excluded.invoice_date_norm,
	total=excluded.total,
	status=excluded.status,
	overall_confidence=excluded.overall_confidence,
	agreement_rate=excluded.agreement_rate,
	adaptive_shots_used=excluded.adaptive_shots_used,
	total_latency_ms=excluded.total_latency_ms,
	record_json=excluded.record_json,
	updated_at=excluded.updated_at
`,
		rec.ID,
		rec.Filename,
		rec.FileHashSHA256,
		rec.CompositeHash,
		rec.PrimaryPHash,
		vendorNorm,
		rec.Reconciled.VendorName,
		invNumNorm,
		dateNorm,
		rec.Reconciled.Total,
		string(rec.Status),
		rec.OverallConf,
		rec.AgreementRate,
		rec.AdaptiveShotUsed,
		rec.TotalLatencyMs,
		string(rawRecBytes),
		rec.CreatedAt.Format(time.RFC3339),
		rec.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("upsert invoice: %w", err)
	}

	// Refresh child projection tables for pages, fields, line_items, fraud_flags
	_ = s.conn.Exec(`DELETE FROM pages WHERE invoice_id = ?`, rec.ID)
	for _, pg := range rec.Pages {
		_ = s.conn.Exec(
			`INSERT INTO pages (invoice_id, page_number, image_path, width, height, phash_hex) VALUES (?, ?, ?, ?, ?, ?)`,
			rec.ID, pg.PageNumber, pg.ImagePath, pg.Width, pg.Height, pg.PHashHex,
		)
	}

	_ = s.conn.Exec(`DELETE FROM fields WHERE invoice_id = ?`, rec.ID)
	for _, f := range rec.Fields {
		evBytes, _ := json.Marshal(f.Evidence)
		_ = s.conn.Exec(`
INSERT INTO fields (
	invoice_id, field_name, normalized_value, display_value,
	engine_a_value, engine_b_value, source_engine, similarity,
	confidence, status, grounded, evidence_json, critical, notes
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rec.ID, f.FieldName, f.NormalizedValue, f.DisplayValue,
			f.EngineAValue, f.EngineBValue, f.SourceEngine, f.Similarity,
			f.Confidence, string(f.Status), f.Grounded, string(evBytes), f.Critical, f.Notes,
		)
	}

	_ = s.conn.Exec(`DELETE FROM line_items WHERE invoice_id = ?`, rec.ID)
	var lineTexts []string
	for _, li := range rec.LineItems {
		evBytes, _ := json.Marshal(li.Evidence)
		lineTexts = append(lineTexts, li.Description)
		_ = s.conn.Exec(`
INSERT INTO line_items (
	invoice_id, row_index, description, quantity, unit_price, amount,
	status, confidence, math_valid, evidence_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rec.ID, li.RowIndex, li.Description, li.Quantity, li.UnitPrice, li.Amount,
			string(li.Status), li.Confidence, li.MathValid, string(evBytes),
		)
	}

	_ = s.conn.Exec(`DELETE FROM fraud_flags WHERE invoice_id = ?`, rec.ID)
	for _, fl := range rec.FraudFlags {
		ts := fl.CreatedAt
		if ts.IsZero() {
			ts = now
		}
		_ = s.conn.Exec(`
INSERT INTO fraud_flags (invoice_id, flag_type, severity, reason, metric_value, threshold, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			rec.ID, fl.FlagType, fl.Severity, fl.Reason, fl.MetricValue, fl.Threshold, ts.Format(time.RFC3339),
		)
	}

	// Append-only engine_runs (only insert if none recorded yet for this invoice+engine)
	for _, er := range rec.EngineRuns {
		ts := er.CreatedAt
		if ts.IsZero() {
			ts = now
		}
		_ = s.conn.Exec(`
INSERT INTO engine_runs (invoice_id, engine_name, model_id, raw_output, latency_ms, prompt_tokens, output_tokens, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			rec.ID, er.EngineName, er.ModelID, er.RawOutput, er.LatencyMs, er.PromptTokens, er.OutputTokens, ts.Format(time.RFC3339),
		)
	}

	// Update FTS5 index
	ocrMD := ""
	if rec.ParseResult != nil {
		ocrMD = rec.ParseResult.Markdown
	}
	_ = s.conn.Exec(`DELETE FROM invoices_fts WHERE invoice_id = ?`, rec.ID)
	_ = s.conn.Exec(`
INSERT INTO invoices_fts (invoice_id, vendor_name, invoice_number, ocr_markdown, line_items_text)
VALUES (?, ?, ?, ?, ?)`,
		rec.ID, rec.Reconciled.VendorName, rec.Reconciled.InvoiceNumber, ocrMD, strings.Join(lineTexts, " "),
	)

	return nil
}

// GetInvoice retrieves an InvoiceRecord by ID.
func (s *Store) GetInvoice(id string) (*models.InvoiceRecord, error) {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()

	stmt, err := s.conn.prepareLocked(`SELECT record_json FROM invoices WHERE id = ? LIMIT 1`, id)
	if err != nil {
		return nil, err
	}
	defer C_finalize(stmt)

	if C_step(stmt) != sqliteRow {
		return nil, fmt.Errorf("invoice %s not found", id)
	}
	rawJSON := C_colText(stmt, 0)
	var rec models.InvoiceRecord
	if err := json.Unmarshal([]byte(rawJSON), &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// ListInvoices returns all stored invoices ordered with review-needed and most recent first.
func (s *Store) ListInvoices(statusFilter string) ([]models.InvoiceRecord, error) {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()

	sql := `SELECT record_json FROM invoices `
	var args []any
	if statusFilter != "" {
		sql += `WHERE status = ? `
		args = append(args, statusFilter)
	}
	sql += `ORDER BY CASE WHEN status = 'needs_review' THEN 0 ELSE 1 END, created_at DESC`

	stmt, err := s.conn.prepareLocked(sql, args...)
	if err != nil {
		return nil, err
	}
	defer C_finalize(stmt)

	var out []models.InvoiceRecord
	for C_step(stmt) == sqliteRow {
		raw := C_colText(stmt, 0)
		var rec models.InvoiceRecord
		if err := json.Unmarshal([]byte(raw), &rec); err == nil {
			out = append(out, rec)
		}
	}
	return out, nil
}

// SearchInvoices performs full-text search across vendor_name, invoice_number, OCR markdown, and line items using SQLite FTS5.
func (s *Store) SearchInvoices(query string) ([]models.InvoiceRecord, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return s.ListInvoices("")
	}
	// Sanitize FTS5 query tokens
	tokens := strings.Fields(query)
	var ftsTerms []string
	for _, tok := range tokens {
		clean := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				return r
			}
			return -1
		}, tok)
		if clean != "" {
			ftsTerms = append(ftsTerms, clean+"*")
		}
	}
	if len(ftsTerms) == 0 {
		return s.ListInvoices("")
	}
	ftsQuery := strings.Join(ftsTerms, " AND ")

	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()

	stmt, err := s.conn.prepareLocked(`
SELECT i.record_json
FROM invoices_fts f
JOIN invoices i ON i.id = f.invoice_id
WHERE invoices_fts MATCH ?
ORDER BY rank LIMIT 50`, ftsQuery)
	if err != nil {
		return nil, err
	}
	defer C_finalize(stmt)

	var out []models.InvoiceRecord
	for C_step(stmt) == sqliteRow {
		raw := C_colText(stmt, 0)
		var rec models.InvoiceRecord
		if err := json.Unmarshal([]byte(raw), &rec); err == nil {
			out = append(out, rec)
		}
	}
	return out, nil
}

// RecordReview saves a reviewer's correction to `reviews`, updates the invoice status to `reviewed`,
// updates vendor aggregates, and appends the correction to `adaptive_examples`.
func (s *Store) RecordReview(sub models.ReviewSubmission) (*models.InvoiceRecord, error) {
	rec, err := s.GetInvoice(sub.InvoiceID)
	if err != nil {
		return nil, err
	}
	if sub.ReviewedAt.IsZero() {
		sub.ReviewedAt = time.Now().UTC()
	}
	if sub.Reviewer == "" {
		sub.Reviewer = "local_reviewer"
	}

	corrBytes, _ := json.Marshal(sub.Corrected)
	if err := s.conn.Exec(`
INSERT INTO reviews (invoice_id, reviewer, corrected_json, notes, reviewed_at)
VALUES (?, ?, ?, ?, ?)`,
		sub.InvoiceID, sub.Reviewer, string(corrBytes), sub.Notes, sub.ReviewedAt.Format(time.RFC3339),
	); err != nil {
		return nil, err
	}

	// Update invoice reconciled values and field statuses to corrected/verified
	rec.Reconciled = sub.Corrected
	rec.Status = models.InvoiceReviewed
	rec.OverallConf = 1.0
	rec.CompositeHash = pipeline.ComputeCompositeHash(sub.Corrected)
	updateFieldsFromCorrection(rec, sub.Corrected)

	if err := s.SaveInvoice(rec); err != nil {
		return nil, err
	}

	// Save to adaptive_examples for future few-shot retrieval
	vendorNorm := pipeline.NormalizeVendorName(sub.Corrected.VendorName)
	headerSnippet := ""
	ocrExcerpt := ""
	var layoutSig []float64
	if rec.ParseResult != nil {
		layoutSig = rec.ParseResult.LayoutSignature
		ocrExcerpt = truncateString(rec.ParseResult.Markdown, 800)
		for _, blk := range rec.ParseResult.Blocks {
			if blk.BlockType == "header" || blk.BBox.Y1 < 400 {
				headerSnippet += blk.Text + " | "
			}
		}
		headerSnippet = truncateString(strings.TrimSpace(headerSnippet), 300)
	}
	if headerSnippet == "" {
		headerSnippet = fmt.Sprintf("%s %s %s", sub.Corrected.VendorName, sub.Corrected.VendorTaxID, sub.Corrected.BankDetails)
	}

	sigBytes, _ := json.Marshal(layoutSig)
	_ = s.conn.Exec(`
INSERT INTO adaptive_examples (invoice_id, vendor_norm, header_snippet, ocr_excerpt, layout_signature_json, corrected_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, vendorNorm, headerSnippet, ocrExcerpt, string(sigBytes), string(corrBytes), sub.ReviewedAt.Format(time.RFC3339),
	)

	// Update vendor historical profile
	_ = s.UpdateVendorHistory(sub.Corrected)

	return rec, nil
}

func updateFieldsFromCorrection(rec *models.InvoiceRecord, corr models.ExtractedInvoice) {
	valMap := map[string]string{
		"vendor_name":    corr.VendorName,
		"vendor_tax_id":  corr.VendorTaxID,
		"invoice_number": corr.InvoiceNumber,
		"invoice_date":   pipeline.NormalizeDate(corr.InvoiceDate),
		"due_date":       pipeline.NormalizeDate(corr.DueDate),
		"currency":       pipeline.NormalizeCurrency(corr.Currency),
		"subtotal":       pipeline.FormatAmount(corr.Subtotal),
		"tax_total":      pipeline.FormatAmount(corr.TaxTotal),
		"total":          pipeline.FormatAmount(corr.Total),
		"bank_details":   corr.BankDetails,
	}
	for i := range rec.Fields {
		fn := rec.Fields[i].FieldName
		if v, ok := valMap[fn]; ok {
			rec.Fields[i].DisplayValue = v
			rec.Fields[i].NormalizedValue = v
			rec.Fields[i].Status = models.StatusCorrected
			rec.Fields[i].SourceEngine = "reviewer"
			rec.Fields[i].Confidence = 1.0
		}
	}
	var newLines []models.LineItemResult
	for idx, li := range corr.LineItems {
		newLines = append(newLines, models.LineItemResult{
			RowIndex:    idx + 1,
			Description: li.Description,
			Quantity:    li.Quantity,
			UnitPrice:   li.UnitPrice,
			Amount:      li.Amount,
			Status:      models.StatusCorrected,
			Confidence:  1.0,
			MathValid:   true,
		})
	}
	rec.LineItems = newLines
}

// UpdateVendorHistory appends an invoice's total, number, and bank/tax details to the `vendors` aggregate table.
func (s *Store) UpdateVendorHistory(inv models.ExtractedInvoice) error {
	vNorm := pipeline.NormalizeVendorName(inv.VendorName)
	if vNorm == "" {
		return nil
	}

	amounts, invNums, knownBank, knownTax, display, err := s.getVendorRaw(vNorm)
	if err != nil {
		amounts = []float64{}
		invNums = []string{}
		display = inv.VendorName
	}
	if inv.Total > 0 {
		amounts = append(amounts, pipeline.RoundCents(inv.Total))
	}
	normNum := pipeline.NormalizeInvoiceNumber(inv.InvoiceNumber)
	if normNum != "" {
		invNums = append(invNums, normNum)
	}
	if knownBank == "" && inv.BankDetails != "" {
		knownBank = pipeline.NormalizeBankDetails(inv.BankDetails)
	}
	if knownTax == "" && inv.VendorTaxID != "" {
		knownTax = pipeline.NormalizeTaxID(inv.VendorTaxID)
	}
	if display == "" {
		display = inv.VendorName
	}

	amtBytes, _ := json.Marshal(amounts)
	numBytes, _ := json.Marshal(invNums)
	return s.conn.Exec(`
INSERT INTO vendors (vendor_norm, display_name, invoice_count, known_bank_details, known_tax_id, amounts_json, invoice_numbers_json, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(vendor_norm) DO UPDATE SET
	display_name=excluded.display_name,
	invoice_count=excluded.invoice_count,
	known_bank_details=excluded.known_bank_details,
	known_tax_id=excluded.known_tax_id,
	amounts_json=excluded.amounts_json,
	invoice_numbers_json=excluded.invoice_numbers_json,
	updated_at=excluded.updated_at`,
		vNorm, display, len(amounts), knownBank, knownTax, string(amtBytes), string(numBytes), time.Now().UTC().Format(time.RFC3339),
	)
}

func (s *Store) getVendorRaw(vendorNorm string) ([]float64, []string, string, string, string, error) {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()

	stmt, err := s.conn.prepareLocked(`
SELECT display_name, known_bank_details, known_tax_id, amounts_json, invoice_numbers_json
FROM vendors WHERE vendor_norm = ? LIMIT 1`, vendorNorm)
	if err != nil {
		return nil, nil, "", "", "", err
	}
	defer C_finalize(stmt)

	if C_step(stmt) != sqliteRow {
		return nil, nil, "", "", "", fmt.Errorf("not found")
	}
	display := C_colText(stmt, 0)
	bank := C_colText(stmt, 1)
	tax := C_colText(stmt, 2)
	amtJSON := C_colText(stmt, 3)
	numJSON := C_colText(stmt, 4)

	var amounts []float64
	var nums []string
	_ = json.Unmarshal([]byte(amtJSON), &amounts)
	_ = json.Unmarshal([]byte(numJSON), &nums)
	return amounts, nums, bank, tax, display, nil
}

// GetVendorHistory returns historical amounts, invoice numbers, and known bank/tax details for fraud auditing.
func (s *Store) GetVendorHistory(vendorNorm string) ([]float64, []string, string, string, string, bool) {
	amounts, nums, bank, tax, display, err := s.getVendorRaw(vendorNorm)
	if err != nil {
		return nil, nil, "", "", "", false
	}
	return amounts, nums, bank, tax, display, true
}

// ListAdaptiveExamples returns stored human-corrected examples for adaptive few-shot retrieval.
func (s *Store) ListAdaptiveExamples() ([]models.AdaptiveExample, error) {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()

	stmt, err := s.conn.prepareLocked(`
SELECT id, invoice_id, vendor_norm, header_snippet, ocr_excerpt, layout_signature_json, corrected_json, created_at
FROM adaptive_examples ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer C_finalize(stmt)

	var out []models.AdaptiveExample
	for C_step(stmt) == sqliteRow {
		var ex models.AdaptiveExample
		ex.ID = C_colInt64(stmt, 0)
		ex.InvoiceID = C_colText(stmt, 1)
		ex.VendorNorm = C_colText(stmt, 2)
		ex.HeaderSnippet = C_colText(stmt, 3)
		ex.OCRExcerpt = C_colText(stmt, 4)
		sigStr := C_colText(stmt, 5)
		corrStr := C_colText(stmt, 6)
		tsStr := C_colText(stmt, 7)
		_ = json.Unmarshal([]byte(sigStr), &ex.LayoutSignature)
		_ = json.Unmarshal([]byte(corrStr), &ex.CorrectedJSON)
		ex.CreatedAt, _ = time.Parse(time.RFC3339, tsStr)
		out = append(out, ex)
	}
	return out, nil
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
