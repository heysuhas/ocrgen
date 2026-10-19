package memory

import (
	"math"
	"sort"
	"strings"

	"ocrgen/internal/models"
	"ocrgen/internal/pipeline"
)

// Retriever selects the most relevant human-corrected invoices from adaptive memory
// to inject as few-shot examples into Engine A and Engine B prompts.
type Retriever struct {
	MaxShots      int
	MinSimilarity float64
}

func NewRetriever(maxShots int, minSim float64) *Retriever {
	if maxShots <= 0 {
		maxShots = 2
	}
	if minSim <= 0 {
		minSim = 0.28
	}
	return &Retriever{
		MaxShots:      maxShots,
		MinSimilarity: minSim,
	}
}

// RetrieveTopK ranks stored AdaptiveExamples against the incoming document's OCR markdown, header blocks, and layout signature.
func (r *Retriever) RetrieveTopK(
	parseRes *models.DocumentParseResult,
	candidates []models.AdaptiveExample,
) []models.AdaptiveExample {
	if len(candidates) == 0 || parseRes == nil {
		return nil
	}

	docLower := strings.ToLower(parseRes.Markdown)
	var headerText strings.Builder
	for _, blk := range parseRes.Blocks {
		if blk.BlockType == "header" || blk.BBox.Y1 < 450 {
			headerText.WriteString(blk.Text)
			headerText.WriteString(" ")
		}
	}
	hdrNorm := pipeline.NormalizeFreeText(headerText.String())
	if hdrNorm == "" {
		hdrNorm = pipeline.NormalizeFreeText(truncate(parseRes.Markdown, 400))
	}

	var scored []models.AdaptiveExample
	for _, cand := range candidates {
		vScore := 0.0
		if cand.VendorNorm != "" && strings.Contains(docLower, cand.VendorNorm) {
			vScore = 1.0
		} else if cand.VendorNorm != "" {
			// Check partial vendor token match in header
			vScore = pipeline.StringSimilarity(cand.VendorNorm, truncate(hdrNorm, 80))
		}

		hScore := pipeline.StringSimilarity(hdrNorm, pipeline.NormalizeFreeText(cand.HeaderSnippet))
		layoutScore := cosineSimilarity(parseRes.LayoutSignature, cand.LayoutSignature)

		totalSim := 0.50*vScore + 0.30*hScore + 0.20*layoutScore
		if totalSim >= r.MinSimilarity {
			candCopy := cand
			candCopy.SimilarityScore = math.Round(totalSim*1000) / 1000
			scored = append(scored, candCopy)
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].SimilarityScore > scored[j].SimilarityScore
	})

	if len(scored) > r.MaxShots {
		scored = scored[:r.MaxShots]
	}
	return scored
}

func cosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0.0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA <= 1e-9 || normB <= 1e-9 {
		return 0.0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
