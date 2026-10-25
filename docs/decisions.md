# Architectural & Technical Decisions (`docs/decisions.md`)

This document records key engineering decisions and deviations/refinements relative to `INVOICE_PROJECT_SPEC.md`.

## ADR-001: Polyglot Go + Python Worker Architecture for 6 GB VRAM Budget
- **Context:** The target machine has an NVIDIA GeForce RTX 4050 Laptop GPU with **6,141 MiB VRAM** and **14 GiB System RAM** (~7.6 GiB free). Running a heavy Python web backend alongside PyTorch (`PaddleOCR-VL-1.6`) and Ollama (`qwen3-vl:4b`) risks both system RAM exhaustion and GPU VRAM thrashing.
- **Decision:**
  1. Implement the core backend, ingestion orchestrator, normalization, consensus engine, arithmetic/grounding validators, statistical fraud engine, SQLite persistence, and web dashboard in **Go (`1.26.8`)**. The compiled Go binary uses **< 25 MB RAM**.
  2. Isolate `PaddlePaddle/PaddleOCR-VL-1.6` into a dedicated, lightweight Python worker (`worker_paddle/server.py`) managed by `uv`.
  3. Add an explicit `/unload` lifecycle hook on the Python worker (`model.to("cpu")` + `torch.cuda.empty_cache()`) invoked by the Go coordinator immediately after page parsing completes, freeing VRAM before Ollama executes LLM/VLM extraction.

## ADR-002: Using `PaddlePaddle/PaddleOCR-VL-1.6` (May 2026 Release)
- **Context:** `INVOICE_PROJECT_SPEC.md` noted `PaddleOCR-VL 1.5` existed in Jan 2026 and asked to check for newer releases.
- **Decision:** Use **`PaddlePaddle/PaddleOCR-VL-1.6`** (released May 28, 2026, 96.33% on OmniDocBench v1.6, 0.9B parameters). The worker supports loading `PaddlePaddle/PaddleOCR-VL-1.6` via Hugging Face `transformers>=5.0.0` (`AutoModelForImageTextToText` in `bfloat16`) as well as the `paddleocr` `PaddleOCRVL(pipeline_version="v1.6")` pipeline, plus a fast local OCR fallback when weights are still downloading or in CPU-only test environments.

## ADR-003: Dual-Role `qwen3-vl:4b` in Ollama with Strict Input Isolation & `think: false`
- **Context:** `qwen3-vl:4b` (3.3 GB Q4_K_M) is pre-installed in Ollama. Loading a separate text LLM (e.g., `qwen2.5:3b` or `qwen3:4b`) after `qwen3-vl:4b` on a 6 GB GPU would force Ollama to unload and reload 3+ GB of weights on every invoice.
- **Decision:**
  - Use `qwen3-vl:4b` for **both** Engine A's text extraction step and Engine B's vision extraction step, while keeping the model IDs configurable via CLI flags for ablations.
  - **Strict Path Independence:**
    - Engine A receives **only** the markdown/table text parsed by `PaddleOCR-VL-1.6` (no image).
    - Engine B receives **only** the raw 200 DPI rendered page image (no OCR text from Engine A).
  - **Context Window & Thinking Mode:** Explicitly set `"options": {"num_ctx": 16384, "temperature": 0}` and `"think": false` in Ollama API requests. Disabling chain-of-thought thinking mode (`think: false`) prevents Qwen3 from consuming thousands of reasoning tokens before emitting schema-constrained JSON, cutting latency by 3–5× and keeping KV cache memory well inside the 6 GB VRAM ceiling.

## ADR-004: SQLite + FTS5 + Structural Signature for Adaptive Memory
- **Context:** `INVOICE_PROJECT_SPEC.md` §4.9 offers a choice between ChromaDB or SQLite FTS5 / lightweight vector similarity to reduce moving parts.
- **Decision:** Use embedded **SQLite (`modernc.org/sqlite`) with FTS5 and a hybrid Vendor + Layout Geometry Signature** (cosine similarity over normalized header block geometry + token n-gram TF-IDF vector stored in SQLite). This eliminates running an external vector database daemon, uses zero extra RAM, and provides deterministic sub-millisecond retrieval for adaptive few-shot prompt injection.
