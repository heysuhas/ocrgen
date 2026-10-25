#!/usr/bin/env python3
"""
Downloads invoices and ground-truth annotations from huggingface.co/datasets/jngb-labs/InvoiceBenchmark
and formats them for OCRGen's evaluation runner.
"""

import argparse
import json
import os
import sys
import urllib.request
from pathlib import Path


def download_file(url: str, dest_path: str) -> None:
    req = urllib.request.Request(url, headers={"User-Agent": "OCRGen-Benchmark-Loader/1.0"})
    with urllib.request.urlopen(req) as resp, open(dest_path, "wb") as out:
        out.write(resp.read())


def fetch_benchmark(dest_dir: str, count: int, start_idx: int = 1) -> str:
    out_dir = Path(dest_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)

    manifest = []
    print(f"[*] Downloading {count} sample(s) from jngb-labs/InvoiceBenchmark starting at index {start_idx}...")

    for i in range(start_idx, start_idx + count):
        inv_id = f"INV-2026-{i:04d}"
        pdf_filename = f"{inv_id}.pdf"
        pdf_dest = out_dir / pdf_filename

        gt_url = f"https://huggingface.co/datasets/jngb-labs/InvoiceBenchmark/raw/main/output/ground_truth/{inv_id}.json"
        pdf_url = f"https://huggingface.co/datasets/jngb-labs/InvoiceBenchmark/resolve/main/output/pdf/{inv_id}.pdf"

        # Download PDF if not present
        if not pdf_dest.exists() or pdf_dest.stat().st_size == 0:
            print(f"  -> Downloading {pdf_filename}...")
            try:
                download_file(pdf_url, str(pdf_dest))
            except Exception as e:
                print(f"  [!] Failed to download PDF {pdf_url}: {e}")
                continue

        # Fetch and parse JSON ground truth
        try:
            req = urllib.request.Request(gt_url, headers={"User-Agent": "OCRGen-Benchmark-Loader/1.0"})
            with urllib.request.urlopen(req) as resp:
                raw_gt = json.loads(resp.read().decode("utf-8"))
        except Exception as e:
            print(f"  [!] Failed to download GT {gt_url}: {e}")
            continue

        # Map to OCRGen models.ExtractedInvoice schema
        subtotal = float(raw_gt.get("subtotal") or 0.0)
        tax_total = float(raw_gt.get("vat_amount") or raw_gt.get("tax_amount") or 0.0)
        total = float(raw_gt.get("total") or 0.0)

        line_items = []
        for li in raw_gt.get("line_items", []):
            line_items.append({
                "description": li.get("description", ""),
                "quantity": float(li.get("quantity") or 1.0),
                "unit_price": float(li.get("unit_price") or 0.0),
                "amount": float(li.get("amount") or 0.0),
            })

        sample = {
            "id": inv_id,
            "file_path": str(pdf_dest),
            "layout": raw_gt.get("variants", {}).get("layout", "standard"),
            "ground_truth": {
                "vendor_name": raw_gt.get("vendor", ""),
                "vendor_tax_id": raw_gt.get("vendor_tax_id", "") or raw_gt.get("vendor_vat", ""),
                "invoice_number": raw_gt.get("invoice_id", inv_id),
                "invoice_date": raw_gt.get("date", ""),
                "due_date": raw_gt.get("due_date", ""),
                "currency": raw_gt.get("currency", "EUR"),
                "subtotal": subtotal,
                "tax_total": tax_total,
                "total": total,
                "bank_details": raw_gt.get("bank_details", ""),
                "line_items": line_items,
            },
        }
        manifest.append(sample)

    manifest_path = out_dir / "ground_truth.json"
    with open(manifest_path, "w", encoding="utf-8") as f:
        json.dump(manifest, f, indent=2)

    print(f"[✓] Prepared {len(manifest)} benchmark samples in {out_dir}")
    print(f"[✓] Manifest written to {manifest_path}")
    return str(manifest_path)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Download HF InvoiceBenchmark samples")
    parser.add_argument("--dest", default="./eval/datasets/hf_smoke_2", help="Destination folder")
    parser.add_argument("--count", type=int, default=2, help="Number of invoices to download")
    parser.add_argument("--start", type=int, default=1, help="Starting invoice index")
    args = parser.parse_args()

    fetch_benchmark(args.dest, args.count, args.start)
