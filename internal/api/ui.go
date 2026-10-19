package api

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>OCRGen — Dual-Engine Invoice Consensus & Fraud Audit</title>
<style>
  :root {
    --bg: #0f172a;
    --panel: #1e293b;
    --border: #334155;
    --text: #f8fafc;
    --muted: #94a3b8;
    --accent: #38bdf8;
    --ok: #22c55e;
    --warn: #f59e0b;
    --err: #ef4444;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; font-family: system-ui, -apple-system, sans-serif;
    background: var(--bg); color: var(--text); font-size: 14px;
  }
  header {
    display: flex; align-items: center; justify-content: space-between;
    padding: 12px 20px; background: #020617; border-bottom: 1px solid var(--border);
  }
  header h1 { margin: 0; font-size: 17px; font-weight: 600; color: var(--accent); }
  .badge {
    display: inline-block; padding: 2px 8px; border-radius: 999px;
    font-size: 11px; font-weight: 600; text-transform: uppercase;
  }
  .badge-verified, .badge-auto_accepted, .badge-reviewed { background: rgba(34,197,94,0.2); color: #4ade80; }
  .badge-conflict, .badge-needs_review, .badge-high { background: rgba(239,68,68,0.2); color: #f87171; }
  .badge-single_source, .badge-medium { background: rgba(245,158,11,0.2); color: #fbbf24; }
  .badge-unverified, .badge-low, .badge-info { background: rgba(148,163,184,0.2); color: #cbd5e1; }
  .badge-corrected { background: rgba(56,189,248,0.2); color: #38bdf8; }

  .layout {
    display: grid; grid-template-columns: 340px 1fr; height: calc(100vh - 53px);
  }
  .sidebar {
    border-right: 1px solid var(--border); display: flex; flex-direction: column;
    background: #0b1120; overflow: hidden;
  }
  .upload-box {
    padding: 12px; border-bottom: 1px solid var(--border); background: var(--panel);
  }
  .search-box {
    padding: 10px 12px; border-bottom: 1px solid var(--border); display: flex; gap: 6px;
  }
  input, select, button, textarea {
    background: #0f172a; color: var(--text); border: 1px solid var(--border);
    border-radius: 6px; padding: 6px 10px; font-size: 13px;
  }
  button {
    cursor: pointer; background: #0284c7; border-color: #0369a1; font-weight: 600;
  }
  button:hover { background: #0369a1; }
  button.secondary { background: #334155; border-color: #475569; }
  .inv-list { flex: 1; overflow-y: auto; }
  .inv-item {
    padding: 12px; border-bottom: 1px solid var(--border); cursor: pointer;
  }
  .inv-item:hover, .inv-item.active { background: var(--panel); }
  .main { display: flex; flex-direction: column; overflow: hidden; }
  .tabs {
    display: flex; gap: 8px; padding: 10px 16px; border-bottom: 1px solid var(--border);
    background: #0b1120;
  }
  .workspace {
    display: grid; grid-template-columns: 45% 55%; flex: 1; overflow: hidden;
  }
  .viewer-pane {
    border-right: 1px solid var(--border); padding: 12px; overflow: auto;
    background: #020617; display: flex; flex-direction: column; align-items: center;
  }
  .detail-pane { padding: 16px; overflow-y: auto; }
  canvas { max-width: 100%; border: 1px solid var(--border); background: #fff; }
  table { width: 100%; border-collapse: collapse; margin-bottom: 16px; font-size: 13px; }
  th, td {
    text-align: left; padding: 7px 8px; border-bottom: 1px solid var(--border);
  }
  th { color: var(--muted); font-weight: 600; background: #0b1120; }
  tr.clickable:hover { background: rgba(56,189,248,0.08); cursor: pointer; }
  .card {
    background: var(--panel); border: 1px solid var(--border); border-radius: 8px;
    padding: 12px; margin-bottom: 14px;
  }
  .card h3 { margin: 0 0 10px 0; font-size: 14px; color: var(--accent); }
  .bar-row { display: flex; align-items: center; gap: 8px; margin: 4px 0; font-size: 12px; }
  .bar-track { flex: 1; height: 12px; background: #0f172a; border-radius: 4px; position: relative; overflow: hidden; }
  .bar-obs { height: 100%; background: var(--accent); }
</style>
</head>
<body>
<header>
  <h1>OCRGen — Local Dual-Engine Invoice Extraction (PaddleOCR-VL-1.6 + Qwen3-VL:4B)</h1>
  <div style="display:flex; gap:10px; align-items:center;">
    <button class="secondary" onclick="showBatchModal()">Batch Benford & Adaptive Memory</button>
    <span id="statusIndicator" class="badge badge-verified">Local Ready</span>
  </div>
</header>

<div class="layout">
  <div class="sidebar">
    <div class="upload-box">
      <div style="font-weight:600; margin-bottom:6px;">Ingest Invoice (PDF / PNG / JPG)</div>
      <form id="uploadForm" onsubmit="uploadInvoice(event)" style="display:flex; flex-direction:column; gap:6px;">
        <input type="file" id="fileInput" accept=".pdf,.png,.jpg,.jpeg,.webp,.tiff" required>
        <button type="submit" id="uploadBtn">Upload & Run Dual-Engine Pipeline</button>
      </form>
    </div>
    <div class="search-box">
      <input type="text" id="searchInput" placeholder="FTS5 Search vendor, # or text..." style="flex:1;" oninput="loadInvoices()">
      <select id="statusFilter" onchange="loadInvoices()">
        <option value="">All</option>
        <option value="needs_review">Needs Review</option>
        <option value="auto_accepted">Auto-Accepted</option>
        <option value="reviewed">Reviewed</option>
      </select>
    </div>
    <div class="inv-list" id="invoiceList"></div>
  </div>

  <div class="main">
    <div class="workspace" id="workspace">
      <div class="viewer-pane">
        <div style="margin-bottom:8px; color:var(--muted); font-size:12px;">
          Click any extracted field row to highlight its grounding bounding box on the page image.
        </div>
        <canvas id="pageCanvas" width="600" height="800"></canvas>
      </div>
      <div class="detail-pane" id="detailPane">
        <div style="color:var(--muted); padding:40px; text-align:center;">
          Select an invoice from the queue or upload a PDF/image to inspect Engine A vs. Engine B consensus, grounding, arithmetic validation, and fraud audit flags.
        </div>
      </div>
    </div>
  </div>
</div>

<script>
let currentInvoice = null;
let currentPageImg = null;
let activeBBox = null;

async function loadInvoices() {
  const q = document.getElementById('searchInput').value;
  const st = document.getElementById('statusFilter').value;
  const res = await fetch('/api/invoices?q=' + encodeURIComponent(q) + '&status=' + encodeURIComponent(st));
  const data = await res.json();
  const listEl = document.getElementById('invoiceList');
  listEl.innerHTML = '';
  (data.invoices || []).forEach(inv => {
    const div = document.createElement('div');
    div.className = 'inv-item' + (currentInvoice && currentInvoice.id === inv.id ? ' active' : '');
    const flagsCnt = (inv.fraud_flags || []).length;
    div.innerHTML =
      '<div style="display:flex; justify-content:space-between; align-items:center;">' +
        '<strong>' + escapeHtml(inv.reconciled.vendor_name || inv.filename) + '</strong>' +
        '<span class="badge badge-' + inv.status + '">' + inv.status + '</span>' +
      '</div>' +
      '<div style="font-size:12px; color:var(--muted); margin-top:4px; display:flex; justify-content:space-between;">' +
        '<span>#' + escapeHtml(inv.reconciled.invoice_number || 'N/A') + ' • ' + escapeHtml(inv.reconciled.currency || 'USD') + ' ' + Number(inv.reconciled.total || 0).toFixed(2) + '</span>' +
        '<span>Agree: ' + Math.round((inv.agreement_rate || 0)*100) + '%' + (flagsCnt ? ' • ⚠ ' + flagsCnt : '') + '</span>' +
      '</div>';
    div.onclick = () => selectInvoice(inv.id);
    listEl.appendChild(div);
  });
}

async function uploadInvoice(e) {
  e.preventDefault();
  const fileInput = document.getElementById('fileInput');
  if (!fileInput.files.length) return;
  const btn = document.getElementById('uploadBtn');
  btn.disabled = true;
  btn.textContent = 'Running PaddleOCR-VL-1.6 + Qwen3-VL Consensus...';
  try {
    const fd = new FormData();
    fd.append('file', fileInput.files[0]);
    const res = await fetch('/api/invoices/upload', { method: 'POST', body: fd });
    const inv = await res.json();
    if (inv.error) {
      alert('Upload error: ' + inv.error);
    } else {
      await loadInvoices();
      await selectInvoice(inv.id);
    }
  } finally {
    btn.disabled = false;
    btn.textContent = 'Upload & Run Dual-Engine Pipeline';
    fileInput.value = '';
  }
}

async function selectInvoice(id) {
  const res = await fetch('/api/invoices/' + encodeURIComponent(id));
  currentInvoice = await res.json();
  activeBBox = null;
  renderDetail(currentInvoice);
  loadPageCanvas(currentInvoice.id, 1);
  loadInvoices();
}

function loadPageCanvas(invId, pageNum) {
  const img = new Image();
  img.onload = () => {
    currentPageImg = img;
    drawCanvas();
  };
  img.src = '/api/invoices/' + encodeURIComponent(invId) + '/page/' + pageNum;
}

function drawCanvas() {
  const canvas = document.getElementById('pageCanvas');
  const ctx = canvas.getContext('2d');
  if (!currentPageImg) return;
  canvas.width = currentPageImg.width;
  canvas.height = currentPageImg.height;
  ctx.drawImage(currentPageImg, 0, 0);

  if (currentInvoice && currentInvoice.parse_result && currentInvoice.parse_result.blocks) {
    ctx.strokeStyle = 'rgba(56, 189, 248, 0.35)';
    ctx.lineWidth = 2;
    currentInvoice.parse_result.blocks.forEach(b => {
      if (b.bbox) {
        ctx.strokeRect(b.bbox.x1, b.bbox.y1, b.bbox.x2 - b.bbox.x1, b.bbox.y2 - b.bbox.y1);
      }
    });
  }

  if (activeBBox) {
    ctx.fillStyle = 'rgba(250, 204, 21, 0.28)';
    ctx.strokeStyle = '#ef4444';
    ctx.lineWidth = 4;
    ctx.fillRect(activeBBox.x1, activeBBox.y1, activeBBox.x2 - activeBBox.x1, activeBBox.y2 - activeBBox.y1);
    ctx.strokeRect(activeBBox.x1, activeBBox.y1, activeBBox.x2 - activeBBox.x1, activeBBox.y2 - activeBBox.y1);
  }
}

function highlightEvidence(idx) {
  if (!currentInvoice || !currentInvoice.fields[idx]) return;
  activeBBox = currentInvoice.fields[idx].evidence || null;
  drawCanvas();
}

function renderDetail(inv) {
  const pane = document.getElementById('detailPane');
  let flagsHtml = '';
  if (inv.fraud_flags && inv.fraud_flags.length) {
    flagsHtml = inv.fraud_flags.map(f =>
      '<div style="padding:6px 8px; margin-bottom:6px; border-left:3px solid #ef4444; background:rgba(239,68,68,0.1);">' +
        '<span class="badge badge-' + f.severity + '">' + escapeHtml(f.flag_type) + '</span> ' +
        '<span>' + escapeHtml(f.reason) + '</span>' +
      '</div>'
    ).join('');
  } else {
    flagsHtml = '<div style="color:var(--ok);">No statistical anomalies or rule violations detected.</div>';
  }

  let fieldsRows = (inv.fields || []).map((f, idx) =>
    '<tr class="clickable" onclick="highlightEvidence(' + idx + ')">' +
      '<td><strong>' + escapeHtml(f.field_name) + '</strong>' + (f.critical ? ' *' : '') + '</td>' +
      '<td><span class="badge badge-' + f.status + '">' + f.status + '</span></td>' +
      '<td>' + escapeHtml(f.engine_a_value) + '</td>' +
      '<td>' + escapeHtml(f.engine_b_value) + '</td>' +
      '<td><input type="text" id="edit_' + f.field_name + '" value="' + escapeHtml(f.display_value) + '" style="width:100%;"></td>' +
      '<td>' + (f.grounded ? '✓' : '—') + ' (' + Math.round((f.confidence||0)*100) + '%)</td>' +
    '</tr>'
  ).join('');

  let checksRows = ((inv.validation && inv.validation.checks) || []).map(c =>
    '<div>' + (c.passed ? '✅ ' : '❌ ') + '<strong>' + escapeHtml(c.check_name) + ':</strong> ' + escapeHtml(c.details) + '</div>'
  ).join('');

  let runsRows = (inv.engine_runs || []).map(r =>
    '<tr><td>' + escapeHtml(r.engine_name) + '</td><td>' + escapeHtml(r.model_id) + '</td><td>' + r.latency_ms + ' ms</td><td>' + (r.prompt_tokens||0) + '/' + (r.output_tokens||0) + '</td></tr>'
  ).join('');

  pane.innerHTML =
    '<div class="card">' +
      '<div style="display:flex; justify-content:space-between; align-items:center;">' +
        '<h3>Invoice ' + escapeHtml(inv.id) + ' (' + escapeHtml(inv.filename) + ')</h3>' +
        '<button onclick="submitReview(\'' + inv.id + '\')">Approve / Save to Adaptive Memory</button>' +
      '</div>' +
      '<div style="font-size:12px; color:var(--muted);">' +
        'Total Latency: ' + inv.total_latency_ms + ' ms • Agreement: ' + Math.round((inv.agreement_rate||0)*100) + '% • Adaptive Few-Shots Injected: ' + (inv.adaptive_shots_used||0) +
      '</div>' +
    '</div>' +
    '<div class="card"><h3>Fraud Audit Flags</h3>' + flagsHtml + '</div>' +
    '<div class="card">' +
      '<h3>Dual-Engine Field Consensus (Click row to highlight BBox)</h3>' +
      '<table><thead><tr><th>Field</th><th>Status</th><th>Engine A (PaddleOCR+Text)</th><th>Engine B (Qwen3-VL Vision)</th><th>Reconciled / Reviewer Edit</th><th>Ground / Conf</th></tr></thead>' +
      '<tbody>' + fieldsRows + '</tbody></table>' +
    '</div>' +
    '<div class="card"><h3>Arithmetic & Grounding Validation</h3>' + (checksRows || 'No line arithmetic checks required.') + '</div>' +
    '<div class="card"><h3>Engine Audit Trail</h3><table><thead><tr><th>Stage</th><th>Model</th><th>Latency</th><th>Tokens (In/Out)</th></tr></thead><tbody>' + runsRows + '</tbody></table></div>';
}

async function submitReview(invId) {
  if (!currentInvoice) return;
  const g = name => {
    const el = document.getElementById('edit_' + name);
    return el ? el.value : '';
  };
  const corrected = {
    vendor_name: g('vendor_name'),
    vendor_tax_id: g('vendor_tax_id'),
    invoice_number: g('invoice_number'),
    invoice_date: g('invoice_date'),
    due_date: g('due_date'),
    currency: g('currency'),
    subtotal: parseFloat(g('subtotal') || '0'),
    tax_total: parseFloat(g('tax_total') || '0'),
    total: parseFloat(g('total') || '0'),
    bank_details: g('bank_details'),
    line_items: currentInvoice.reconciled.line_items || []
  };
  const res = await fetch('/api/invoices/' + encodeURIComponent(invId) + '/review', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({ reviewer: 'human_auditor', corrected, notes: 'Approved via Web UI' })
  });
  currentInvoice = await res.json();
  renderDetail(currentInvoice);
  loadInvoices();
}

async function showBatchModal() {
  const [benRes, memRes] = await Promise.all([
    fetch('/api/fraud/benford').then(r => r.json()),
    fetch('/api/memory').then(r => r.json())
  ]);
  const pane = document.getElementById('detailPane');
  const bars = (benRes.digits || []).map(d =>
    '<div class="bar-row">' +
      '<span style="width:55px;">Digit ' + d.digit + '</span>' +
      '<div class="bar-track"><div class="bar-obs" style="width:' + Math.min(100, d.observed_freq*200) + '%"></div></div>' +
      '<span style="width:160px;">Obs: ' + (d.observed_freq*100).toFixed(1) + '% vs Exp: ' + (d.expected_freq*100).toFixed(1) + '%</span>' +
    '</div>'
  ).join('');

  const memRows = (memRes.examples || []).map(ex =>
    '<tr><td>' + ex.id + '</td><td>' + escapeHtml(ex.vendor_norm) + '</td><td>' + escapeHtml(ex.header_snippet) + '</td><td>' + escapeHtml(ex.created_at) + '</td></tr>'
  ).join('');

  pane.innerHTML =
    '<div class="card">' +
      '<h3>Population-Level Benford\'s Law Audit</h3>' +
      '<p style="font-size:12px; color:var(--muted);">Sample Size: <strong>' + benRes.sample_size + '</strong> (Min required: ' + benRes.min_required + ') • Chi-Square: <strong>' + benRes.chi_square + '</strong> (Crit p=0.05: ' + benRes.critical_value_p05 + ') • MAD: <strong>' + benRes.mad + '</strong> • Conformity: <span class="badge badge-info">' + benRes.conformity + '</span></p>' +
      bars +
    '</div>' +
    '<div class="card">' +
      '<h3>Adaptive Memory Index (Few-Shot Learned Corrections)</h3>' +
      '<table><thead><tr><th>ID</th><th>Normalized Vendor</th><th>Header / Layout Signature Context</th><th>Learned At</th></tr></thead><tbody>' +
      (memRows || '<tr><td colspan="4">No reviewer corrections stored yet.</td></tr>') +
      '</tbody></table>' +
    '</div>';
}

function escapeHtml(s) {
  return String(s || '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

loadInvoices();
</script>
</body>
</html>
`
