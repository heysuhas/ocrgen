package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"math/bits"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"ocrgen/internal/models"
)

// Config controls document ingestion and preprocessing behavior.
type Config struct {
	WorkDir    string
	DPI        int
	EnableSkew bool
}

// DefaultConfig returns sensible ingestion defaults (~200 DPI per spec §4.1).
func DefaultConfig(workDir string) Config {
	return Config{
		WorkDir:    workDir,
		DPI:        200,
		EnableSkew: true,
	}
}

// IngestedDocument holds the content hash, stored original path, and rendered page images.
type IngestedDocument struct {
	InvoiceID      string             `json:"invoice_id"`
	Filename       string             `json:"filename"`
	OriginalPath   string             `json:"original_path"`
	FileHashSHA256 string             `json:"file_hash_sha256"`
	PrimaryPHash   string             `json:"primary_phash"`
	Pages          []models.PageAsset `json:"pages"`
}

// Ingestor renders PDFs and preprocesses images into 200 DPI page PNGs.
type Ingestor struct {
	cfg Config
}

func NewIngestor(cfg Config) (*Ingestor, error) {
	if cfg.DPI <= 0 {
		cfg.DPI = 200
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "./data/pages"
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, fmt.Errorf("create ingest workdir: %w", err)
	}
	return &Ingestor{cfg: cfg}, nil
}

// IngestFile processes a PDF or image from disk into page PNGs and computes SHA256 + perceptual hashes.
func (ing *Ingestor) IngestFile(invoiceID, srcPath string) (*IngestedDocument, error) {
	fileBytes, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("read source file: %w", err)
	}
	sum := sha256.Sum256(fileBytes)
	hashHex := hex.EncodeToString(sum[:])

	docDir := filepath.Join(ing.cfg.WorkDir, invoiceID)
	if err := os.MkdirAll(docDir, 0o755); err != nil {
		return nil, fmt.Errorf("create doc dir: %w", err)
	}

	ext := strings.ToLower(filepath.Ext(srcPath))
	origDest := filepath.Join(docDir, "original"+ext)
	if err := os.WriteFile(origDest, fileBytes, 0o644); err != nil {
		return nil, fmt.Errorf("save original file: %w", err)
	}

	var pagePaths []string
	if ext == ".pdf" || isPDFBytes(fileBytes) {
		pagePaths, err = ing.renderPDF(origDest, docDir)
		if err != nil {
			return nil, fmt.Errorf("render pdf: %w", err)
		}
	} else {
		pageOut := filepath.Join(docDir, "page-1.png")
		if err := ing.normalizeImage(origDest, pageOut); err != nil {
			return nil, fmt.Errorf("normalize image: %w", err)
		}
		pagePaths = []string{pageOut}
	}

	var pages []models.PageAsset
	for idx, p := range pagePaths {
		if ing.cfg.EnableSkew {
			_ = ing.preprocessDeskew(p)
		}
		w, h, phash, err := ComputeImageMetaAndPHash(p)
		if err != nil {
			return nil, fmt.Errorf("compute phash for page %d: %w", idx+1, err)
		}
		pages = append(pages, models.PageAsset{
			PageNumber: idx + 1,
			ImagePath:  p,
			Width:      w,
			Height:     h,
			PHash:      phash,
			PHashHex:   fmt.Sprintf("%016x", phash),
		})
	}

	primaryPHash := ""
	if len(pages) > 0 {
		primaryPHash = pages[0].PHashHex
	}

	return &IngestedDocument{
		InvoiceID:      invoiceID,
		Filename:       filepath.Base(srcPath),
		OriginalPath:   origDest,
		FileHashSHA256: hashHex,
		PrimaryPHash:   primaryPHash,
		Pages:          pages,
	}, nil
}

// IngestReader saves an uploaded stream and runs IngestFile.
func (ing *Ingestor) IngestReader(invoiceID, filename string, r io.Reader) (*IngestedDocument, error) {
	tmpDir := filepath.Join(ing.cfg.WorkDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	safeBase := filepath.Base(filename)
	if safeBase == "" || safeBase == "." {
		safeBase = "upload.png"
	}
	tmpPath := filepath.Join(tmpDir, invoiceID+"_"+safeBase)
	f, err := os.Create(tmpPath)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	defer os.Remove(tmpPath)

	doc, err := ing.IngestFile(invoiceID, tmpPath)
	if err != nil {
		return nil, err
	}
	doc.Filename = safeBase
	return doc, nil
}

func isPDFBytes(b []byte) bool {
	return len(b) >= 4 && string(b[:4]) == "%PDF"
}

func (ing *Ingestor) renderPDF(pdfPath, outDir string) ([]string, error) {
	prefix := filepath.Join(outDir, "page")
	dpiStr := fmt.Sprintf("%d", ing.cfg.DPI)

	// Primary: pdftoppm -png -r 200
	cmd := exec.Command("pdftoppm", "-png", "-r", dpiStr, pdfPath, prefix)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Fallback to ghostscript if pdftoppm fails
		gsPattern := filepath.Join(outDir, "page-%d.png")
		gsCmd := exec.Command("gs", "-dSAFER", "-dBATCH", "-dNOPAUSE", "-sDEVICE=png16m", "-r"+dpiStr, "-sOutputFile="+gsPattern, pdfPath)
		if gsOut, gsErr := gsCmd.CombinedOutput(); gsErr != nil {
			return nil, fmt.Errorf("pdftoppm (%v: %s) and gs (%v: %s) failed", err, string(out), gsErr, string(gsOut))
		}
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		return nil, err
	}
	var pages []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "page") && strings.HasSuffix(name, ".png") {
			pages = append(pages, filepath.Join(outDir, name))
		}
	}
	sort.Strings(pages)
	if len(pages) == 0 {
		return nil, fmt.Errorf("no pages rendered from PDF %s", pdfPath)
	}
	return pages, nil
}

func (ing *Ingestor) normalizeImage(srcPath, dstPNG string) error {
	f, err := os.Open(srcPath)
	if err == nil {
		img, _, decErr := image.Decode(f)
		f.Close()
		if decErr == nil {
			out, err := os.Create(dstPNG)
			if err != nil {
				return err
			}
			defer out.Close()
			return png.Encode(out, img)
		}
	}
	// Fallback to ImageMagick for TIFF/WebP/BMP
	cmd := exec.Command("magick", srcPath, "-auto-orient", dstPNG)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("magick convert failed: %v (%s)", err, string(out))
	}
	return nil
}

func (ing *Ingestor) preprocessDeskew(pngPath string) error {
	// Light auto-orient only to preserve sharp OCR glyph boundaries unless image is skewed
	cmd := exec.Command("magick", pngPath, "-auto-orient", pngPath)
	_ = cmd.Run()
	return nil
}

// ComputeImageMetaAndPHash decodes an image and computes its width, height, and 64-bit difference hash (dHash).
// dHash divides the image into a 9x8 luminance grid and compares adjacent horizontal cells (8x8 = 64 bits).
func ComputeImageMetaAndPHash(imgPath string) (int, int, uint64, error) {
	f, err := os.Open(imgPath)
	if err != nil {
		return 0, 0, 0, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return 0, 0, 0, err
	}

	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	if w <= 0 || h <= 0 {
		return 0, 0, 0, fmt.Errorf("invalid image dimensions %dx%d", w, h)
	}

	const gridW = 9
	const gridH = 8
	var luma [gridH][gridW]float64

	for gy := 0; gy < gridH; gy++ {
		y0 := bounds.Min.Y + (gy*h)/gridH
		y1 := bounds.Min.Y + ((gy+1)*h)/gridH
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for gx := 0; gx < gridW; gx++ {
			x0 := bounds.Min.X + (gx*w)/gridW
			x1 := bounds.Min.X + ((gx+1)*w)/gridW
			if x1 <= x0 {
				x1 = x0 + 1
			}
			// Sample up to 4x4 points inside each cell for stable box-filtered luminance
			stepY := (y1 - y0) / 4
			if stepY < 1 {
				stepY = 1
			}
			stepX := (x1 - x0) / 4
			if stepX < 1 {
				stepX = 1
			}
			var sum float64
			var cnt int
			for y := y0; y < y1; y += stepY {
				for x := x0; x < x1; x += stepX {
					r, g, b, _ := img.At(x, y).RGBA()
					// ITU-R BT.601 luminance
					yVal := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
					sum += yVal
					cnt++
				}
			}
			if cnt > 0 {
				luma[gy][gx] = sum / float64(cnt)
			}
		}
	}

	var hash uint64
	var bitIdx uint
	for gy := 0; gy < gridH; gy++ {
		for gx := 0; gx < gridW-1; gx++ {
			if luma[gy][gx] > luma[gy][gx+1] {
				hash |= (1 << bitIdx)
			}
			bitIdx++
		}
	}

	return w, h, hash, nil
}

// HammingDistanceHex returns the number of differing bits between two 16-char hex perceptual hashes.
func HammingDistanceHex(hexA, hexB string) (int, error) {
	if len(hexA) == 0 || len(hexB) == 0 {
		return 64, fmt.Errorf("empty phash")
	}
	var a, b uint64
	if _, err := fmt.Sscanf(hexA, "%x", &a); err != nil {
		return 64, err
	}
	if _, err := fmt.Sscanf(hexB, "%x", &b); err != nil {
		return 64, err
	}
	return bits.OnesCount64(a ^ b), nil
}
