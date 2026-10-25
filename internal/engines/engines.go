package engines

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"ocrgen/internal/models"
)

// Parser is the interface for Engine A's document parser (PaddleOCR-VL-1.6).
type Parser interface {
	ParseDocument(ctx context.Context, pagePaths []string, originalPDF string) (*models.DocumentParseResult, error)
	UnloadVRAM(ctx context.Context) error
}

// ExtractionResponse wraps the structured extraction output along with raw audit telemetry.
type ExtractionResponse struct {
	Extracted    models.ExtractedInvoice
	RawOutput    string
	ModelID      string
	LatencyMs    int64
	PromptTokens int
	OutputTokens int
}

// Extractor is the swappable interface for Engine A (text LLM) and Engine B (vision VLM).
type Extractor interface {
	Extract(ctx context.Context, input ExtractionInput) (*ExtractionResponse, error)
}

// ExtractionInput holds the inputs for either Engine A (Markdown text only) or Engine B (Page images only).
type ExtractionInput struct {
	Markdown        string                   // Populated ONLY for Engine A
	PageImagePaths  []string                 // Populated ONLY for Engine B
	FewShotExamples []models.AdaptiveExample // Retrieved adaptive memory examples
}

// PaddleHTTPParser connects to `worker_paddle/server.py` (and can auto-start it if not running).
type PaddleHTTPParser struct {
	baseURL    string
	httpClient *http.Client
	workerCmd  *exec.Cmd
}

func NewPaddleHTTPParser(baseURL string, autoStart bool, workerScriptPath string, ollamaURL string, ollamaModel string) *PaddleHTTPParser {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8091"
	}
	p := &PaddleHTTPParser{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 180 * time.Second,
		},
	}
	if autoStart && !p.IsHealthy() && workerScriptPath != "" {
		_ = p.startWorkerProcess(workerScriptPath, ollamaURL, ollamaModel)
	}
	return p
}

func (p *PaddleHTTPParser) IsHealthy() bool {
	resp, err := p.httpClient.Get(p.baseURL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (p *PaddleHTTPParser) startWorkerProcess(scriptPath, ollamaURL, ollamaModel string) error {
	if _, err := os.Stat(scriptPath); err != nil {
		return err
	}
	cmd := exec.Command("python3", scriptPath, "--port", "8091", "--ollama-url", ollamaURL, "--ollama-model", ollamaModel)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	p.workerCmd = cmd
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if p.IsHealthy() {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return nil
}

func (p *PaddleHTTPParser) Close() {
	if p.workerCmd != nil && p.workerCmd.Process != nil {
		_ = p.workerCmd.Process.Kill()
	}
}

func (p *PaddleHTTPParser) ParseDocument(ctx context.Context, pagePaths []string, originalPDF string) (*models.DocumentParseResult, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"page_paths":   pagePaths,
		"original_pdf": originalPDF,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/parse", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("paddle worker request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("paddle worker status %d: %s", resp.StatusCode, string(raw))
	}
	var parsed models.DocumentParseResult
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal paddle parse result: %w", err)
	}
	return &parsed, nil
}

func (p *PaddleHTTPParser) UnloadVRAM(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/unload", bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	return nil
}

// OllamaConfig configures an Ollama-backed Extractor (either Text-only for Engine A or Vision-only for Engine B).
type OllamaConfig struct {
	BaseURL    string
	Model      string
	NumCtx     int
	VisionMode bool // false = Engine A (Text LLM on Markdown), true = Engine B (VLM on raw page images)
	Timeout    time.Duration
}

type OllamaExtractor struct {
	cfg        OllamaConfig
	httpClient *http.Client
}

func NewOllamaExtractor(cfg OllamaConfig) *OllamaExtractor {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://127.0.0.1:11434"
	}
	if cfg.Model == "" {
		cfg.Model = "qwen3-vl:4b"
	}
	if cfg.NumCtx <= 0 {
		cfg.NumCtx = 8192
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120 * time.Second
	}
	return &OllamaExtractor{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

type ollamaGenerateRequest struct {
	Model   string          `json:"model"`
	Prompt  string          `json:"prompt"`
	Images  []string        `json:"images,omitempty"`
	Raw     bool            `json:"raw"`
	Format  json.RawMessage `json:"format"`
	Stream  bool            `json:"stream"`
	Options map[string]any  `json:"options"`
}

type ollamaGenerateResponse struct {
	Model           string `json:"model"`
	Response        string `json:"response"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	Error           string `json:"error,omitempty"`
}

func (e *OllamaExtractor) Extract(ctx context.Context, input ExtractionInput) (*ExtractionResponse, error) {
	t0 := time.Now()
	fewShotBlock := formatFewShotBlock(input.FewShotExamples)

	var rawPrompt strings.Builder
	var b64Images []string

	if !e.cfg.VisionMode {
		// Engine A: Strictly text-only path consuming PaddleOCR-VL-1.6 Markdown/Tables
		if strings.TrimSpace(input.Markdown) == "" {
			return nil, fmt.Errorf("engine A text extractor received empty markdown")
		}
		sysPrompt := "You are an expert financial invoice extraction engine (Engine A - Text Path). " +
			"Extract structured invoice fields strictly from the provided OCR Markdown and tables into the required JSON schema. " +
			"Use ISO-8601 format (YYYY-MM-DD) for dates when possible, 3-letter ISO currency codes (e.g. USD, EUR, GBP, INR), " +
			"and exact decimal numbers for subtotal, tax_total, total, and line_items. " +
			"If a field is absent, output an empty string \"\" or 0."
		rawPrompt.WriteString("<|im_start|>system\n" + sysPrompt + "<|im_end|>\n<|im_start|>user\n")
		if fewShotBlock != "" {
			rawPrompt.WriteString(fewShotBlock + "\n\n")
		}
		rawPrompt.WriteString("### PARSED INVOICE DOCUMENT (MARKDOWN & TABLES):\n" + input.Markdown + "\n\nExtract the invoice JSON now.<|im_end|>\n")
		rawPrompt.WriteString("<|im_start|>assistant\n<think>\n</think>\n")
	} else {
		// Engine B: Strictly vision-only path consuming ONLY raw page images (never sees Engine A's OCR output)
		if len(input.PageImagePaths) == 0 {
			return nil, fmt.Errorf("engine B vision extractor received no page images")
		}
		var imgMarkers strings.Builder
		for idx, imgPath := range input.PageImagePaths {
			imgBytes, err := os.ReadFile(imgPath)
			if err != nil {
				return nil, fmt.Errorf("read page image %s: %w", imgPath, err)
			}
			b64Images = append(b64Images, base64.StdEncoding.EncodeToString(imgBytes))
			imgMarkers.WriteString(fmt.Sprintf("[img-%d]\n", idx))
		}
		sysPrompt := "You are an expert vision-language financial invoice extraction engine (Engine B - Independent Vision Path). " +
			"Read the attached invoice page image(s) directly and extract all fields and line items into the required JSON schema. " +
			"Use ISO-8601 format (YYYY-MM-DD) for dates when possible, 3-letter ISO currency codes (e.g. USD, EUR, GBP, INR), " +
			"and exact decimal numbers for subtotal, tax_total, total, and line_items. " +
			"For multi-page invoices, take header/vendor info from page 1, concatenate all line items across pages, and take final totals from the summary section."
		rawPrompt.WriteString("<|im_start|>system\n" + sysPrompt + "<|im_end|>\n<|im_start|>user\n")
		rawPrompt.WriteString(imgMarkers.String())
		if fewShotBlock != "" {
			rawPrompt.WriteString(fewShotBlock + "\n\n")
		}
		rawPrompt.WriteString("Inspect the attached invoice image(s) carefully and return the structured JSON extraction.<|im_end|>\n")
		rawPrompt.WriteString("<|im_start|>assistant\n<think>\n</think>\n")
	}

	reqPayload := ollamaGenerateRequest{
		Model:  e.cfg.Model,
		Prompt: rawPrompt.String(),
		Images: b64Images,
		Raw:    true,
		Format: models.ExtractionJSONSchema(),
		Stream: false,
		Options: map[string]any{
			"temperature": 0,
			"num_ctx":     e.cfg.NumCtx,
			"num_predict": 1024,
			"stop":        []string{"<|im_end|>", "<|endoftext|>"},
		},
	}

	rawReq, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}

	var lastErr error
	var respBytes []byte

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 1500 * time.Millisecond)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.cfg.BaseURL, "/")+"/api/generate", bytes.NewReader(rawReq))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := e.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("ollama generate request failed (attempt %d): %w", attempt+1, err)
			continue
		}

		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, string(b))
			continue
		}

		respBytes = b
		lastErr = nil
		break
	}

	if lastErr != nil {
		return nil, lastErr
	}

	var genResp ollamaGenerateResponse
	if err := json.Unmarshal(respBytes, &genResp); err != nil {
		return nil, fmt.Errorf("unmarshal ollama response: %w", err)
	}
	if genResp.Error != "" {
		return nil, fmt.Errorf("ollama error: %s", genResp.Error)
	}

	content := strings.TrimSpace(genResp.Response)
	content = cleanJSONFence(content)

	var extracted models.ExtractedInvoice
	if err := json.Unmarshal([]byte(content), &extracted); err != nil {
		return nil, fmt.Errorf("failed to parse structured JSON from model (%v): %s", err, content)
	}

	return &ExtractionResponse{
		Extracted:    extracted,
		RawOutput:    content,
		ModelID:      e.cfg.Model,
		LatencyMs:    time.Since(t0).Milliseconds(),
		PromptTokens: genResp.PromptEvalCount,
		OutputTokens: genResp.EvalCount,
	}, nil
}

func formatFewShotBlock(examples []models.AdaptiveExample) string {
	if len(examples) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("### VERIFIED PRIOR EXAMPLES FROM SIMILAR VENDOR/LAYOUT (LEARN FROM THESE CORRECTIONS):\n")
	for i, ex := range examples {
		corrBytes, _ := json.Marshal(ex.CorrectedJSON)
		sb.WriteString(fmt.Sprintf("Example %d (Vendor: %s):\n", i+1, ex.CorrectedJSON.VendorName))
		if ex.HeaderSnippet != "" {
			sb.WriteString(fmt.Sprintf("Header/OCR Snippet: %s\n", ex.HeaderSnippet))
		}
		sb.WriteString(fmt.Sprintf("Verified Correct JSON: %s\n\n", string(corrBytes)))
	}
	return sb.String()
}

func cleanJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSuffix(s, "```")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}
