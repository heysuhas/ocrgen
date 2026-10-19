package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"

	"ocrgen/internal/models"
)

var (
	multiSpaceRe = regexp.MustCompile(`\s+`)
	nonAlnumRe   = regexp.MustCompile(`[^a-z0-9\s]+`)
	invPrefixRe  = regexp.MustCompile(`^(?i)(invoice\s*(no|number|num|#)?[\s:.-]*|inv[\s#:.-]+)`)
	corpSuffixRe = regexp.MustCompile(`\b(inc|incorporated|llc|ltd|limited|corp|corporation|co|company|gmbh|plc|llp)\b`)
)

var supportedDateLayouts = []string{
	"2006-01-02",
	"2006/01/02",
	"2006.01.02",
	"01/02/2006",
	"02/01/2006",
	"1/2/2006",
	"2/1/2006",
	"01-02-2006",
	"02-01-2006",
	"02.01.2006",
	"Jan 2, 2006",
	"Jan 02, 2006",
	"January 2, 2006",
	"January 02, 2006",
	"2 Jan 2006",
	"02 Jan 2006",
	"2 January 2006",
	"02 January 2006",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02 15:04:05",
}

// NormalizeDate parses common date formats and converts them to ISO-8601 (YYYY-MM-DD).
func NormalizeDate(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// Strip ordinal suffixes like 1st, 2nd, 3rd, 4th
	ordRe := regexp.MustCompile(`(\d{1,2})(st|nd|rd|th)\b`)
	s = ordRe.ReplaceAllString(s, "$1")
	s = multiSpaceRe.ReplaceAllString(s, " ")

	for _, layout := range supportedDateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return strings.ToLower(s)
}

// NormalizeCurrency standardizes currency symbols and names to 3-letter ISO 4217 codes.
func NormalizeCurrency(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	switch s {
	case "$", "US$", "USD", "US DOLLAR", "DOLLARS":
		return "USD"
	case "€", "EUR", "EURO", "EUROS":
		return "EUR"
	case "£", "GBP", "POUND", "POUNDS":
		return "GBP"
	case "₹", "RS", "RS.", "INR", "RUPEE", "RUPEES":
		return "INR"
	case "CA$", "CAD":
		return "CAD"
	case "A$", "AUD":
		return "AUD"
	case "¥", "JPY":
		return "JPY"
	default:
		if len(s) == 3 {
			return s
		}
		return s
	}
}

// RoundCents rounds a float64 monetary amount to 2 decimal places (exact cents).
func RoundCents(val float64) float64 {
	return math.Round(val*100.0) / 100.0
}

// FormatAmount formats a monetary float to canonical 2-decimal string (e.g., "1250.50").
func FormatAmount(val float64) string {
	return fmt.Sprintf("%.2f", RoundCents(val))
}

// NormalizeVendorName canonicalizes a vendor name for comparison and indexing.
func NormalizeVendorName(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	s = nonAlnumRe.ReplaceAllString(s, " ")
	s = corpSuffixRe.ReplaceAllString(s, " ")
	s = multiSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// NormalizeInvoiceNumber strips prefixes ("INV#", "Invoice No:") and non-alphanumeric separators.
func NormalizeInvoiceNumber(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = invPrefixRe.ReplaceAllString(s, "")
	s = strings.ToUpper(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeTaxID strips spaces, dashes, and punctuation from tax/VAT identifiers.
func NormalizeTaxID(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeBankDetails canonicalizes IBAN/account strings for comparison.
func NormalizeBankDetails(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeFreeText normalizes descriptions and general strings.
func NormalizeFreeText(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = nonAlnumRe.ReplaceAllString(s, " ")
	s = multiSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// ComputeCompositeHash computes a deterministic SHA-256 hash over (normalized vendor, invoice_number, total, invoice_date).
func ComputeCompositeHash(inv models.ExtractedInvoice) string {
	v := NormalizeVendorName(inv.VendorName)
	num := NormalizeInvoiceNumber(inv.InvoiceNumber)
	tot := FormatAmount(inv.Total)
	dt := NormalizeDate(inv.InvoiceDate)
	if v == "" && num == "" && inv.Total == 0 {
		return ""
	}
	payload := fmt.Sprintf("%s|%s|%s|%s", v, num, tot, dt)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:16])
}

// StringSimilarity computes a [0.0, 1.0] normalized Levenshtein + token-overlap similarity score.
func StringSimilarity(a, b string) float64 {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" && b == "" {
		return 1.0
	}
	if a == "" || b == "" {
		return 0.0
	}
	if a == b {
		return 1.0
	}
	levSim := levenshteinRatio(a, b)
	tokSim := tokenJaccard(a, b)
	if tokSim > levSim {
		return 0.6*levSim + 0.4*tokSim
	}
	return levSim
}

func levenshteinRatio(s1, s2 string) float64 {
	r1 := []rune(s1)
	r2 := []rune(s2)
	n := len(r1)
	m := len(r2)
	if n == 0 && m == 0 {
		return 1.0
	}
	maxLen := n
	if m > maxLen {
		maxLen = m
	}
	dp := make([]int, m+1)
	for j := 0; j <= m; j++ {
		dp[j] = j
	}
	for i := 1; i <= n; i++ {
		prev := dp[0]
		dp[0] = i
		for j := 1; j <= m; j++ {
			temp := dp[j]
			cost := 0
			if r1[i-1] != r2[j-1] {
				cost = 1
			}
			del := dp[j] + 1
			ins := dp[j-1] + 1
			sub := prev + cost
			best := del
			if ins < best {
				best = ins
			}
			if sub < best {
				best = sub
			}
			dp[j] = best
			prev = temp
		}
	}
	dist := dp[m]
	return 1.0 - float64(dist)/float64(maxLen)
}

func tokenJaccard(a, b string) float64 {
	ta := strings.Fields(a)
	tb := strings.Fields(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0.0
	}
	setA := make(map[string]bool, len(ta))
	for _, t := range ta {
		setA[t] = true
	}
	setB := make(map[string]bool, len(tb))
	for _, t := range tb {
		setB[t] = true
	}
	inter := 0
	union := len(setA)
	for t := range setB {
		if setA[t] {
			inter++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0.0
	}
	return float64(inter) / float64(union)
}
