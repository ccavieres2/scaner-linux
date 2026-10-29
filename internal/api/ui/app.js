// Interfaz web del escáner ARSOP. Solo consume la API del mismo origen con el
// token del usuario; todo texto se inserta con textContent (nunca innerHTML).
"use strict";

const KEY = "arsop_token";
let token = null;
let scopes = [];

const $ = (id) => document.getElementById(id);

function el(tag, attrs, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === "class") n.className = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c !== null && c !== undefined) n.append(c instanceof Node ? c : String(c));
  }
  return n;
}

function store(get, value) {
  try {
    if (get) return sessionStorage.getItem(KEY);
    if (value) sessionStorage.setItem(KEY, value); else sessionStorage.removeItem(KEY);
  } catch (_) { /* almacenamiento bloqueado: el token vive solo en memoria */ }
  return null;
}

class APIError extends Error {
  constructor(status, code) { super(code); this.status = status; this.code = code; }
}

async function api(path, opts = {}) {
  const res = await fetch(path, {
    ...opts,
    headers: { "Authorization": "Bearer " + token, "Content-Type": "application/json", ...(opts.headers || {}) },
    cache: "no-store",
    credentials: "omit",
  });
  let body = null;
  try { body = await res.json(); } catch (_) { /* respuesta sin JSON */ }
  if (res.status === 401) { logout("La sesión no es válida o el token venció."); throw new APIError(401, "unauthorized"); }
  if (!res.ok) throw new APIError(res.status, (body && body.error) || "error");
  return body;
}

const MESSAGES = {
  forbidden: "Su token no tiene permiso para esta consulta.",
  rate_limited: "Demasiadas búsquedas seguidas. Espere un minuto.",
  invalid_identifier: "El valor no es un RUT o correo válido (revise el dígito verificador).",
  case_reference_required: "Indique un motivo o caso: letras, números, punto o guion, sin espacios.",
  not_found: "No encontrado.",
};
const message = (e) => MESSAGES[e.code] || "Error al consultar la API (" + (e.code || e.message) + ").";

const fmtDate = (s) => s ? new Date(s).toLocaleString("es-CL") : "—";
function fmtSize(n) {
  if (n == null) return "—";
  const u = ["B", "KB", "MB", "GB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(1) : n) + " " + u[i];
}

const STATUS = {
  completed: ["completo", "ok"], partial: ["parcial", "warn"], failed: ["falló", "bad"], running: ["en curso", ""],
  paused: ["pausado", "warn"], unavailable: ["no disponible", "bad"], skipped: ["omitido", "warn"],
  queued: ["en cola", ""], discovered: ["descubierto", ""], stale: ["cambió durante la lectura", "warn"],
  never_scanned: ["nunca escaneado", "warn"], in_progress: ["escaneo en curso", ""],
  potential_match: ["coincidencia", "ok"], requires_review: ["requiere revisión", "warn"],
  confirmed_by_authorized_reviewer: ["confirmado", "ok"],
};
function chip(s) {
  const [label, cls] = STATUS[s] || [s, ""];
  return el("span", { class: "chip " + cls }, label);
}

function table(headers, rows) {
  return el("div", { class: "table-wrap" }, el("table", {},
    el("thead", {}, el("tr", {}, headers.map((h) => el("th", { class: h.num ? "num" : "" }, h.label || h)))),
    el("tbody", {}, rows)));
}

function show(node, ...children) { node.replaceChildren(...children); }

// Ruta completa en el servidor: nombre del archivo destacado, carpeta debajo y botón para copiarla.
function pathCell(full, source) {
  const i = full.lastIndexOf("/");
  const copy = el("button", { class: "link small", title: "Copiar la ruta completa" }, "copiar ruta");
  copy.addEventListener("click", async () => {
    try { await navigator.clipboard.writeText(full); copy.textContent = "copiada"; }
    catch (_) { copy.textContent = "seleccione y copie"; }
    setTimeout(() => { copy.textContent = "copiar ruta"; }, 1500);
  });
  return el("div", {},
    el("div", {}, el("b", {}, full.slice(i + 1))),
    el("div", { class: "muted small" }, (source ? "[" + source + "]  " : "") + full.slice(0, i + 1)),
    copy);
}

// ---- sesión ----

async function login(t) {
  token = t;
  try {
    const me = await api("/api/v1/me");
    scopes = me.scopes;
    store(false, t);
    $("who").textContent = me.principal;
    $("session").hidden = false;
    $("login").hidden = true;
    $("app").hidden = false;
    for (const b of document.querySelectorAll(".tab")) {
      const need = { buscar: "findings:search", resumen: "scans:read", archivos: "files:read", escaneos: "scans:read" }[b.dataset.tab];
      b.hidden = !scopes.includes(need);
    }
    const first = [...document.querySelectorAll(".tab")].find((b) => !b.hidden);
    if (first) selectTab(first.dataset.tab);
    return true;
  } catch (e) {
    token = null;
    store(false, null);
    if (e.status !== 401) throw e;
    return false;
  }
}

function logout(msg) {
  token = null;
  scopes = [];
  store(false, null);
  $("app").hidden = true;
  $("session").hidden = true;
  $("login").hidden = false;
  $("token").value = "";
  const err = $("login-error");
  err.hidden = !msg;
  err.textContent = msg || "";
}

$("login-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const err = $("login-error");
  err.hidden = true;
  try {
    if (!(await login($("token").value.trim()))) {
      err.textContent = "Token incorrecto.";
      err.hidden = false;
    }
  } catch (e) {
    err.textContent = message(e);
    err.hidden = false;
  }
  $("token").value = "";
});
$("logout").addEventListener("click", () => logout());

// ---- pestañas ----

const loaders = { resumen: loadOverview, archivos: loadSources, escaneos: loadScans };

function selectTab(name) {
  for (const b of document.querySelectorAll(".tab")) b.classList.toggle("active", b.dataset.tab === name);
  for (const s of document.querySelectorAll("[id^=tab-]")) s.hidden = s.id !== "tab-" + name;
  if (loaders[name]) loaders[name]();
}
for (const b of document.querySelectorAll(".tab")) b.addEventListener("click", () => selectTab(b.dataset.tab));

// ---- buscar ----

let lastSearch = null;

$("search-form").addEventListener("submit", (ev) => {
  ev.preventDefault();
  lastSearch = { type: $("s-type").value, value: $("s-value").value.trim(), case_reference: $("s-case").value.trim() };
  runSearch(null, false);
});

function fmtLocation(l) {
  const parts = [];
  if (l.page) parts.push("pág. " + l.page);
  if (l.sheet) parts.push("hoja " + l.sheet);
  if (l.row) parts.push("fila " + l.row);
  if (l.column) parts.push("col. " + l.column);
  if (l.line) parts.push("línea " + l.line);
  return parts.join(", ");
}

async function runSearch(cursor, append) {
  const out = $("search-out");
  if (!append) show(out, el("p", { class: "muted" }, "Buscando…"));
  try {
    const r = await api("/api/v1/identifiers/search", {
      method: "POST", body: JSON.stringify({ ...lastSearch, page_size: 50, ...(cursor ? { cursor } : {}) }),
    });
    const rows = r.results.map((x) => el("tr", {},
      el("td", { class: "path" }, x.full_path ? pathCell(x.full_path, x.source) : el("span", { class: "muted" }, "[ruta oculta] " + x.file_id)),
      el("td", {}, x.locations.map(fmtLocation).filter(Boolean).join(" · ") || "—"),
      el("td", { class: "num" }, x.occurrences),
      el("td", {}, x.method === "ocr" ? "OCR" : "texto"),
      el("td", {}, chip(x.review_status)),
      el("td", {}, fmtDate(x.processed_at))));
    const t = table(["Archivo y carpeta", "Ubicación", { label: "Veces", num: true }, "Método", "Estado", "Leído"], rows);
    const header = el("div", {},
      el("p", {}, el("b", {}, r.result_count), r.result_count === 1 ? " archivo contiene este valor." : " archivos contienen este valor."),
      el("div", { class: "notice" }, "Cobertura: ", chip(r.coverage.scan_status),
        r.coverage.files_not_covered ? " · " + r.coverage.files_not_covered + " archivos sin cubrir por completo" : "",
        el("div", { class: "small muted" }, r.notice)));
    const more = r.next_cursor
      ? el("div", { class: "pager" }, el("button", { class: "ghost", onclick: (e) => { e.target.remove(); runSearch(r.next_cursor, true); } }, "Ver más"))
      : null;
    if (append) {
      out.querySelector("tbody").append(...rows);
      if (more) out.append(more);
    } else {
      show(out, header, r.results.length ? t : el("p", { class: "muted" }, "Sin coincidencias."), more || "");
    }
  } catch (e) {
    show(out, el("p", { class: "error" }, message(e)));
  }
}

// ---- resumen ----

async function loadOverview() {
  const out = $("overview-out");
  show(out, el("p", { class: "muted" }, "Cargando…"));
  try {
    const r = await api("/api/v1/overview");
    const c = r.coverage;
    const tot = r.sources.reduce((a, s) => ({ files: a.files + s.files, with: a.with + s.files_with_findings }), { files: 0, with: 0 });
    const stats = el("div", { class: "stats" },
      el("div", { class: "stat" }, el("b", {}, tot.files), "archivos indexados"),
      el("div", { class: "stat" }, el("b", {}, tot.with), "archivos con RUT o correos"),
      el("div", { class: "stat" }, el("b", {}, c.sources_scanned + " / " + c.sources_total), "fuentes escaneadas"),
      el("div", { class: "stat" }, el("b", {}, c.files_not_covered), "archivos sin cubrir por completo"));
    const rows = r.sources.map((s) => el("tr", {},
      el("td", {}, s.source, s.enabled ? "" : el("span", { class: "muted small" }, " (deshabilitada)")),
      el("td", { class: "num" }, s.files),
      el("td", { class: "num" }, s.files_with_findings),
      el("td", { class: "num" }, s.files_partial),
      el("td", { class: "num" }, s.files_failed),
      el("td", { class: "num" }, s.files_skipped),
      el("td", {}, fmtDate(s.last_completed_at))));
    show(out,
      el("div", { class: "notice" }, "Estado general: ", chip(c.scan_status)),
      stats,
      el("h3", {}, "Fuentes"),
      table(["Fuente", { label: "Archivos", num: true }, { label: "Con hallazgos", num: true }, { label: "Parciales", num: true },
        { label: "Fallidos", num: true }, { label: "Omitidos", num: true }, "Último escaneo"], rows));
  } catch (e) {
    show(out, el("p", { class: "error" }, message(e)));
  }
}

// ---- archivos ----

let filesPage = 0;

async function loadSources() {
  const sel = $("f-source");
  if (sel.options.length) return loadFiles(0);
  if (!scopes.includes("scans:read")) {
    show($("files-out"), el("p", { class: "muted" }, "Su token no puede listar fuentes."));
    return;
  }
  try {
    const r = await api("/api/v1/overview");
    sel.replaceChildren(...r.sources.map((s) => el("option", { value: s.source }, s.source)));
    loadFiles(0);
  } catch (e) {
    show($("files-out"), el("p", { class: "error" }, message(e)));
  }
}
$("f-source").addEventListener("change", () => loadFiles(0));
$("f-only").addEventListener("change", () => loadFiles(0));

// Motivos de omisión o falla (códigos del escáner) en palabras simples.
const REASONS = {
  encrypted: "protegido con contraseña o cifrado",
  corrupt: "archivo dañado o incompleto",
  timeout: "tardó demasiado en leerse",
  out_of_memory: "demasiado grande para la memoria asignada",
  image_too_large: "imagen demasiado grande (más de 100 megapíxeles)",
  zip_bomb_suspected: "contenido comprimido sospechoso",
  temp_space_exhausted: "sin espacio temporal en el servidor",
  extractor_failed: "error interno del lector",
  ocr_failed: "error al leer el texto de la imagen (OCR)",
  unsupported_format: "formato no soportado",
  too_large: "supera el tamaño máximo configurado",
  permission_denied: "sin permiso de lectura",
  symlink: "acceso directo (enlace simbólico); no se sigue",
  special_file: "no es un archivo normal",
  other_mount: "está en otro disco o montaje",
  outside_root: "fuera de la carpeta autorizada",
  open_failed: "no se pudo abrir",
  stat_failed: "no se pudo consultar el archivo",
  seek_failed: "no se pudo leer",
  root_unavailable: "la carpeta no estaba disponible",
  changed_during_read: "cambió mientras se leía",
  changed_since_inventory: "cambió durante el escaneo",
  vanished: "se borró durante el escaneo",
  seal_failed: "error al cifrar los resultados",
};
function reasonText(code) {
  if (!code) return "";
  if (REASONS[code]) return REASONS[code];
  if (code.startsWith("unsupported_")) return "formato no soportado (" + code.slice(12) + ")";
  return code;
}

function fileState(f) {
  if (f.deferred) {
    return el("div", {}, el("span", { class: "chip warn" }, "pendiente por tamaño"),
      el("div", { class: "muted small" }, "pesa 1 MB o más; se lee con scan process-pending"));
  }
  const why = reasonText(f.reason);
  if (f.state === "completed" && !f.ruts && !f.emails) {
    return el("div", {}, chip(f.state), el("div", { class: "muted small" }, "leído entero, sin RUT ni correos"));
  }
  return el("div", {}, chip(f.state), why ? el("div", { class: "small" }, why) : "");
}

async function loadFiles(page) {
  const out = $("files-out");
  const src = $("f-source").value;
  if (!src) { show(out, el("p", { class: "muted" }, "No hay fuentes registradas.")); return; }
  filesPage = page;
  show(out, el("p", { class: "muted" }, "Cargando…"));
  try {
    const q = new URLSearchParams({ page: String(page), with_findings: String($("f-only").checked) });
    const r = await api("/api/v1/sources/" + encodeURIComponent(src) + "/files?" + q);
    const rows = r.files.map((f) => el("tr", {},
      el("td", { class: "path" }, f.full_path ? pathCell(f.full_path) : el("span", { class: "muted" }, "[ruta oculta] " + f.file_id)),
      el("td", {}, f.kind || "—"),
      el("td", { class: "num" }, fmtSize(f.size_bytes)),
      el("td", { class: "num" }, f.ruts),
      el("td", { class: "num" }, f.emails),
      el("td", {}, f.ocr ? "OCR" : ""),
      el("td", {}, fileState(f))));
    const pager = el("div", { class: "pager" },
      el("button", { class: "ghost", onclick: () => loadFiles(filesPage - 1), ...(page === 0 ? { disabled: "" } : {}) }, "Anterior"),
      el("span", { class: "muted small" }, "Página " + (page + 1)),
      el("button", { class: "ghost", onclick: () => loadFiles(filesPage + 1), ...(r.has_more ? {} : { disabled: "" }) }, "Siguiente"));
    show(out,
      el("p", { class: "muted small" }, "Los valores no se muestran aquí: el sistema guarda solo huellas. Para saber dónde aparece un RUT o correo, use Buscar."),
      r.files.length ? table(["Archivo y carpeta", "Tipo", { label: "Tamaño", num: true }, { label: "RUT", num: true },
        { label: "Correos", num: true }, "", "Estado"], rows) : el("p", { class: "muted" }, "Sin archivos que mostrar."),
      pager);
  } catch (e) {
    show(out, el("p", { class: "error" }, message(e)));
  }
}

// ---- escaneos ----

async function loadScans() {
  const out = $("scans-out");
  show($("scan-detail"));
  show(out, el("p", { class: "muted" }, "Cargando…"));
  try {
    const r = await api("/api/v1/scans");
    const rows = r.scans.map((s) => el("tr", {},
      el("td", {}, s.source),
      el("td", {}, s.type === "full" ? "completo" : "incremental"),
      el("td", {}, chip(s.status)),
      el("td", {}, fmtDate(s.started_at)),
      el("td", {}, fmtDate(s.completed_at)),
      el("td", {}, el("button", { class: "link", onclick: () => loadScan(s.scan_id) }, "Detalle"))));
    show(out, r.scans.length
      ? table(["Fuente", "Tipo", "Estado", "Inicio", "Término", ""], rows)
      : el("p", { class: "muted" }, "Aún no hay escaneos."));
  } catch (e) {
    show(out, el("p", { class: "error" }, message(e)));
  }
}

async function loadScan(id) {
  const out = $("scan-detail");
  show(out, el("p", { class: "muted" }, "Cargando…"));
  try {
    const s = await api("/api/v1/scans/" + id);
    const p = s.progress;
    const skipped = Object.entries(p.files_skipped || {}).map(([k, v]) => (k || "otro") + ": " + v).join(", ");
    const parts = [
      el("h3", {}, "Escaneo de " + s.source + " ", chip(s.status)),
      el("p", { class: "muted small" }, "ID " + s.scan_id),
      el("div", { class: "stats" },
        el("div", { class: "stat" }, el("b", {}, p.files_seen), "archivos vistos"),
        el("div", { class: "stat" }, el("b", {}, p.files_partial), "parciales"),
        el("div", { class: "stat" }, el("b", {}, p.files_failed), "fallidos"),
        el("div", { class: "stat" }, el("b", {}, p.deferred_files), "pendientes por tamaño")),
      skipped ? el("p", { class: "small" }, "Omitidos: " + skipped) : "",
    ];
    if (p.deferred_files) {
      const d = await api("/api/v1/scans/" + id + "/pending");
      parts.push(el("h3", {}, "Pendientes por tamaño"),
        el("p", { class: "muted small" }, "Se leen con: ./arsop.sh scan process-pending --scan " + s.scan_id),
        table(["Archivo", { label: "Tamaño", num: true }], d.pending.map((f) => el("tr", {},
          el("td", { class: "path" }, f.relative_path || "[nombre oculto] " + f.file_id),
          el("td", { class: "num" }, fmtSize(f.size_bytes))))));
    }
    if (scopes.includes("scans:diagnose")) {
      const e = await api("/api/v1/scans/" + id + "/errors");
      if (e.errors.length) {
        parts.push(el("h3", {}, "Errores y advertencias"), table(["Etapa", "Código", { label: "Intentos", num: true }, "Última vez"],
          e.errors.map((x) => el("tr", {}, el("td", {}, x.stage), el("td", {}, x.code),
            el("td", { class: "num" }, x.attempts), el("td", {}, fmtDate(x.last_seen_at))))));
      }
    }
    show(out, ...parts);
  } catch (e) {
    show(out, el("p", { class: "error" }, message(e)));
  }
}

// ---- inicio ----

const saved = store(true);
if (saved) login(saved).catch(() => logout());
