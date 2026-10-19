package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"ocrgen/internal/fraud"
	"ocrgen/internal/models"
	"ocrgen/internal/orchestrator"
	"ocrgen/internal/store"
)

// Server exposes the REST API and embedded Web Dashboard.
type Server struct {
	coord   *orchestrator.Coordinator
	store   *store.Store
	auditor *fraud.Auditor
	mux     *http.ServeMux
}

func NewServer(coord *orchestrator.Coordinator, st *store.Store, aud *fraud.Auditor) *Server {
	s := &Server{
		coord:   coord,
		store:   st,
		auditor: aud,
		mux:     http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/api/health", s.handleHealth)
	s.mux.HandleFunc("/api/invoices", s.handleInvoices)
	s.mux.HandleFunc("/api/invoices/upload", s.handleUpload)
	s.mux.HandleFunc("/api/invoices/", s.handleInvoiceDetail)
	s.mux.HandleFunc("/api/fraud/benford", s.handleBenford)
	s.mux.HandleFunc("/api/memory", s.handleAdaptiveMemory)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(dashboardHTML))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleInvoices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))

	var list []models.InvoiceRecord
	var err error
	if q != "" {
		list, err = s.store.SearchInvoices(q)
	} else {
		list, err = s.store.ListInvoices(status)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if list == nil {
		list = []models.InvoiceRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"invoices": list})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid multipart upload: " + err.Error()})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing 'file' field: " + err.Error()})
		return
	}
	defer file.Close()

	opts := orchestrator.ProcessOptions{
		DisableEngineB:  r.FormValue("disable_engine_b") == "true",
		DisableAdaptive: r.FormValue("disable_adaptive") == "true",
	}

	invID := fmt.Sprintf("inv-%d", time.Now().UnixNano()/1e6)
	rec, err := s.coord.ProcessUpload(r.Context(), invID, header.Filename, file, opts)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleInvoiceDetail(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/invoices/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing invoice id"})
		return
	}
	invID := parts[0]

	// Route: /api/invoices/{id}/page/{pageNum}
	if len(parts) == 3 && parts[1] == "page" {
		rec, err := s.store.GetInvoice(invID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		var pageNum int
		_, _ = fmt.Sscanf(parts[2], "%d", &pageNum)
		if pageNum <= 0 {
			pageNum = 1
		}
		for _, pg := range rec.Pages {
			if pg.PageNumber == pageNum {
				data, err := os.ReadFile(pg.ImagePath)
				if err != nil {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(data)
				return
			}
		}
		http.NotFound(w, r)
		return
	}

	// Route: POST /api/invoices/{id}/review
	if len(parts) == 2 && parts[1] == "review" && r.Method == http.MethodPost {
		var sub models.ReviewSubmission
		if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
			return
		}
		sub.InvoiceID = invID
		updated, err := s.store.RecordReview(sub)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, updated)
		return
	}

	// Route: GET /api/invoices/{id}
	if r.Method == http.MethodGet {
		rec, err := s.store.GetInvoice(invID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rec)
		return
	}

	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
}

func (s *Server) handleBenford(w http.ResponseWriter, r *http.Request) {
	invoices, err := s.store.ListInvoices("")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var amounts []float64
	for _, inv := range invoices {
		if inv.Reconciled.Total > 0 {
			amounts = append(amounts, inv.Reconciled.Total)
		}
		for _, li := range inv.LineItems {
			if li.Amount > 0 {
				amounts = append(amounts, li.Amount)
			}
		}
	}
	report := s.auditor.AnalyzeBenford("all_stored_invoices_and_lines", amounts)
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleAdaptiveMemory(w http.ResponseWriter, r *http.Request) {
	examples, err := s.store.ListAdaptiveExamples()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if examples == nil {
		examples = []models.AdaptiveExample{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"examples": examples})
}
