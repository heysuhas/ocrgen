package fraud

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"time"

	"ocrgen/internal/ingest"
	"ocrgen/internal/models"
	"ocrgen/internal/pipeline"
)

// Config defines statistical thresholds and cold-start guards for the Fraud Audit module.
type Config struct {
	MinVendorHistory int     // Default 20 per Spec §4.8
	RobustZThreshold float64 // Default 3.5 (Modified Z-score threshold)
	PHashMaxDistance int     // Default 6 bits out of 64-bit dHash
	MinBenfordSample int     // Default 50 amounts for population-level Benford test
	RoundTotalMin    float64 // Default 500.00
	SeqGapMaxJump    int     // Default 250
}

func DefaultConfig() Config {
	return Config{
		MinVendorHistory: 20,
		RobustZThreshold: 3.5,
		PHashMaxDistance: 6,
		MinBenfordSample: 50,
		RoundTotalMin:    500.0,
		SeqGapMaxJump:    250,
	}
}

// Auditor runs per-invoice and batch-level fraud checks.
type Auditor struct {
	cfg Config
}

func NewAuditor(cfg Config) *Auditor {
	if cfg.MinVendorHistory <= 0 {
		cfg.MinVendorHistory = 20
	}
	if cfg.RobustZThreshold <= 0 {
		cfg.RobustZThreshold = 3.5
	}
	if cfg.PHashMaxDistance <= 0 {
		cfg.PHashMaxDistance = 6
	}
	if cfg.MinBenfordSample <= 0 {
		cfg.MinBenfordSample = 50
	}
	if cfg.RoundTotalMin <= 0 {
		cfg.RoundTotalMin = 500.0
	}
	if cfg.SeqGapMaxJump <= 0 {
		cfg.SeqGapMaxJump = 250
	}
	return &Auditor{cfg: cfg}
}

// AuditInvoice evaluates a candidate invoice against existing invoices and historical vendor stats.
func (a *Auditor) AuditInvoice(
	rec *models.InvoiceRecord,
	existing []models.InvoiceRecord,
	vendorAmounts []float64,
	vendorInvNums []string,
	knownBankNorm string,
	knownTaxNorm string,
) []models.FraudFlag {
	var flags []models.FraudFlag
	now := time.Now().UTC()

	vNorm := pipeline.NormalizeVendorName(rec.Reconciled.VendorName)
	invNumNorm := pipeline.NormalizeInvoiceNumber(rec.Reconciled.InvoiceNumber)
	dateNorm := pipeline.NormalizeDate(rec.Reconciled.InvoiceDate)
	compHash := pipeline.ComputeCompositeHash(rec.Reconciled)

	// 1. Duplicate Detection (Exact SHA-256, Composite Key Hash, Perceptual Hash, Fuzzy Match)
	for _, prev := range existing {
		if prev.ID == rec.ID {
			continue
		}
		if rec.FileHashSHA256 != "" && prev.FileHashSHA256 == rec.FileHashSHA256 {
			flags = append(flags, models.FraudFlag{
				InvoiceID:   rec.ID,
				FlagType:    "duplicate_exact",
				Severity:    "high",
				Reason:      fmt.Sprintf("Exact file content hash (SHA-256) matches existing invoice %s (%s)", prev.ID, prev.Filename),
				MetricValue: 1.0,
				Threshold:   1.0,
				CreatedAt:   now,
			})
			break
		}
		if compHash != "" && prev.CompositeHash == compHash {
			flags = append(flags, models.FraudFlag{
				InvoiceID:   rec.ID,
				FlagType:    "duplicate_exact",
				Severity:    "high",
				Reason:      fmt.Sprintf("Identical composite key (vendor, invoice #, total, date) as invoice %s", prev.ID),
				MetricValue: 1.0,
				Threshold:   1.0,
				CreatedAt:   now,
			})
			break
		}
		if rec.PrimaryPHash != "" && prev.PrimaryPHash != "" {
			if dist, err := ingest.HammingDistanceHex(rec.PrimaryPHash, prev.PrimaryPHash); err == nil && dist <= a.cfg.PHashMaxDistance {
				flags = append(flags, models.FraudFlag{
					InvoiceID:   rec.ID,
					FlagType:    "duplicate_phash",
					Severity:    "high",
					Reason:      fmt.Sprintf("Page image perceptual hash within %d bits of invoice %s (near-identical visual scan)", dist, prev.ID),
					MetricValue: float64(dist),
					Threshold:   float64(a.cfg.PHashMaxDistance),
					CreatedAt:   now,
				})
				break
			}
		}

		// Fuzzy duplicate check: same vendor + very close invoice number + identical total
		prevVendor := pipeline.NormalizeVendorName(prev.Reconciled.VendorName)
		prevNum := pipeline.NormalizeInvoiceNumber(prev.Reconciled.InvoiceNumber)
		prevDate := pipeline.NormalizeDate(prev.Reconciled.InvoiceDate)
		if vNorm != "" && vNorm == prevVendor && math.Abs(rec.Reconciled.Total-prev.Reconciled.Total) <= 0.02 && rec.Reconciled.Total > 0 {
			numSim := pipeline.StringSimilarity(invNumNorm, prevNum)
			if (numSim >= 0.85 && invNumNorm != "") || (dateNorm != "" && dateNorm == prevDate) {
				flags = append(flags, models.FraudFlag{
					InvoiceID:   rec.ID,
					FlagType:    "duplicate_fuzzy",
					Severity:    "high",
					Reason:      fmt.Sprintf("Fuzzy duplicate of invoice %s: same vendor & total (%.2f) with similar invoice #/date (sim=%.2f)", prev.ID, rec.Reconciled.Total, numSim),
					MetricValue: numSim,
					Threshold:   0.85,
					CreatedAt:   now,
				})
				break
			}
		}
	}

	// 2. Vendor-level Outlier via Robust Z-Score (Median & MAD) with Cold-Start Guard
	if len(vendorAmounts) >= a.cfg.MinVendorHistory && rec.Reconciled.Total > 0 {
		stats := ComputeVendorStats(vNorm, rec.Reconciled.VendorName, vendorAmounts, vendorInvNums, knownBankNorm, knownTaxNorm, a.cfg.MinVendorHistory)
		robustZ := ComputeRobustZScore(rec.Reconciled.Total, stats.MedianTotal, stats.MADTotal, stats.MeanTotal, stats.StdDevTotal)
		if robustZ >= a.cfg.RobustZThreshold {
			flags = append(flags, models.FraudFlag{
				InvoiceID:   rec.ID,
				FlagType:    "vendor_outlier_z",
				Severity:    "high",
				Reason:      fmt.Sprintf("Total %.2f is a statistical outlier for vendor '%s' (Robust Z=%.2f >= %.2f; median=%.2f, MAD=%.2f, n=%d)", rec.Reconciled.Total, rec.Reconciled.VendorName, robustZ, a.cfg.RobustZThreshold, stats.MedianTotal, stats.MADTotal, len(vendorAmounts)),
				MetricValue: robustZ,
				Threshold:   a.cfg.RobustZThreshold,
				CreatedAt:   now,
			})
		}
	}

	// 3. Bank Details / Tax ID Change for Known Vendor
	currBank := pipeline.NormalizeBankDetails(rec.Reconciled.BankDetails)
	if knownBankNorm != "" && currBank != "" && currBank != knownBankNorm {
		flags = append(flags, models.FraudFlag{
			InvoiceID:   rec.ID,
			FlagType:    "rule_bank_change",
			Severity:    "high",
			Reason:      fmt.Sprintf("Vendor bank details changed from known baseline (%s -> %s); potential BEC/payment diversion fraud", knownBankNorm, currBank),
			MetricValue: 1.0,
			Threshold:   0.0,
			CreatedAt:   now,
		})
	}

	currTax := pipeline.NormalizeTaxID(rec.Reconciled.VendorTaxID)
	if knownTaxNorm != "" && currTax != "" && currTax != knownTaxNorm {
		flags = append(flags, models.FraudFlag{
			InvoiceID:   rec.ID,
			FlagType:    "rule_tax_id_change",
			Severity:    "medium",
			Reason:      fmt.Sprintf("Vendor tax ID (%s) differs from historical tax ID (%s)", currTax, knownTaxNorm),
			MetricValue: 1.0,
			Threshold:   0.0,
			CreatedAt:   now,
		})
	}

	// 4. Arithmetic Discrepancy Rule
	if !rec.Validation.ArithmeticPassed {
		for _, chk := range rec.Validation.Checks {
			if !chk.Passed {
				flags = append(flags, models.FraudFlag{
					InvoiceID:   rec.ID,
					FlagType:    "rule_math_mismatch",
					Severity:    "high",
					Reason:      fmt.Sprintf("Arithmetic mismatch (%s): %s", chk.CheckName, chk.Details),
					MetricValue: chk.Difference,
					Threshold:   chk.Tolerance,
					CreatedAt:   now,
				})
			}
		}
	}

	// 5. Round-Number Total Rule
	tot := pipeline.RoundCents(rec.Reconciled.Total)
	if tot >= a.cfg.RoundTotalMin && math.Mod(tot, 500.0) == 0 && rec.Reconciled.TaxTotal == 0 {
		flags = append(flags, models.FraudFlag{
			InvoiceID:   rec.ID,
			FlagType:    "rule_round_total",
			Severity:    "low",
			Reason:      fmt.Sprintf("Suspicious exact round-number invoice total (%.2f) with zero tax breakdown", tot),
			MetricValue: tot,
			Threshold:   a.cfg.RoundTotalMin,
			CreatedAt:   now,
		})
	}

	// 6. Weekend Issue Date Rule
	if dateNorm != "" {
		if dt, err := time.Parse("2006-01-02", dateNorm); err == nil {
			wd := dt.Weekday()
			if wd == time.Saturday || wd == time.Sunday {
				flags = append(flags, models.FraudFlag{
					InvoiceID:   rec.ID,
					FlagType:    "rule_weekend_date",
					Severity:    "low",
					Reason:      fmt.Sprintf("Invoice dated on a weekend (%s, %s)", dateNorm, wd.String()),
					MetricValue: float64(wd),
					Threshold:   5.0,
					CreatedAt:   now,
				})
			}
		}
	}

	// 7. Invoice Number Sequence Gap Rule
	if len(vendorInvNums) > 0 && invNumNorm != "" {
		if gap, ok := detectSequenceGap(invNumNorm, vendorInvNums, a.cfg.SeqGapMaxJump); ok {
			flags = append(flags, models.FraudFlag{
				InvoiceID:   rec.ID,
				FlagType:    "rule_seq_gap",
				Severity:    "medium",
				Reason:      fmt.Sprintf("Invoice number %s jumps by %d from vendor's recent sequence", rec.Reconciled.InvoiceNumber, gap),
				MetricValue: float64(gap),
				Threshold:   float64(a.cfg.SeqGapMaxJump),
				CreatedAt:   now,
			})
		}
	}

	return flags
}

// ComputeVendorStats calculates Median, MAD, Mean, and StdDev for a vendor's historical invoice totals.
func ComputeVendorStats(
	vendorNorm, displayName string,
	amounts []float64,
	invNums []string,
	knownBank, knownTax string,
	minReq int,
) models.VendorStats {
	if minReq <= 0 {
		minReq = 20
	}
	n := len(amounts)
	stats := models.VendorStats{
		VendorNorm:      vendorNorm,
		DisplayName:     displayName,
		InvoiceCount:    n,
		ColdStart:       n < minReq,
		MinHistoryReq:   minReq,
		KnownBankDetail: knownBank,
		KnownTaxID:      knownTax,
		RecentNumbers:   invNums,
	}
	if n == 0 {
		return stats
	}

	sorted := make([]float64, n)
	copy(sorted, amounts)
	sort.Float64s(sorted)

	median := percentileMedian(sorted)
	absDevs := make([]float64, n)
	var sum float64
	for i, v := range sorted {
		sum += v
		absDevs[i] = math.Abs(v - median)
	}
	sort.Float64s(absDevs)
	mad := percentileMedian(absDevs)
	mean := sum / float64(n)

	var sqDiff float64
	for _, v := range sorted {
		d := v - mean
		sqDiff += d * d
	}
	stdDev := math.Sqrt(sqDiff / float64(n))

	stats.MedianTotal = pipeline.RoundCents(median)
	stats.MADTotal = pipeline.RoundCents(mad)
	stats.MeanTotal = pipeline.RoundCents(mean)
	stats.StdDevTotal = pipeline.RoundCents(stdDev)
	return stats
}

// ComputeRobustZScore returns |x - median| / (1.4826 * MAD), falling back to standard Z-score if MAD == 0.
func ComputeRobustZScore(x, median, mad, mean, stdDev float64) float64 {
	denom := 1.4826 * mad
	if denom > 1e-6 {
		return math.Abs(x-median) / denom
	}
	if stdDev > 1e-6 {
		return math.Abs(x-mean) / stdDev
	}
	if math.Abs(x-median) > 1.0 {
		return 10.0
	}
	return 0.0
}

func percentileMedian(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return 0.5 * (sorted[n/2-1] + sorted[n/2])
}

var trailingDigitsRe = regexp.MustCompile(`(\d+)$`)

func detectSequenceGap(currNorm string, prevNorms []string, maxJump int) (int, bool) {
	m1 := trailingDigitsRe.FindStringSubmatch(currNorm)
	if len(m1) < 2 {
		return 0, false
	}
	currVal, err := strconv.Atoi(m1[1])
	if err != nil {
		return 0, false
	}
	maxPrev := -1
	for _, p := range prevNorms {
		m2 := trailingDigitsRe.FindStringSubmatch(p)
		if len(m2) >= 2 {
			if v, err := strconv.Atoi(m2[1]); err == nil && v > maxPrev {
				maxPrev = v
			}
		}
	}
	if maxPrev < 0 {
		return 0, false
	}
	gap := currVal - maxPrev
	if gap < 0 {
		gap = -gap
	}
	if gap > maxJump {
		return gap, true
	}
	return gap, false
}

// AnalyzeBenford runs a population/batch-level first-digit Benford's Law test per Spec §4.8.
// Never scored per single invoice; requires `minRequired` amounts to claim statistical validity.
func (a *Auditor) AnalyzeBenford(scope string, amounts []float64) models.BenfordReport {
	var counts [10]int
	validN := 0
	for _, amt := range amounts {
		d := leadingNonZeroDigit(amt)
		if d >= 1 && d <= 9 {
			counts[d]++
			validN++
		}
	}

	digits := make([]models.BenfordDigitStat, 0, 9)
	var chiSq float64
	var madSum float64

	for d := 1; d <= 9; d++ {
		expFreq := math.Log10(1.0 + 1.0/float64(d))
		obsCnt := counts[d]
		obsFreq := 0.0
		if validN > 0 {
			obsFreq = float64(obsCnt) / float64(validN)
			expCnt := expFreq * float64(validN)
			diffCnt := float64(obsCnt) - expCnt
			chiSq += (diffCnt * diffCnt) / expCnt
		}
		dev := math.Abs(obsFreq - expFreq)
		madSum += dev
		digits = append(digits, models.BenfordDigitStat{
			Digit:        d,
			ObservedCnt:  obsCnt,
			ObservedFreq: obsFreq,
			ExpectedFreq: expFreq,
			Deviation:    dev,
		})
	}

	mad := madSum / 9.0
	critVal := 15.507 // df=8, alpha=0.05
	sufficient := validN >= a.cfg.MinBenfordSample

	conformity := "insufficient_sample"
	suspicious := false
	if sufficient {
		switch {
		case mad <= 0.006:
			conformity = "close_conformity"
		case mad <= 0.012:
			conformity = "acceptable_conformity"
		case mad <= 0.015:
			conformity = "marginally_acceptable"
		default:
			conformity = "nonconformity"
		}
		if chiSq > critVal && mad > 0.015 {
			suspicious = true
		}
	}

	return models.BenfordReport{
		Scope:          scope,
		SampleSize:     validN,
		MinRequired:    a.cfg.MinBenfordSample,
		SufficientData: sufficient,
		ChiSquare:      math.Round(chiSq*1000) / 1000,
		CriticalValue:  critVal,
		MAD:            math.Round(mad*10000) / 10000,
		Conformity:     conformity,
		Suspicious:     suspicious,
		Digits:         digits,
	}
}

func leadingNonZeroDigit(val float64) int {
	val = math.Abs(val)
	if val < 1e-6 {
		return 0
	}
	s := fmt.Sprintf("%.4f", val)
	for _, r := range s {
		if r >= '1' && r <= '9' {
			return int(r - '0')
		}
	}
	return 0
}
