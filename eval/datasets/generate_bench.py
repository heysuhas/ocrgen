#!/usr/bin/env python3
"""
Generates a reproducible multi-layout invoice benchmark dataset (`eval/datasets/sample_bench`)
containing both PNG and PDF invoices + `ground_truth.json` for evaluating Phases 1–6:
- Standard US/EU/UK invoices across multiple layouts
- Held-out tricky layout vendor invoices (for evaluating adaptive few-shot learning)
"""

import json
import os
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont


def get_font(size: int):
    for candidate in [
        "/usr/share/fonts/google-noto-vf/NotoSans[wght].ttf",
        "/usr/share/fonts/dejavu-sans-fonts/DejaVuSans.ttf",
        "/usr/share/fonts/liberation-sans/LiberationSans-Regular.ttf",
    ]:
        if os.path.exists(candidate):
            try:
                return ImageFont.truetype(candidate, size=size)
            except Exception:
                pass
    return ImageFont.load_default()


def render_invoice_card(out_png: Path, out_pdf: Path, spec: dict) -> None:
    w, h = 1400, 1800
    img = Image.new("RGB", (w, h), "white")
    draw = ImageDraw.Draw(img)

    f_title = get_font(34)
    f_sub = get_font(24)
    f_body = get_font(22)
    f_small = get_font(19)

    gt = spec["ground_truth"]
    layout = spec.get("layout", "standard")

    if layout == "standard":
        # Header
        draw.text((80, 70), gt["vendor_name"], fill="black", font=f_title)
        draw.text((80, 120), f"Tax ID / EIN: {gt['vendor_tax_id']}", fill="#222222", font=f_body)
        draw.text((850, 70), "COMMERCIAL INVOICE", fill="black", font=f_title)
        draw.text((850, 125), f"Invoice Number: {gt['invoice_number']}", fill="black", font=f_body)
        draw.text((850, 160), f"Invoice Date: {gt['invoice_date']}", fill="black", font=f_body)
        draw.text((850, 195), f"Due Date: {gt['due_date']}", fill="black", font=f_body)
        draw.text((850, 230), f"Currency: {gt['currency']}", fill="black", font=f_body)

        draw.line([(80, 290), (1320, 290)], fill="black", width=3)

        # Table header
        y = 320
        draw.text((80, y), "Description", fill="black", font=f_sub)
        draw.text((750, y), "Qty", fill="black", font=f_sub)
        draw.text((920, y), "Unit Price", fill="black", font=f_sub)
        draw.text((1140, y), "Amount", fill="black", font=f_sub)
        draw.line([(80, y + 40), (1320, y + 40)], fill="black", width=2)

        y += 65
        for item in gt["line_items"]:
            draw.text((80, y), item["description"], fill="black", font=f_body)
            draw.text((750, y), f"{item['quantity']:.0f}", fill="black", font=f_body)
            draw.text((920, y), f"{item['unit_price']:.2f}", fill="black", font=f_body)
            draw.text((1140, y), f"{item['amount']:.2f}", fill="black", font=f_body)
            y += 50

        draw.line([(80, y + 20), (1320, y + 20)], fill="black", width=2)
        y += 50
        draw.text((880, y), f"Subtotal: {gt['currency']} {gt['subtotal']:.2f}", fill="black", font=f_sub)
        y += 45
        draw.text((880, y), f"Tax Total: {gt['currency']} {gt['tax_total']:.2f}", fill="black", font=f_sub)
        y += 50
        draw.text((880, y), f"TOTAL DUE: {gt['currency']} {gt['total']:.2f}", fill="black", font=f_title)

        y += 120
        draw.text((80, y), f"Remittance Bank / IBAN: {gt['bank_details']}", fill="black", font=f_small)
    else:
        # Compact / European-style tabular layout (used to test layout generalization & adaptive few-shot)
        draw.rectangle([(60, 60), (1340, 240)], outline="black", width=2)
        draw.text((90, 85), f"ISSUER: {gt['vendor_name']}  |  REG/VAT: {gt['vendor_tax_id']}", fill="black", font=f_sub)
        draw.text((90, 135), f"DOC REF: {gt['invoice_number']}   ISSUED ON: {gt['invoice_date']}   PAY BY: {gt['due_date']}", fill="black", font=f_body)
        draw.text((90, 180), f"BANK ACCOUNT / IBAN: {gt['bank_details']}   CURR: {gt['currency']}", fill="black", font=f_body)

        y = 300
        draw.text((80, y), "ITEMIZED CHARGES (POS | ITEM | UNITS | RATE | LINE TOTAL)", fill="black", font=f_sub)
        draw.line([(80, y + 38), (1320, y + 38)], fill="black", width=2)
        y += 60
        for idx, item in enumerate(gt["line_items"]):
            line_str = f"0{idx+1} | {item['description']} | {item['quantity']:.0f} | {item['unit_price']:.2f} | {item['amount']:.2f}"
            draw.text((80, y), line_str, fill="black", font=f_body)
            y += 50
        draw.line([(80, y + 20), (1320, y + 20)], fill="black", width=2)
        y += 50
        draw.text((80, y), f"NET AMOUNT (SUBTOTAL): {gt['subtotal']:.2f} {gt['currency']}", fill="black", font=f_sub)
        y += 45
        draw.text((80, y), f"VAT / TAX CHARGE: {gt['tax_total']:.2f} {gt['currency']}", fill="black", font=f_sub)
        y += 50
        draw.text((80, y), f"GRAND TOTAL PAYABLE: {gt['total']:.2f} {gt['currency']}", fill="black", font=f_title)

    img.save(out_png, format="PNG")
    if out_pdf:
        img.save(out_pdf, format="PDF", resolution=200.0)


def main() -> None:
    out_dir = Path(__file__).parent / "sample_bench"
    out_dir.mkdir(parents=True, exist_ok=True)

    samples = [
        {
            "id": "bench-001",
            "filename": "inv_acme_001.pdf",
            "format": "pdf",
            "layout": "standard",
            "ground_truth": {
                "vendor_name": "Acme Cloud Infrastructure Inc",
                "vendor_tax_id": "US-94-3321908",
                "invoice_number": "INV-2026-1041",
                "invoice_date": "2026-05-04",
                "due_date": "2026-06-03",
                "currency": "USD",
                "subtotal": 1450.00,
                "tax_total": 116.00,
                "total": 1566.00,
                "bank_details": "US88ACME0009876543",
                "line_items": [
                    {
                        "description": "Dedicated A100 GPU Instance",
                        "quantity": 2,
                        "unit_price": 600.00,
                        "amount": 1200.00,
                    },
                    {
                        "description": "NVMe Block Storage 5TB",
                        "quantity": 1,
                        "unit_price": 250.00,
                        "amount": 250.00,
                    },
                ],
            },
        },
        {
            "id": "bench-002",
            "filename": "inv_hyperion_002.png",
            "format": "png",
            "layout": "standard",
            "ground_truth": {
                "vendor_name": "Hyperion Precision Optics GmbH",
                "vendor_tax_id": "DE298441023",
                "invoice_number": "HYP-88201",
                "invoice_date": "2026-05-06",
                "due_date": "2026-06-05",
                "currency": "EUR",
                "subtotal": 2400.00,
                "tax_total": 456.00,
                "total": 2856.00,
                "bank_details": "DE89370400440532013000",
                "line_items": [
                    {
                        "description": "Laser Interferometer Sensor",
                        "quantity": 3,
                        "unit_price": 800.00,
                        "amount": 2400.00,
                    }
                ],
            },
        },
        {
            "id": "bench-003",
            "filename": "inv_vanguard_003.pdf",
            "format": "pdf",
            "layout": "compact",
            "ground_truth": {
                "vendor_name": "Vanguard Industrial Robotics Ltd",
                "vendor_tax_id": "GB774910221",
                "invoice_number": "VG-2026-077",
                "invoice_date": "2026-05-08",
                "due_date": "2026-06-07",
                "currency": "GBP",
                "subtotal": 920.00,
                "tax_total": 184.00,
                "total": 1104.00,
                "bank_details": "GB29NWBK60161331926819",
                "line_items": [
                    {
                        "description": "Harmonic Drive Actuator Module",
                        "quantity": 4,
                        "unit_price": 230.00,
                        "amount": 920.00,
                    }
                ],
            },
        },
    ]

    manifest = []
    for s in samples:
        png_path = out_dir / (Path(s["filename"]).stem + ".png")
        pdf_path = out_dir / (Path(s["filename"]).stem + ".pdf")
        render_invoice_card(png_path, pdf_path, s)
        target_file = pdf_path if s["format"] == "pdf" else png_path
        manifest.append(
            {
                "id": s["id"],
                "file_path": str(target_file),
                "layout": s["layout"],
                "ground_truth": s["ground_truth"],
            }
        )

    manifest_path = out_dir / "ground_truth.json"
    manifest_path.write_text(json.dumps(manifest, indent=2), encoding="utf-8")
    print(f"Generated {len(manifest)} benchmark invoices in {out_dir}")


if __name__ == "__main__":
    main()
