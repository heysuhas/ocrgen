package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"ocrgen/internal/api"
	"ocrgen/internal/engines"
	"ocrgen/internal/fraud"
	"ocrgen/internal/ingest"
	"ocrgen/internal/memory"
	"ocrgen/internal/orchestrator"
	"ocrgen/internal/store"
)

func main() {
	var (
		port       = flag.Int("port", 8080, "HTTP server port")
		dbPath     = flag.String("db", "./data/ocrgen.db", "SQLite database path")
		workDir    = flag.String("workdir", "./data/pages", "Directory for rendered 200 DPI page PNGs")
		ollamaURL  = flag.String("ollama-url", "http://127.0.0.1:11434", "Ollama server base URL")
		textModel  = flag.String("text-model", "qwen3-vl:4b", "Ollama model for Engine A text extraction")
		vlmModel   = flag.String("vlm-model", "qwen3-vl:4b", "Ollama model for Engine B vision extraction")
		numCtx     = flag.Int("num-ctx", 16384, "Context window tokens for Ollama models")
		paddleURL  = flag.String("paddle-url", "http://127.0.0.1:8091", "PaddleOCR-VL-1.6 Python worker URL")
		workerPath = flag.String("worker-script", "./worker_paddle/server.py", "Path to worker_paddle/server.py for auto-start")
		autoWorker = flag.Bool("auto-start-worker", true, "Auto-start worker_paddle/server.py if not running")
	)
	flag.Parse()

	ing, err := ingest.NewIngestor(ingest.DefaultConfig(*workDir))
	if err != nil {
		log.Fatalf("failed to init ingestor: %v", err)
	}

	st, err := store.NewStore(*dbPath)
	if err != nil {
		log.Fatalf("failed to init sqlite store: %v", err)
	}
	defer st.Close()

	parser := engines.NewPaddleHTTPParser(*paddleURL, *autoWorker, *workerPath, *ollamaURL, *vlmModel)
	defer parser.Close()

	engA := engines.NewOllamaExtractor(engines.OllamaConfig{
		BaseURL:    *ollamaURL,
		Model:      *textModel,
		NumCtx:     *numCtx,
		VisionMode: false,
	})
	engB := engines.NewOllamaExtractor(engines.OllamaConfig{
		BaseURL:    *ollamaURL,
		Model:      *vlmModel,
		NumCtx:     *numCtx,
		VisionMode: true,
	})

	aud := fraud.NewAuditor(fraud.DefaultConfig())
	ret := memory.NewRetriever(2, 0.28)
	coord := orchestrator.NewCoordinator(ing, parser, engA, engB, st, aud, ret)

	srv := api.NewServer(coord, st, aud)
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("[ocrgen-server] Dashboard & API listening on http://%s (EngineA=%s, EngineB=%s, num_ctx=%d)", addr, *textModel, *vlmModel, *numCtx)
	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
