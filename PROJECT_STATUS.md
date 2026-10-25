# OCRGen — Master Implementation & Agent Handoff Document

> **Purpose of this file:** This single Markdown file tracks the complete architecture, implementation status (`[x]` Done vs `[ ]` To-Do), file-by-file responsibilities, runtime commands, and continuation instructions so any AI agent or engineer can pick up this repository at any point and understand the full state immediately.

---

## 1. System Context & Hardware Target

- **Spec Reference:** `INVOICE_PROJECT_SPEC.md`
- **Host Hardware:**
  - **GPU:** NVIDIA GeForce RTX 4050 Laptop GPU (`6,141 MiB` VRAM, CUDA 13.4)
  - **CPU:** AMD Ryzen 7 7445HS w/ Radeon 740M Graphics (12 threads)
  - **RAM:** 14 GiB System RAM (~7.6 GiB available)
- **Models Configured:**
  - **Engine A Document Parser:** `PaddlePaddle/PaddleOCR-VL-1.6` (0.9B VLM document parser via Python worker in `worker_paddle/`, with automatic VRAM offload after parsing + fallback OCR/spotting support).
  - **Engine A Text LLM:** `qwen3-vl:4b` via Ollama (`http://127.0.0.1:11434/api/chat`, **text-only prompt**, `think: false`, `num_ctx: 16384`, JSON schema constrained).
  - **Engine B Vision VLM:** `qwen3-vl:4b` via Ollama (`http://127.0.0.1:11434/api/chat`, **image-only prompt**, `think: false`, `num_ctx: 16384`, JSON schema constrained).
  - *Note on Independence:* Engine B receives **only** the rendered page PNG and never sees Engine A's OCR output. Reusing `qwen3-vl:4b` across Engine A's text step and Engine B's vision step eliminates a 3.3 GB VRAM swap in Ollama while maintaining strict path independence.

---

## 2. Polyglot Architecture Overview

1. **Go Core Backend (`cmd/server`, `cmd/eval`, `internal/*`)**
   - Compiles to a lightweight native binary (~20 MB RAM footprint) so maximum system RAM and VRAM remain available for the ML models.
   - **Ingestion (`internal/ingest`):** Converts PDFs (`pdftoppm -png -r 200`) and images into normalized 200 DPI page PNGs, auto-orients/deskews via ImageMagick (`magick`), computes SHA-256 file hashes and 64-bit perceptual image hashes (`dHash`/`pHash`) in pure Go.
   - **Engines (`internal/engines`):**
     - `PaddleClient`: HTTP client to `worker_paddle` (`http://127.0.0.1:8091/parse`) with automatic `/unload` trigger to free GPU VRAM before Ollama runs.
     - `OllamaExtractor`: Structured JSON-schema client for Ollama (`qwen3-vl:4b`) supporting both text-only extraction (Engine A) and vision-only extraction (Engine B), with explicit `num_ctx: 16384`, `temperature: 0`, and `think: false`.
   - **Pipeline (`internal/pipeline`):**
     - Multi-page merger, locale-aware normalizer (ISO-8601 dates, exact currency/decimal normalization, vendor & invoice ID canonicalization).
     - Field-level and row-matched line-item consensus engine (`verified`, `conflict`, `single_source`, `unverified`).
     - Grounding verifier (checks normalized field values against OCR text & bounding-box spans) and arithmetic validator (`qty * unit_price ≈ amount`, `sum(lines) ≈ subtotal`, `subtotal + tax ≈ total`).
   - **Storage (`internal/store`):** Pure-Go SQLite (`modernc.org/sqlite`, zero CGO issues, WAL mode, FTS5 enabled) storing `invoices`, `pages`, `fields`, `line_items`, `engine_runs`, `reviews`, `fraud_flags`, `vendors`, and `adaptive_examples`.
   - **Fraud Auditing (`internal/fraud`):**
     - Exact SHA-256, composite `(vendor, invoice_number, total, date)` hash, fuzzy duplicate check, and perceptual image hash (`pHash` Hamming distance) check.
     - Vendor-level Robust Z-score (`|x - median| / (1.4826 * MAD)`) with configurable cold-start (`min_history = 20`, falls back to informational cold-start status below 20).
     - Population/Batch-level Benford's Law first-digit test ($\chi^2$ and Mean Absolute Deviation conformity).
     - Deterministic rules: round-number totals, weekend invoice dates, invoice sequence gaps, vendor bank-detail changes, and arithmetic mismatch flags.
   - **Adaptive Memory (`internal/memory`):**
     - Stores human-reviewed/corrected invoices with vendor name, layout signature (header block geometry + token fingerprint), and OCR snippet.
     - Retrieves top-$k$ nearest prior corrections for the same vendor or similar layout and injects them as few-shot examples into Engine A and Engine B prompts.
   - **Web Dashboard (`internal/api`):**
     - Embedded HTML/HTMX/Canvas single-binary web UI (`http://127.0.0.1:8080`) featuring PDF/image upload, prioritized review queue, side-by-side page viewer with interactive bounding-box overlays, Engine A vs Engine B candidate comparison, one-click field resolution/correction, fraud audit explorer, Benford batch analyzer, and full-text search.

2. **Python `PaddleOCR-VL-1.6` Worker (`worker_paddle/`)**
   - Managed via `uv` (`worker_paddle/pyproject.toml`).
   - Loads `PaddlePaddle/PaddleOCR-VL-1.6` via Hugging Face `transformers>=5.0.0` (`AutoModelForImageTextToText` + `AutoProcessor` in `bfloat16`) or `paddleocr` pipeline.
   - Exposes `POST /parse` (returns Markdown, tables, and normalized bounding boxes `[x1, y1, x2, y2]` for grounding/UI highlighting) and `POST /unload` (offloads model to CPU / clears CUDA cache so Ollama gets all 6 GB VRAM).

---

## 3. Implementation Checklist (Phases 1–6)

### Phase 1: Baseline + Evaluation Harness
- [x] Project directory layout & `docs/decisions.md`
- [x] Core data structures & JSON Schema definitions (`internal/models/models.go`)
- [x] PDF/Image ingestion at 200 DPI, SHA-256 & perceptual hash (`internal/ingest/ingest.go`)
- [x] PaddleOCR-VL worker (`worker_paddle/server.py`, `worker_paddle/pyproject.toml`)
- [x] Engine A (`PaddleClient` + `OllamaExtractor` text mode) (`internal/engines/`)
- [x] Synthetic multi-layout benchmark generator and loader (`eval/datasets/`)
- [x] CLI evaluation harness computing normalized exact match, token F1, latency, and VRAM (`cmd/eval/main.go`)

### Phase 2: Engine B + Dual-Engine Consensus
- [x] Engine B (`OllamaExtractor` vision mode on raw page images, isolated from Engine A's document input)
- [x] Field & currency/date/vendor normalizer (`internal/pipeline/normalize.go`)
- [x] Field-level & line-item matching consensus engine (`internal/pipeline/consensus.go`)
- [x] Evaluation metrics for A-only, B-only, consensus accuracy/F1, agreement rate, and verified coverage/accuracy

### Phase 3: Validation + SQLite Persistence
- [x] OCR text & bounding-box grounding verifier + arithmetic validator (`internal/pipeline/validate.go`)
- [x] SQLite schema & repository for invoices, pages, fields, line items, engine runs, reviews, fraud flags, vendors, and adaptive examples (`internal/store/`)
- [x] End-to-end pipeline coordinator (`internal/orchestrator/orchestrator.go`)

### Phase 4: Statistical Fraud Auditing Module
- [x] Duplicate detector (SHA-256, composite field hash, fuzzy match, perceptual image dHash Hamming distance) (`internal/fraud/fraud.go`)
- [x] Vendor Robust Z-Score (Median + MAD) with cold-start guard
- [x] Batch/Population-level Benford's Law analyzer (chi-square + MAD + sample-size guard)
- [x] Deterministic rules (round-number totals, weekend dates, invoice sequence gaps, vendor bank-detail changes, arithmetic discrepancy flags)
- [x] Labeled synthetic anomaly evaluator reporting precision/recall per fraud anomaly type

### Phase 5: Web Dashboard + Adaptive Few-Shot Memory Loop
- [x] Adaptive few-shot memory store & similarity retriever (`internal/memory/memory.go`)
- [x] HTTP API + embedded dashboard UI (`internal/api/server.go`, `internal/api/ui.go`, `cmd/server/main.go`)
- [x] Adaptive evaluation measuring extraction accuracy before/after reviewer corrections on held-out vendors (`cmd/eval/main.go`)

### Phase 6: Ablations, Hardware Profiling & Verification
- [x] Run end-to-end unit & integration tests (`go test ./...`)
- [x] Run live Ollama + PaddleOCR-VL evaluation and fraud/adaptive benchmarks (`eval/reports/latest_report.md`)
- [x] Record real hardware latency, RAM, and VRAM measurements in `docs/hardware_profile.md` (verified live on RTX 4050 6 GB VRAM)

---

## 4. How Another AI Agent or Developer Can Continue / Operate This Project

### Quick Start Commands
```bash
# 1. Start the Python PaddleOCR-VL-1.6 Worker (port 8091)
cd worker_paddle
uv run python server.py --port 8091

# 2. Build and run the Go Web Dashboard & API Server (port 8080)
go build -o bin/ocrgen-server ./cmd/server
./bin/ocrgen-server --db ./data/ocrgen.db --port 8080 --ollama-url http://127.0.0.1:11434 --paddle-url http://127.0.0.1:8091

# 3. Run the Evaluation & Ablation Harness (Phases 1-6 metrics)
go build -o bin/ocrgen-eval ./cmd/eval
./bin/ocrgen-eval --dataset ./eval/datasets/sample_bench --report ./eval/reports/latest_report.md
```

### Key Extension Points
- **Swapping Models:** Pass `--text-model` or `--vlm-model` flags to `ocrgen-server` or `ocrgen-eval` (defaults to `qwen3-vl:4b` for both, with `num_ctx=16384`).
- **Adjusting Fraud Thresholds:** See `internal/fraud/fraud.go` (`Config` struct: `MinVendorHistory: 20`, `RobustZThreshold: 3.5`, `PHashMaxDistance: 8`).
- **Adding New Extraction Fields:** Update `ExtractedInvoice` and `ExtractionJSONSchema` in `internal/models/models.go`, then add normalization/consensus rules in `internal/pipeline/`.
