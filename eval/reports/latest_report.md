# Evaluation Report

- **Generated:** `2026-10-25T15:11:10Z`
- **GPU:** NVIDIA GeForce RTX 4050 Laptop GPU (`6,141 MiB` VRAM)
- **CPU / RAM:** AMD Ryzen 7 7445HS (12 threads), 14 GiB System RAM
- **Engine A Parser:** `PaddlePaddle/PaddleOCR-VL-1.6` Worker
- **Engine A Text LLM:** `qwen3-vl:4b`; **Engine B Vision VLM:** `qwen3-vl:4b` via Ollama (`num_ctx=16384`, `think=false`, `temp=0`)

## 1. Per-Field Accuracy: Engine A vs. Engine B vs. Dual-Engine Consensus

| Field | Samples | Engine A Exact | Engine A Token F1 | Engine B Exact | Engine B Token F1 | Consensus Exact | Consensus Token F1 | Auto-Verified Coverage | Accuracy on Verified Subset |
|---|---|---|---|---|---|---|
| `vendor_name` | 3 | 100.0% | 1.000 | 100.0% | 1.000 | **100.0%** | **1.000** | 100.0% (3/3) | **100.0%** |
| `vendor_tax_id` | 3 | 100.0% | 1.000 | 100.0% | 1.000 | **100.0%** | **1.000** | 100.0% (3/3) | **100.0%** |
| `invoice_number` | 3 | 100.0% | 1.000 | 100.0% | 1.000 | **100.0%** | **1.000** | 100.0% (3/3) | **100.0%** |
| `invoice_date` | 3 | 100.0% | 1.000 | 100.0% | 1.000 | **100.0%** | **1.000** | 100.0% (3/3) | **100.0%** |
| `due_date` | 3 | 100.0% | 1.000 | 100.0% | 1.000 | **100.0%** | **1.000** | 100.0% (3/3) | **100.0%** |
| `currency` | 3 | 100.0% | 1.000 | 100.0% | 1.000 | **100.0%** | **1.000** | 100.0% (3/3) | **100.0%** |
| `subtotal` | 3 | 100.0% | 1.000 | 100.0% | 1.000 | **100.0%** | **1.000** | 100.0% (3/3) | **100.0%** |
| `tax_total` | 3 | 100.0% | 1.000 | 100.0% | 1.000 | **100.0%** | **1.000** | 100.0% (3/3) | **100.0%** |
| `total` | 3 | 100.0% | 1.000 | 100.0% | 1.000 | **100.0%** | **1.000** | 100.0% (3/3) | **100.0%** |
| `bank_details` | 3 | 66.7% | 0.667 | 33.3% | 0.333 | **66.7%** | **0.667** | 66.7% (2/3) | **50.0%** |
| **OVERALL (Micro-Avg)** | **30** | **96.7%** | **0.967** | **93.3%** | **0.933** | **96.7%** | **0.967** | **96.7% (29/30)** | **96.6%** |

## 2. Consensus & Workflow Metrics

- **Inter-Engine Agreement Rate:** `96.7%`
- **Auto-Accepted Invoices:** `3 / 3 (100.0%)`
- **Human Review Rate:** `0 / 3 (0.0%)`

## 3. Fraud Audit Precision & Recall on Injected Anomalies

| Anomaly Type | TP | FP | FN | Precision | Recall | F1 |
|---|---|---|---|---|---|---|
| `duplicate_exact` | 1 | 0 | 0 | 1.00 | 1.00 | **1.00** |
| `duplicate_phash` | 1 | 0 | 0 | 1.00 | 1.00 | **1.00** |
| `vendor_outlier_z` | 1 | 0 | 0 | 1.00 | 1.00 | **1.00** |
| `rule_bank_change` | 1 | 0 | 0 | 1.00 | 1.00 | **1.00** |
| `rule_math_mismatch` | 1 | 0 | 0 | 1.00 | 1.00 | **1.00** |

### Batch-Level Benford's Law Evaluation
- **Natural Invoice Batch ($n=120$):** $\chi^2 = 3.73$ (crit $15.51$), $\text{MAD} = 0.0137$ → `marginally_acceptable` (Suspicious=`false`)
- **Manipulated Threshold-Splitting Batch ($n=90$):** $\chi^2 = 1186.73$ (crit $15.51$), $\text{MAD} = 0.2007$ → `nonconformity` (Suspicious=`true`)

## 4. Adaptive Few-Shot Memory Verification

- **Result:** Retrieved 1 prior human correction(s) for vendor `vanguard industrial robotics` (similarity=0.605, canonical entity=`Vanguard Industrial Robotics Ltd`, VAT=`GB774910221`)
- **Before Correction (Zero-Shot):** Vendor=`Vanguard Ind. Robotics`, VAT=`GB774910221` (Canonical Match: `false`, F1: `0.571`)
- **After Correction (Few-Shot Injected):** Vendor=`Vanguard Ind. Robotics`, VAT=`GB774910221` (Canonical Match: `false`, F1: `0.571`)
- **Adaptive Accuracy Gain:** Canonical token F1 improved from `0.571` to `0.571` (`false` -> `false`)

## 5. Hardware, Latency & Memory Profile (RTX 4050 6 GB VRAM)

| Metric | Measured Value |
|---|---|
| **Go Backend Heap Alloc** | `18.62 MiB` |
| **Go Backend Total Sys RAM** | `33.41 MiB` |
| **Peak GPU VRAM Used** | `5470 MiB / 6141 MiB` |
| **Avg Stage 1 (`PaddleOCR-VL-1.6` Parse) Latency** | `11947 ms` |
| **Avg Stage 2 (Engine A Text LLM) Latency** | `15835 ms` |
| **Avg Stage 3 (Engine B Vision VLM) Latency** | `13638 ms` |
| **Avg Total End-to-End Invoice Latency** | `41483 ms` |
