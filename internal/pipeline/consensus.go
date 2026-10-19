package pipeline

import (
	"fmt"
	"math"
	"strings"

	"ocrgen/internal/models"
)

// ConsensusConfig controls fuzzy thresholds and amount tolerances during Engine A vs Engine B reconciliation.
type ConsensusConfig struct {
	VendorFuzzyThreshold float64
	DescFuzzyThreshold   float64
	AmountTolerance      float64
}

func DefaultConsensusConfig() ConsensusConfig {
	return ConsensusConfig{
		VendorFuzzyThreshold: 0.85,
		DescFuzzyThreshold:   0.78,
		AmountTolerance:      0.02,
	}
}

// ConsensusOutput holds the reconciled invoice, per-field results, reconciled line items, and agreement rate.
type ConsensusOutput struct {
	Reconciled    models.ExtractedInvoice
	Fields        []models.FieldResult
	LineItems     []models.LineItemResult
	AgreementRate float64
	OverallConf   float64
}

// Reconcile runs field-by-field and row-by-row consensus between Engine A and Engine B outputs.
// If Engine B is nil (e.g. Phase 1 baseline mode), all populated Engine A fields are marked single_source.
func Reconcile(a *models.ExtractedInvoice, b *models.ExtractedInvoice, cfg ConsensusConfig) ConsensusOutput {
	if a == nil && b == nil {
		return ConsensusOutput{}
	}
	if a == nil {
		a = &models.ExtractedInvoice{}
	}
	singleEngineMode := (b == nil)
	if b == nil {
		b = &models.ExtractedInvoice{}
	}

	var fields []models.FieldResult
	var reconciled models.ExtractedInvoice

	// 1. vendor_name (critical, fuzzy allowed >= VendorFuzzyThreshold)
	fVendor := compareStringField(
		"vendor_name",
		a.VendorName,
		b.VendorName,
		NormalizeVendorName(a.VendorName),
		NormalizeVendorName(b.VendorName),
		cfg.VendorFuzzyThreshold,
		true,
		singleEngineMode,
	)
	reconciled.VendorName = fVendor.DisplayValue
	fields = append(fields, fVendor)

	// 2. vendor_tax_id (optional, exact after normalization)
	fTaxID := compareStringField(
		"vendor_tax_id",
		a.VendorTaxID,
		b.VendorTaxID,
		NormalizeTaxID(a.VendorTaxID),
		NormalizeTaxID(b.VendorTaxID),
		1.0,
		false,
		singleEngineMode,
	)
	reconciled.VendorTaxID = fTaxID.DisplayValue
	fields = append(fields, fTaxID)

	// 3. invoice_number (critical, exact after normalization)
	fInvNum := compareStringField(
		"invoice_number",
		a.InvoiceNumber,
		b.InvoiceNumber,
		NormalizeInvoiceNumber(a.InvoiceNumber),
		NormalizeInvoiceNumber(b.InvoiceNumber),
		1.0,
		true,
		singleEngineMode,
	)
	reconciled.InvoiceNumber = fInvNum.DisplayValue
	fields = append(fields, fInvNum)

	// 4. invoice_date (critical, exact ISO-8601 after normalization)
	normDateA := NormalizeDate(a.InvoiceDate)
	normDateB := NormalizeDate(b.InvoiceDate)
	fDate := compareStringField(
		"invoice_date",
		a.InvoiceDate,
		b.InvoiceDate,
		normDateA,
		normDateB,
		1.0,
		true,
		singleEngineMode,
	)
	if fDate.NormalizedValue != "" {
		reconciled.InvoiceDate = fDate.NormalizedValue
		fDate.DisplayValue = fDate.NormalizedValue
	} else {
		reconciled.InvoiceDate = fDate.DisplayValue
	}
	fields = append(fields, fDate)

	// 5. due_date (optional, exact ISO-8601 after normalization)
	normDueA := NormalizeDate(a.DueDate)
	normDueB := NormalizeDate(b.DueDate)
	fDue := compareStringField(
		"due_date",
		a.DueDate,
		b.DueDate,
		normDueA,
		normDueB,
		1.0,
		false,
		singleEngineMode,
	)
	if fDue.NormalizedValue != "" {
		reconciled.DueDate = fDue.NormalizedValue
		fDue.DisplayValue = fDue.NormalizedValue
	}
	fields = append(fields, fDue)

	// 6. currency (non-critical, exact ISO code after normalization)
	normCurrA := NormalizeCurrency(a.Currency)
	normCurrB := NormalizeCurrency(b.Currency)
	fCurr := compareStringField(
		"currency",
		a.Currency,
		b.Currency,
		normCurrA,
		normCurrB,
		1.0,
		false,
		singleEngineMode,
	)
	if fCurr.NormalizedValue != "" {
		reconciled.Currency = fCurr.NormalizedValue
		fCurr.DisplayValue = fCurr.NormalizedValue
	}
	fields = append(fields, fCurr)

	// 7. subtotal (numeric)
	fSub, valSub := compareAmountField("subtotal", a.Subtotal, b.Subtotal, cfg.AmountTolerance, false, singleEngineMode)
	reconciled.Subtotal = valSub
	fields = append(fields, fSub)

	// 8. tax_total (numeric)
	fTax, valTax := compareAmountField("tax_total", a.TaxTotal, b.TaxTotal, cfg.AmountTolerance, false, singleEngineMode)
	reconciled.TaxTotal = valTax
	fields = append(fields, fTax)

	// 9. total (critical numeric)
	fTot, valTot := compareAmountField("total", a.Total, b.Total, cfg.AmountTolerance, true, singleEngineMode)
	reconciled.Total = valTot
	fields = append(fields, fTot)

	// 10. bank_details (optional)
	fBank := compareStringField(
		"bank_details",
		a.BankDetails,
		b.BankDetails,
		NormalizeBankDetails(a.BankDetails),
		NormalizeBankDetails(b.BankDetails),
		0.90,
		false,
		singleEngineMode,
	)
	reconciled.BankDetails = fBank.DisplayValue
	fields = append(fields, fBank)

	// 11. Line items consensus
	lineResults, recLines := reconcileLineItems(a.LineItems, b.LineItems, cfg, singleEngineMode)
	reconciled.LineItems = recLines

	// Compute agreement rate & overall confidence across populated fields
	agreed := 0
	populated := 0
	confSum := 0.0
	for _, f := range fields {
		if f.NormalizedA != "" || f.NormalizedB != "" {
			populated++
			confSum += f.Confidence
			if f.Status == models.StatusVerified {
				agreed++
			}
		}
	}
	agreementRate := 0.0
	overallConf := 0.0
	if populated > 0 {
		if singleEngineMode {
			agreementRate = 0.0
		} else {
			agreementRate = float64(agreed) / float64(populated)
		}
		overallConf = confSum / float64(populated)
	}

	return ConsensusOutput{
		Reconciled:    reconciled,
		Fields:        fields,
		LineItems:     lineResults,
		AgreementRate: agreementRate,
		OverallConf:   overallConf,
	}
}

func compareStringField(
	name, rawA, rawB, normA, normB string,
	fuzzyThresh float64,
	critical bool,
	singleEngineMode bool,
) models.FieldResult {
	rawA = strings.TrimSpace(rawA)
	rawB = strings.TrimSpace(rawB)
	res := models.FieldResult{
		FieldName:    name,
		EngineAValue: rawA,
		EngineBValue: rawB,
		NormalizedA:  normA,
		NormalizedB:  normB,
		Critical:     critical,
	}

	if singleEngineMode {
		if normA != "" {
			res.NormalizedValue = normA
			res.DisplayValue = rawA
			res.SourceEngine = "engine_a"
			res.Similarity = 1.0
			res.Confidence = 0.78
			res.Status = models.StatusSingleSource
		} else {
			res.Status = models.StatusUnverified
			res.Confidence = 0.0
		}
		return res
	}

	if normA == "" && normB == "" {
		res.Status = models.StatusVerified // Both agree optional field is empty
		if critical {
			res.Status = models.StatusUnverified
			res.Confidence = 0.0
		} else {
			res.Confidence = 0.95
		}
		res.SourceEngine = "consensus"
		res.Similarity = 1.0
		return res
	}

	if normA != "" && normB == "" {
		res.NormalizedValue = normA
		res.DisplayValue = rawA
		res.SourceEngine = "engine_a"
		res.Similarity = 0.0
		res.Confidence = 0.65
		res.Status = models.StatusSingleSource
		if critical {
			res.Notes = "Engine B returned empty for critical field"
		}
		return res
	}

	if normA == "" && normB != "" {
		res.NormalizedValue = normB
		res.DisplayValue = rawB
		res.SourceEngine = "engine_b"
		res.Similarity = 0.0
		res.Confidence = 0.65
		res.Status = models.StatusSingleSource
		if critical {
			res.Notes = "Engine A returned empty for critical field"
		}
		return res
	}

	sim := StringSimilarity(normA, normB)
	res.Similarity = sim
	if normA == normB || sim >= fuzzyThresh {
		res.NormalizedValue = normA
		res.DisplayValue = rawA
		if len(rawB) > len(rawA) && sim < 1.0 {
			res.DisplayValue = rawB
		}
		res.SourceEngine = "consensus"
		res.Status = models.StatusVerified
		res.Confidence = 0.90 + 0.09*sim
		return res
	}

	// Conflict between Engine A and Engine B
	res.NormalizedValue = normA
	res.DisplayValue = rawA
	res.SourceEngine = "engine_a"
	res.Status = models.StatusConflict
	res.Confidence = 0.45 * sim
	res.Notes = fmt.Sprintf("Conflict: Engine A='%s' vs Engine B='%s' (sim=%.2f)", rawA, rawB, sim)
	return res
}

func compareAmountField(
	name string,
	valA, valB, tol float64,
	critical bool,
	singleEngineMode bool,
) (models.FieldResult, float64) {
	valA = RoundCents(valA)
	valB = RoundCents(valB)
	strA := ""
	if valA != 0 || name == "tax_total" {
		strA = FormatAmount(valA)
	}
	strB := ""
	if valB != 0 || name == "tax_total" {
		strB = FormatAmount(valB)
	}

	res := models.FieldResult{
		FieldName:    name,
		EngineAValue: strA,
		EngineBValue: strB,
		NormalizedA:  strA,
		NormalizedB:  strB,
		Critical:     critical,
	}

	if singleEngineMode {
		res.NormalizedValue = strA
		res.DisplayValue = strA
		res.SourceEngine = "engine_a"
		res.Similarity = 1.0
		res.Confidence = 0.78
		res.Status = models.StatusSingleSource
		if critical && valA == 0 {
			res.Status = models.StatusUnverified
			res.Confidence = 0.0
		}
		return res, valA
	}

	if valA == 0 && valB == 0 && critical {
		res.Status = models.StatusUnverified
		res.Confidence = 0.0
		return res, 0
	}

	diff := math.Abs(valA - valB)
	if diff <= tol {
		res.NormalizedValue = FormatAmount(valA)
		res.DisplayValue = FormatAmount(valA)
		res.SourceEngine = "consensus"
		res.Similarity = 1.0
		res.Confidence = 0.98
		res.Status = models.StatusVerified
		return res, valA
	}

	if valA > 0 && valB == 0 && name != "tax_total" {
		res.NormalizedValue = strA
		res.DisplayValue = strA
		res.SourceEngine = "engine_a"
		res.Status = models.StatusSingleSource
		res.Confidence = 0.65
		return res, valA
	}
	if valB > 0 && valA == 0 && name != "tax_total" {
		res.NormalizedValue = strB
		res.DisplayValue = strB
		res.SourceEngine = "engine_b"
		res.Status = models.StatusSingleSource
		res.Confidence = 0.65
		return res, valB
	}

	// Conflict on amount
	res.NormalizedValue = strA
	res.DisplayValue = strA
	res.SourceEngine = "engine_a"
	res.Status = models.StatusConflict
	res.Confidence = 0.35
	res.Notes = fmt.Sprintf("Amount mismatch: Engine A=%s vs Engine B=%s (diff=%.2f)", strA, strB, diff)
	return res, valA
}

// reconcileLineItems matches rows by (amount within tolerance, then description similarity) per Spec §4.6.
func reconcileLineItems(
	itemsA, itemsB []models.RawLineItem,
	cfg ConsensusConfig,
	singleEngineMode bool,
) ([]models.LineItemResult, []models.RawLineItem) {
	var results []models.LineItemResult
	var reconciled []models.RawLineItem

	if singleEngineMode {
		for idx, item := range itemsA {
			itemCopy := item
			mathOK := math.Abs(RoundCents(item.Quantity*item.UnitPrice)-RoundCents(item.Amount)) <= cfg.AmountTolerance || item.Quantity == 0
			results = append(results, models.LineItemResult{
				RowIndex:     idx + 1,
				Description:  item.Description,
				Quantity:     item.Quantity,
				UnitPrice:    RoundCents(item.UnitPrice),
				Amount:       RoundCents(item.Amount),
				EngineAMatch: &itemCopy,
				Status:       models.StatusSingleSource,
				Confidence:   0.78,
				MathValid:    mathOK,
			})
			reconciled = append(reconciled, item)
		}
		return results, reconciled
	}

	usedB := make([]bool, len(itemsB))
	rowIdx := 1

	for i := range itemsA {
		aItem := itemsA[i]
		bestJ := -1
		bestScore := -1.0

		for j := range itemsB {
			if usedB[j] {
				continue
			}
			bItem := itemsB[j]
			amtDiff := math.Abs(RoundCents(aItem.Amount) - RoundCents(bItem.Amount))
			descSim := StringSimilarity(NormalizeFreeText(aItem.Description), NormalizeFreeText(bItem.Description))

			score := descSim
			if amtDiff <= cfg.AmountTolerance {
				score += 1.0 // Prioritize matching amount first, then description similarity per Spec §4.6
			}
			if score > bestScore && (amtDiff <= cfg.AmountTolerance || descSim >= 0.55) {
				bestScore = score
				bestJ = j
			}
		}

		aCopy := aItem
		if bestJ >= 0 {
			usedB[bestJ] = true
			bCopy := itemsB[bestJ]
			amtMatch := math.Abs(RoundCents(aCopy.Amount)-RoundCents(bCopy.Amount)) <= cfg.AmountTolerance
			qtyMatch := math.Abs(aCopy.Quantity-bCopy.Quantity) <= 0.01
			descSim := StringSimilarity(NormalizeFreeText(aCopy.Description), NormalizeFreeText(bCopy.Description))

			status := models.StatusVerified
			conf := 0.96
			if !amtMatch || !qtyMatch || descSim < cfg.DescFuzzyThreshold {
				status = models.StatusConflict
				conf = 0.50
			}
			mathOK := math.Abs(RoundCents(aCopy.Quantity*aCopy.UnitPrice)-RoundCents(aCopy.Amount)) <= cfg.AmountTolerance || aCopy.Quantity == 0
			results = append(results, models.LineItemResult{
				RowIndex:     rowIdx,
				Description:  aCopy.Description,
				Quantity:     aCopy.Quantity,
				UnitPrice:    RoundCents(aCopy.UnitPrice),
				Amount:       RoundCents(aCopy.Amount),
				EngineAMatch: &aCopy,
				EngineBMatch: &bCopy,
				Status:       status,
				Confidence:   conf,
				MathValid:    mathOK,
			})
			reconciled = append(reconciled, aCopy)
			rowIdx++
		} else {
			mathOK := math.Abs(RoundCents(aCopy.Quantity*aCopy.UnitPrice)-RoundCents(aCopy.Amount)) <= cfg.AmountTolerance || aCopy.Quantity == 0
			results = append(results, models.LineItemResult{
				RowIndex:     rowIdx,
				Description:  aCopy.Description,
				Quantity:     aCopy.Quantity,
				UnitPrice:    RoundCents(aCopy.UnitPrice),
				Amount:       RoundCents(aCopy.Amount),
				EngineAMatch: &aCopy,
				Status:       models.StatusSingleSource,
				Confidence:   0.62,
				MathValid:    mathOK,
			})
			reconciled = append(reconciled, aCopy)
			rowIdx++
		}
	}

	// Remaining unmatched items from Engine B
	for j := range itemsB {
		if usedB[j] {
			continue
		}
		bCopy := itemsB[j]
		mathOK := math.Abs(RoundCents(bCopy.Quantity*bCopy.UnitPrice)-RoundCents(bCopy.Amount)) <= cfg.AmountTolerance || bCopy.Quantity == 0
		results = append(results, models.LineItemResult{
			RowIndex:     rowIdx,
			Description:  bCopy.Description,
			Quantity:     bCopy.Quantity,
			UnitPrice:    RoundCents(bCopy.UnitPrice),
			Amount:       RoundCents(bCopy.Amount),
			EngineBMatch: &bCopy,
			Status:       models.StatusSingleSource,
			Confidence:   0.60,
			MathValid:    mathOK,
		})
		reconciled = append(reconciled, bCopy)
		rowIdx++
	}

	return results, reconciled
}
