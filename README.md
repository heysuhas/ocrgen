# OCRGen

OCRGen is a local invoice extraction and review app. It processes PDF and image invoices through two extraction paths, compares their results field by field, checks OCR grounding and arithmetic, and provides a browser dashboard for review. It also includes explainable duplicate and statistical anomaly checks.

The application is designed to run locally. Invoice files and extracted data are stored on the machine running the server.

## Features

- PDF and image ingestion, page rendering, and file hashing
- Engine A: document parsing followed by text extraction
- Engine B: vision extraction directly from rendered page images
- Field and line item consensus, normalization, and validation
- SQLite persistence and a browser-based review dashboard
- Duplicate, vendor-history, batch Benford, and rule-based audit flags
- Reviewer corrections stored as examples for later extraction prompts
- Synthetic multi-layout invoice benchmark and evaluation CLI

## Requirements

- Go version specified in `go.mod`
- A C compiler and CGO support for the SQLite driver
- Python 3.11 or later and `uv` for the parser worker
- Ollama running locally with a vision-capable model installed (default: `qwen3-vl:4b`)
- `pdftoppm` or Ghostscript for PDF page rendering; ImageMagick is recommended for image orientation/conversion
- NVIDIA drivers and CUDA are optional; CPU-only fallback parsing is available, but model inference may be slower

The parser worker can use its PaddleOCR-VL backend when the model dependencies and weights are available. It also has a local fallback path for environments where those weights are not installed.

## Quick start

### 1. Start Ollama

Install and start Ollama using its instructions for your platform, then pull the default model:

```bash
ollama pull qwen3-vl:4b
```

### 2. Install parser worker dependencies

```bash
cd worker_paddle
uv sync
cd ..
```

The server can start the Python worker automatically. To start it separately instead:

```bash
cd worker_paddle
uv run python server.py --port 8091
```

### 3. Start the app

From the repository root:

```bash
go run ./cmd/server \
  --port 8080 \
  --db ./data/ocrgen.db \
  --workdir ./data/pages \
  --ollama-url http://127.0.0.1:11434 \
  --paddle-url http://127.0.0.1:8091
```

Open **http://127.0.0.1:8080** in your browser, upload an invoice, review the extracted fields and audit flags, and approve or correct the result. The server binds to `127.0.0.1` by default.

To use different Ollama models, pass `--text-model` and `--vlm-model`. The defaults use the same model for both paths.

## Run the benchmark

The repository includes a small synthetic benchmark under `eval/datasets/sample_bench`:

```bash
go run ./cmd/eval \
  --dataset ./eval/datasets/sample_bench \
  --report ./eval/reports/latest_report.md \
  --hw-profile ./docs/hardware_profile.md \
  --text-model qwen3-vl:4b \
  --vlm-model qwen3-vl:4b
```

The evaluation CLI needs Ollama and the parser worker available. It writes extraction metrics and runtime measurements to the report paths. Hardware measurements are meaningful only when run on the target machine with the relevant services and GPU tools available.

To regenerate the synthetic sample invoices, see `eval/datasets/generate_bench.py`.

## API

- `GET /api/health` — service health
- `GET /api/invoices` — list or search processed invoices
- `POST /api/invoices/upload` — upload and process an invoice (`multipart/form-data`, field `file`)
- `GET /api/invoices/{id}` — invoice details and extraction results
- `GET /api/invoices/{id}/page/{page}` — rendered page image
- `POST /api/invoices/{id}/review` — save a reviewed/corrected extraction
- `GET /api/fraud/benford` — batch-level Benford analysis
- `GET /api/memory` — reviewer examples used for adaptive prompts

## Configuration

The server and evaluator expose command-line flags for database and work directories, Ollama and parser URLs, model names, and context size. Run either command with `-h` to see available options.

Runtime files are written under `data/` by default. Keep that directory private: it can contain the SQLite database, source invoices, and rendered pages.

## Limitations

- Extraction quality depends on invoice scan quality, model availability, and model performance; always review uncertain or conflicting fields.
- Vendor outlier checks need sufficient invoice history. Benford analysis is intended for batches, not individual invoices.
- The included benchmark is small and synthetic. It is useful for exercising the pipeline, not for claiming production accuracy or real-world fraud detection rates.
- Live evaluation and hardware profiling require the local inference services and target hardware.

## License

No license is currently included. Add a license before granting others permission to use, modify, or redistribute this project.
