"""Worker de extracción ARSOP (contrato v1).

Recibe el contenido del archivo por stdin (nunca una ruta) y emite JSON-lines
por stdout:
  {"t":"unit","unit":"page","page":1,"text":"..."}
  {"t":"unit","unit":"cell","sheet":"Hoja1","row":82,"column":"D","text":"..."}
  {"t":"warn","code":"page_needs_ocr","page":3}
  {"t":"error","code":"encrypted"}
  {"t":"done","units":N}

Reglas (spec §5, §9.2): no ejecuta macros ni fórmulas, no accede a red, no
escribe en disco, no registra texto. stderr se descarta por el proceso padre.
"""

from __future__ import annotations

import argparse
import errno
import json
import sys
import tempfile
import zipfile

# stdout se reserva para el protocolo: cualquier print de librerías va a stderr
# (que el proceso padre descarta).
PROTO = sys.stdout
sys.stdout = sys.stderr

MIN_TEXT_CHARS_PER_PAGE = 20
MAX_XLSX_UNCOMPRESSED = 1 << 30  # 1 GiB
MAX_XLSX_RATIO = 200
MAX_ZIP_ENTRIES = 10000


def out(obj: dict) -> None:
    PROTO.write(json.dumps(obj, ensure_ascii=False, separators=(",", ":")))
    PROTO.write("\n")


def limit_memory(max_mb: int) -> None:
    try:
        import resource  # solo POSIX

        lim = max_mb * 1024 * 1024
        resource.setrlimit(resource.RLIMIT_AS, (lim, lim))
        resource.setrlimit(resource.RLIMIT_CORE, (0, 0))  # sin core dumps (spec §25)
    except (ImportError, ValueError, OSError):
        pass


def column_ref(i: int) -> str:
    s = ""
    while i > 0:
        i, r = divmod(i - 1, 26)
        s = chr(ord("A") + r) + s
    return s


def spool_stdin(tmp) -> int:
    """Copia stdin a un temporal SIN NOMBRE por bloques (memoria constante).

    tempfile.TemporaryFile se desvincula del sistema de archivos al crearse en
    POSIX: el contenido desaparece al cerrar o si el proceso muere (spec §25).
    """
    n = 0
    src = sys.stdin.buffer
    while True:
        chunk = src.read(1 << 20)
        if not chunk:
            break
        tmp.write(chunk)
        n += len(chunk)
    tmp.flush()
    tmp.seek(0)
    return n


def extract_pdf(fh, max_pages: int) -> int:
    import pypdfium2 as pdfium

    try:
        # Lectura bajo demanda desde el archivo: pdfium no carga todo en memoria.
        pdf = pdfium.PdfDocument(fh, autoclose=False)
    except pdfium.PdfiumError as e:
        msg = str(e).lower()
        out({"t": "error", "code": "encrypted" if "password" in msg else "corrupt"})
        return -1
    units = 0
    try:
        n = len(pdf)
        if n > max_pages:
            out({"t": "warn", "code": "truncated_by_limit"})
            n = max_pages
        for i in range(n):
            try:
                page = pdf[i]
                tp = page.get_textpage()
                text = tp.get_text_range() or ""
                tp.close()
                page.close()
            except pdfium.PdfiumError:
                # Página dañada: se reporta y se continúa con las demás.
                out({"t": "warn", "code": "page_unreadable", "page": i + 1})
                continue
            if len(text.strip()) < MIN_TEXT_CHARS_PER_PAGE:
                # Sin texto digital suficiente: requiere OCR (Fase 2).
                out({"t": "warn", "code": "page_needs_ocr", "page": i + 1})
            if text.strip():
                out({"t": "unit", "unit": "page", "page": i + 1, "text": text})
            units += 1
    finally:
        pdf.close()
    return units


def check_zip(fh, size: int) -> bool:
    try:
        with zipfile.ZipFile(fh) as zf:
            infos = zf.infolist()
            if len(infos) > MAX_ZIP_ENTRIES:
                out({"t": "error", "code": "zip_bomb_suspected"})
                return False
            total = sum(i.file_size for i in infos)
    except (zipfile.BadZipFile, OSError):
        out({"t": "error", "code": "corrupt"})
        return False
    finally:
        fh.seek(0)
    if total > MAX_XLSX_UNCOMPRESSED or (size and total / size > MAX_XLSX_RATIO):
        out({"t": "error", "code": "zip_bomb_suspected"})
        return False
    return True


def extract_xlsx(fh, size: int) -> int:
    import openpyxl

    if not check_zip(fh, size):
        return -1
    try:
        # data_only: valores cacheados, nunca se evalúan fórmulas ni macros.
        wb = openpyxl.load_workbook(fh, read_only=True, data_only=True, keep_links=False)
    except MemoryError:
        raise
    except Exception:
        out({"t": "error", "code": "corrupt"})
        return -1
    units = 0
    try:
        for ws in wb.worksheets:
            if ws.sheet_state != "visible":
                out({"t": "warn", "code": "hidden_sheet"})
            for r, row in enumerate(ws.iter_rows(min_row=1, min_col=1, values_only=True), start=1):
                for c, v in enumerate(row, start=1):
                    if v is None or isinstance(v, bool):
                        continue
                    text = v if isinstance(v, str) else str(v)
                    if not text:
                        continue
                    out({"t": "unit", "unit": "cell", "sheet": ws.title, "row": r,
                         "column": column_ref(c), "text": text})
                    units += 1
    finally:
        wb.close()
    return units


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--kind", required=True, choices=["pdf", "xlsx"])
    ap.add_argument("--max-pages", type=int, default=2000)
    ap.add_argument("--max-mem-mb", type=int, default=1024)
    a = ap.parse_args()
    limit_memory(a.max_mem_mb)
    try:
        with tempfile.TemporaryFile(prefix="arsop-") as tmp:
            size = spool_stdin(tmp)
            units = extract_pdf(tmp, a.max_pages) if a.kind == "pdf" else extract_xlsx(tmp, size)
    except MemoryError:
        out({"t": "error", "code": "out_of_memory"})
        return 1
    except OSError as e:
        # Sin espacio en el directorio temporal u otro error de E/S local.
        out({"t": "error", "code": "temp_space_exhausted" if e.errno == errno.ENOSPC else "extractor_failed"})
        return 1
    except Exception:
        out({"t": "error", "code": "extractor_failed"})
        return 1
    if units < 0:
        return 1
    out({"t": "done", "units": units})
    return 0


if __name__ == "__main__":
    sys.exit(main())
