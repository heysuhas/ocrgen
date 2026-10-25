# Local Invoice Extraction with Dual-Engine Consensus and Fraud Auditing — Implementation Spec

> **How to use this document (read first)**
> This is a *design brief*, not a rigid script. It states intent, a recommended architecture, and acceptance criteria.
> - Treat model names, versions, tags, library APIs and dataset licenses as **starting hypotheses**. The model landscape changes quickly. **Before pinning any dependency, do a web search** to confirm: current model versions (e.g. a newer PaddleOCR-VL or Qwen-VL release may exist), exact Ollama/Hugging Face identifiers, install commands, VRAM/RAM needs, and dataset licenses/download locations.
> - If you find a better-fitting option, or a recommendation here is wrong, deviate and **document the reason** in `docs/decisions.md`.
> - Prefer the simplest thing that satisfies the acceptance criteria. Build and evaluate incrementally; do not build all phases at once.

---

## 1. Project goal

Build a **fully local, privacy-preserving** invoice processing system (no cloud API calls at runtime) that:

1. Extracts structured fields from invoice PDFs/images.
2. Verifies extractions using **two independent engines** and field-level consensus.
3. Flags suspicious invoices with **statistical fraud checks**.
4. Lets a human reviewer correct results, and **learns from corrections** ("adaptive").
5. Runs on **modest hardware** (assume ~6–8 GB VRAM GPU, or CPU-only with 16 GB RAM; confirm actual hardware with the user before choosing quantization).

Working title: *Adaptive Multimodal Invoice Information Extraction with Dual-Engine Consensus Verification and Statistical Fraud Auditing.*

## 2. Key design correction (important)

The original draft flow was a single serial chain: `upload → PaddleOCR-VL → text LLM → validation → DB`. That is **one** extraction path, so "dual-engine consensus" is not actually implemented. The corrected design has **two independent extraction paths** whose outputs are compared field by field:

- **Engine A (text path):** document parser/OCR → text LLM → JSON.
- **Engine B (vision path):** a general vision-language model reads the page image directly → JSON, **without seeing Engine A's output**.

Independence matters: if B consumes A's output, agreement is meaningless.

## 3. Architecture

```
Upload (PDF/image)
   │  render pages (≈200 DPI), deskew/rotate
   ▼
┌───────────────────────┐        ┌───────────────────────────┐
│ Engine A (text path)  │        │ Engine B (vision path)    │
│ PaddleOCR-VL → md/    │        │ General VLM (Qwen-VL via  │
│ tables/blocks         │        │ Ollama) reads image →     │
│   ↓                   │        │ schema-constrained JSON   │
│ Text LLM (Ollama) →   │        └─────────────┬─────────────┘
│ schema-constrained    │                      │
│ JSON                  │                      │
└──────────┬────────────┘                      │
           └─────────────┬─────────────────────┘
                         ▼
              Normalize + field-level consensus
                         ▼
        Validation (grounding + arithmetic checks)
                         ▼
        SQLite (per-field status + raw outputs + confidence)
              │                         │
              ▼                         ▼
        Fraud audit              Retrieval index (adaptive
 (duplicate, vendor z-score,     few-shot memory + search)
  batch-level Benford, rules)
              └────────────┬────────────┘
                           ▼
                  Web dashboard (review / audit / search)
                           │
        Reviewer corrections ──► SQLite + adaptive memory
```

### 3.1 Runtime topology (answer to "can we use both HF and Ollama?")

Yes — they are independent services and can coexist:

- **PaddleOCR-VL** is primarily distributed via Hugging Face / the `paddleocr` package (also vLLM and llama.cpp backends exist; a community Ollama build may exist but verify before depending on it). Run it as an in-process module or a small local worker/service.
- **Qwen models** (text LLM and VLM) are served by **Ollama** over local HTTP.
- The app orchestrates both. Keep each behind an interface (`Parser`, `Extractor`) so backends are swappable.
- Mind memory: if VRAM is tight, load models sequentially (parse with PaddleOCR-VL, release, then call Ollama) or run one of them on CPU. Measure before deciding.

## 4. Component specs

### 4.1 Ingestion
- PDF → page images (e.g. pypdfium2 or pdf2image), ~200 DPI; image inputs pass through.
- Light preprocessing (orientation/deskew). Keep original files and page images; store a content hash.
- Multi-page invoices: process per page, then merge (header fields from page 1, line items concatenated, totals reconciled).

### 4.2 Engine A
- **Parser:** PaddleOCR-VL (0.9B, document parsing to markdown/tables/blocks). Verify latest version (a 1.5 release existed in Jan 2026 and a newer one may exist) and the supported inference path for the user's hardware.
- **Text LLM:** start with a small instruct model via Ollama (Qwen2.5 3B is the *floor*; benchmark Qwen3 4B and a 7B-class Q4 if memory allows). Small models degrade on extraction, so evaluate rather than assume.
- Use **structured output / JSON-schema constrained decoding** (Ollama supports this; confirm the current API). Temperature 0.
- Persist the parser's raw markdown and block coordinates for grounding and later search.

### 4.3 Engine B
- A VLM that takes the **page image** and the same JSON schema. Candidates: a Qwen-VL family model in the ~2–4B range, Q4-quantized, via Ollama (e.g. Qwen3-VL 4B, fallback 2B or Qwen2.5-VL 3B). **Verify exact Ollama tags and current availability by web search.** Avoid quantization below Q4 for OCR-heavy work (small characters degrade).
- Fine-tuned "invoice extractor" checkpoints on Hugging Face can be used as extra baselines, but treat them as unverified unless they publish evaluation results.

### 4.4 Extraction schema (v1, adjust as needed)
`vendor_name, vendor_tax_id?, invoice_number, invoice_date, due_date?, currency, subtotal, tax_total, total, line_items[{description, quantity, unit_price, amount}], bank_details?`
- Every extracted field carries: `value`, `source_engine`, `evidence` (text span/bbox if available), `confidence`, `status`.

### 4.5 Normalization
- Dates → ISO 8601; amounts → `Decimal` (handle locale separators, currency symbols); vendor names → case/punctuation-normalized; invoice numbers → strip whitespace/punctuation variants.
- Normalize **before** comparing engines.

### 4.6 Consensus
Per field:
- Exact match after normalization (or fuzzy ratio ≥ a tuned threshold for free-text fields like vendor name) → `verified`.
- Mismatch → `conflict`; store both candidates and send to review.
- One engine empty → `single_source` (lower confidence, review depending on field criticality).
- Line items: match rows by (amount, then description similarity), then compare per row.
Consensus status is **per field**, not per invoice; an invoice is auto-accepted only if all critical fields (`invoice_number, date, vendor, total`) are `verified` and validation passes.

### 4.3b Validation
- **Grounding:** the value (normalized) should appear in the OCR text/blocks. Note this is only weakly independent of Engine A, so it is a sanity check, not a second vote.
- **Arithmetic:** `qty × unit_price ≈ line amount`, `Σ lines ≈ subtotal`, `subtotal + tax ≈ total` within a small tolerance.
- Failures lower confidence and force review.

### 4.7 Storage (SQLite)
Tables (suggested): `invoices`, `pages`, `fields` (per-field value/status/engine candidates), `line_items`, `engine_runs` (raw outputs, model+version, latency), `reviews` (corrections, reviewer, timestamp), `fraud_flags`, `vendors` (aggregates for fraud stats). Keep an audit trail; never overwrite raw engine output.

### 4.8 Fraud audit
Be statistically honest about what each test can do:
- **Duplicate detection:** hash of (normalized vendor, invoice number, total, date) plus fuzzy match and an image perceptual hash. Works on single invoices.
- **Vendor-level outlier (z-score / robust z with median-MAD):** requires per-vendor history; define a cold-start rule (e.g. skip until n ≥ ~20; make it configurable).
- **Benford's law:** a *population-level* test (needs hundreds of amounts per vendor/batch). Run on batches/vendors, never as a per-invoice score. Report chi-square or MAD conformity with sample size.
- **Cheap rules:** round-number totals, weekend/holiday dates, invoice-number sequence gaps, changed bank details for a known vendor, total mismatch.
- Output flags with a reason and a severity, not a single opaque "fraud score".

### 4.9 Adaptive memory and search
- Define "adaptive" concretely: when a reviewer corrects an invoice, store the corrected extraction with its parsed text and layout signature. For a new invoice from the same vendor or similar layout, **retrieve the nearest corrected examples and inject them as few-shot examples** into the extractor prompt.
- Index options: ChromaDB (as in the original design), or SQLite FTS5 + `sqlite-vec` to cut moving parts. Choose based on complexity vs benefit; document the choice. Plain keyword search can be FTS5 alone.
- Layout signature: vendor + page-1 header region text/embedding, optionally block-geometry features.

### 4.10 Dashboard
- Backend: FastAPI (or similar). Frontend: simple server-rendered/HTMX or a small React app — pick the lightest that works.
- Views: upload, review queue (conflicts first), side-by-side page image and extracted fields with highlighted evidence, edit/approve, audit log, fraud flags, search.
- Reviewer actions write to SQLite and to the adaptive memory.

## 5. Suggested repo layout (adapt freely)

```
/app
  /ingest        # pdf/image → pages
  /engines
    parser_paddle.py     # Engine A parser
    extractor_text.py    # Engine A LLM (Ollama)
    extractor_vlm.py     # Engine B (Ollama)
  /pipeline      # orchestration, normalization, consensus, validation
  /fraud
  /memory        # corrections, retrieval, few-shot builder
  /api           # FastAPI routes
  /ui
/eval            # datasets loaders, metrics, ablation scripts, reports
/docs            # decisions.md, hardware_profile.md
/tests
```

## 6. Implementation phases and acceptance criteria

Build in order; do not start a phase until the previous one is measured.

1. **Baseline + eval harness.** Ingest → Engine A → JSON. Build the evaluation harness *first* (see §7).
   *Accept:* field-level metrics reproducible on a public dataset with a single command.
2. **Engine B + consensus.** Add the VLM path and field-level agreement.
   *Accept:* report A-only, B-only, and consensus accuracy; report agreement rate.
3. **Validation + SQLite.** Grounding, arithmetic, per-field status, audit tables.
   *Accept:* every field has a status and evidence; raw outputs persisted.
4. **Fraud module.** Duplicates first, then vendor stats, then batch Benford, then rules.
   *Accept:* precision/recall reported per anomaly type on injected anomalies (§7).
5. **Dashboard + adaptive loop.** Review queue, corrections, few-shot retrieval.
   *Accept:* measurable accuracy improvement on a held-out vendor/layout after N corrections.
6. **Ablations + hardware profiling.** Quantization levels, model sizes, latency/memory per page.
   *Accept:* a table of accuracy vs latency vs memory on the user's stated hardware.

## 7. Evaluation (use defensible metrics)

- Per-field normalized exact match and F1 for: Engine A only, Engine B only, consensus.
- **Coverage vs accuracy:** the share of fields auto-verified by consensus versus the accuracy of that subset. This is the central result supporting the consensus claim.
- Human review rate (fraction of invoices needing review).
- Fraud: precision/recall per injected-anomaly type (altered totals, duplicates, amount-distribution shifts, changed bank details). Do **not** claim real-world fraud detection accuracy without labeled real fraud.
- Latency and peak memory per page, with hardware stated.
- Report sample sizes and confidence intervals where practical.

### Candidate datasets (verify licenses, availability and links via web search before use)
- **SROIE** — scanned receipts (company, date, address, total).
- **CORD** — receipts with line-item annotation (useful for arithmetic validation).
- **FATURA** — synthetic multi-layout invoices with field annotations (good for layout adaptation).
- **DocILE** — larger invoice-style key-information/line-item benchmark (heavier; check license).
- **Fraud/tamper:** no good public invoice-fraud set is known; generate labeled anomalies synthetically from clean data. A receipt-forgery dataset ("Find it again!") may help for tamper experiments — verify.

## 8. Hardware guidance (confirm real specs first)

| Tier | Assumption | Approach |
|---|---|---|
| Low | CPU only, 16 GB RAM | PaddleOCR-VL via a CPU-capable backend (e.g. llama.cpp build, verify); 2–3B Q4 models; expect slow throughput; process sequentially |
| Mid | 6–8 GB VRAM | Sequential model loading; Qwen3-VL 4B Q4 (or 2B); text LLM 3–4B Q4 |
| Higher | 12 GB+ VRAM | Both engines resident; try 7–8B text LLM Q4 |

Record the measured profile in `docs/hardware_profile.md`.

## 9. Risks and open questions

- Model/tag availability and licenses may differ from this document — verify.
- Engine disagreement may be frequent on poor scans; track it, don't hide it.
- Benford/z-score need volume; handle small data gracefully and communicate this in the UI.
- Multilingual or handwritten invoices may need a different parser/VLM — clarify scope with the user.
- Confirm with the user: target hardware, invoice languages/currencies, expected volume, whether line items are in scope for v1, and whether the dashboard needs auth/multi-user.

## 10. Non-goals (v1)
Cloud APIs at runtime, payment execution, ERP integrations, and training/fine-tuning custom models (revisit after baselines are measured).
