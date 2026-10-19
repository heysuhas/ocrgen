package pipeline

import (
	"fmt"
	"math"
	"strings"

	"ocrgen/internal/models"
)

// ValidateAndGround performs OCR text/bbox grounding and decimal arithmetic checks per Spec §4.3b.
// It updates each field's Grounded flag, Evidence bounding box, Confidence, and returns the ValidationReport
// and the final InvoiceStatus (auto_accepted vs needs_review).
func ValidateAndGround(
	reconciled models.ExtractedInvoice,
	fields []models.FieldResult,
	lineItems []models.LineItemResult,
	parseRes *models.DocumentParseResult,
	tol float64,
) ([]models.FieldResult, []models.LineItemResult, models.ValidationReport, models.InvoiceStatus) {
	if tol <= 0 {
		tol = 0.02
	}

	fullOCRLower := ""
	var blocks []models.ParseBlock
	if parseRes != nil {
		fullOCRLower = strings.ToLower(parseRes.Markdown)
		blocks = parseRes.Blocks
	}

	groundedCnt := 0
	populatedCnt := 0
	allCriticalVerified := true
	allCriticalGrounded := true

	for i := range fields {
		f := &fields[i]
		if f.DisplayValue == "" && f.NormalizedValue == "" {
			if f.Critical {
				allCriticalVerified = false
				allCriticalGrounded = false
			}
			continue
		}
		populatedCnt++
		grounded, bbox := findEvidenceInBlocks(f.FieldName, f.DisplayValue, f.NormalizedValue, fullOCRLower, blocks)
		f.Grounded = grounded
		f.Evidence = bbox
		if grounded {
			groundedCnt++
		} else if parseRes != nil && fullOCRLower != "" {
			// Lower confidence when value is not grounded in OCR text
			f.Confidence = math.Max(0.25, f.Confidence*0.75)
			if f.Notes == "" {
				f.Notes = "Value not directly grounded in OCR blocks"
			}
		}

		if f.Critical {
			if f.Status != models.StatusVerified && f.Status != models.StatusCorrected {
				allCriticalVerified = false
			}
			if !f.Grounded && parseRes != nil && fullOCRLower != "" {
				allCriticalGrounded = false
			}
		}
	}

	// Ground line items to OCR table/text blocks
	for i := range lineItems {
		li := &lineItems[i]
		amtStr := FormatAmount(li.Amount)
		_, bbox := findEvidenceInBlocks("line_item", li.Description, amtStr, fullOCRLower, blocks)
		li.Evidence = bbox
	}

	// Arithmetic Validation Checks (§4.3b)
	var checks []models.ArithmeticCheck
	allMathPassed := true

	// Check 1: Each line item qty * unit_price ≈ amount
	var lineSum float64
	for idx, li := range lineItems {
		lineSum += RoundCents(li.Amount)
		if li.Quantity > 0 && li.UnitPrice > 0 {
			expectedAmt := RoundCents(li.Quantity * li.UnitPrice)
			actualAmt := RoundCents(li.Amount)
			diff := math.Abs(expectedAmt - actualAmt)
			passed := diff <= tol
			if !passed {
				allMathPassed = false
			}
			checks = append(checks, models.ArithmeticCheck{
				CheckName:  fmt.Sprintf("line_%d_qty_x_unit_price", idx+1),
				Passed:     passed,
				Expected:   expectedAmt,
				Actual:     actualAmt,
				Difference: RoundCents(diff),
				Tolerance:  tol,
				Details:    fmt.Sprintf("Line %d (%s): %.2f * %.2f = %.2f vs %.2f", idx+1, li.Description, li.Quantity, li.UnitPrice, expectedAmt, actualAmt),
			})
		}
	}
	lineSum = RoundCents(lineSum)

	// Check 2: Sum of line items ≈ subtotal (when line items and subtotal exist)
	if len(lineItems) > 0 && reconciled.Subtotal > 0 {
		actualSub := RoundCents(reconciled.Subtotal)
		diff := math.Abs(lineSum - actualSub)
		passed := diff <= tol
		if !passed {
			allMathPassed = false
		}
		checks = append(checks, models.ArithmeticCheck{
			CheckName:  "sum_lines_eq_subtotal",
			Passed:     passed,
			Expected:   lineSum,
			Actual:     actualSub,
			Difference: RoundCents(diff),
			Tolerance:  tol,
			Details:    fmt.Sprintf("Sum of %d line items (%.2f) vs Subtotal (%.2f)", len(lineItems), lineSum, actualSub),
		})
	}

	// Check 3: subtotal + tax_total ≈ total
	if reconciled.Subtotal > 0 || reconciled.TaxTotal > 0 {
		expectedTot := RoundCents(reconciled.Subtotal + reconciled.TaxTotal)
		actualTot := RoundCents(reconciled.Total)
		diff := math.Abs(expectedTot - actualTot)
		passed := diff <= tol
		if !passed {
			allMathPassed = false
		}
		checks = append(checks, models.ArithmeticCheck{
			CheckName:  "subtotal_plus_tax_eq_total",
			Passed:     passed,
			Expected:   expectedTot,
			Actual:     actualTot,
			Difference: RoundCents(diff),
			Tolerance:  tol,
			Details:    fmt.Sprintf("Subtotal (%.2f) + Tax (%.2f) = %.2f vs Total (%.2f)", reconciled.Subtotal, reconciled.TaxTotal, expectedTot, actualTot),
		})
	}

	// If arithmetic fails, lower confidence on total/subtotal fields and force review
	if !allMathPassed {
		for i := range fields {
			if fields[i].FieldName == "total" || fields[i].FieldName == "subtotal" || fields[i].FieldName == "tax_total" {
				fields[i].Confidence = math.Min(fields[i].Confidence, 0.55)
				if fields[i].Notes == "" {
					fields[i].Notes = "Arithmetic validation discrepancy detected"
				}
			}
		}
	}

	groundingRate := 1.0
	if populatedCnt > 0 {
		groundingRate = float64(groundedCnt) / float64(populatedCnt)
	}

	report := models.ValidationReport{
		AllCriticalVerified: allCriticalVerified,
		AllCriticalGrounded: allCriticalGrounded,
		ArithmeticPassed:    allMathPassed,
		Checks:              checks,
		GroundingRate:       groundingRate,
	}

	invStatus := models.InvoiceNeedsReview
	if allCriticalVerified && allCriticalGrounded && allMathPassed {
		invStatus = models.InvoiceAutoAccepted
	}

	return fields, lineItems, report, invStatus
}

func stripPunct(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func findEvidenceInBlocks(
	fieldName, displayVal, normVal, fullOCRLower string,
	blocks []models.ParseBlock,
) (bool, *models.BoundingBox) {
	candidates := buildSearchCandidates(fieldName, displayVal, normVal)
	if len(candidates) == 0 {
		return false, nil
	}

	for _, blk := range blocks {
		blkLower := strings.ToLower(blk.Text)
		blkAlnum := stripPunct(blkLower)
		for _, cand := range candidates {
			if cand == "" {
				continue
			}
			candAlnum := stripPunct(cand)
			if strings.Contains(blkLower, cand) || (len(candAlnum) >= 3 && strings.Contains(blkAlnum, candAlnum)) {
				bboxCopy := blk.BBox
				return true, &bboxCopy
			}
		}
	}

	// Check full markdown text if block-level didn't match
	if fullOCRLower != "" {
		ocrAlnum := stripPunct(fullOCRLower)
		for _, cand := range candidates {
			candAlnum := stripPunct(cand)
			if strings.Contains(fullOCRLower, cand) || (len(candAlnum) >= 3 && strings.Contains(ocrAlnum, candAlnum)) {
				return true, nil
			}
		}
	}

	return false, nil
}

func buildSearchCandidates(fieldName, displayVal, normVal string) []string {
	var out []string
	d := strings.ToLower(strings.TrimSpace(displayVal))
	n := strings.ToLower(strings.TrimSpace(normVal))
	if d != "" {
		out = append(out, d)
	}
	if n != "" && n != d {
		out = append(out, n)
	}
	// For vendor names, also match primary multi-char tokens
	if fieldName == "vendor_name" && n != "" {
		parts := strings.Fields(n)
		if len(parts) > 0 && len(parts[0]) >= 3 {
			out = append(out, parts[0])
		}
	}
	// For dates (YYYY-MM-DD), also search common slash/dot variants
	if (fieldName == "invoice_date" || fieldName == "due_date") && len(n) == 10 && n[4] == '-' && n[7] == '-' {
		yyyy, mm, dd := n[0:4], n[5:7], n[8:10]
		out = append(out,
			fmt.Sprintf("%s/%s/%s", mm, dd, yyyy),
			fmt.Sprintf("%s/%s/%s", dd, mm, yyyy),
			fmt.Sprintf("%s.%s.%s", dd, mm, yyyy),
			fmt.Sprintf("%s/%s/%s", yyyy, mm, dd),
		)
	}
	return out
}
