package ingest

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestIngestAndPHash(t *testing.T) {
	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "test_inv.png")

	img := image.NewRGBA(image.Rect(0, 0, 400, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 400; x++ {
			val := uint8((x + y*2) % 255)
			img.Set(x, y, color.RGBA{R: val, G: val, B: val, A: 255})
		}
	}
	f, err := os.Create(imgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()

	ing, err := NewIngestor(Config{WorkDir: filepath.Join(tmpDir, "work"), DPI: 200, EnableSkew: false})
	if err != nil {
		t.Fatal(err)
	}

	doc, err := ing.IngestFile("inv-test-1", imgPath)
	if err != nil {
		t.Fatalf("IngestFile failed: %v", err)
	}
	if len(doc.Pages) != 1 {
		t.Fatalf("expected 1 page, got %d", len(doc.Pages))
	}
	if doc.FileHashSHA256 == "" || doc.PrimaryPHash == "" {
		t.Fatalf("expected non-empty hashes, got sha=%s phash=%s", doc.FileHashSHA256, doc.PrimaryPHash)
	}

	dist, err := HammingDistanceHex(doc.PrimaryPHash, doc.PrimaryPHash)
	if err != nil || dist != 0 {
		t.Fatalf("expected distance 0 for identical image, got %d (err=%v)", dist, err)
	}
}
