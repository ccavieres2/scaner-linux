"""Worker de extracción ARSOP (contrato v1).

Recibe el contenido del archivo por stdin (nunca una ruta) y emite JSON-lines
por stdout:
  {"t":"unit","unit":"page","page":1,"text":"..."}
  {"t":"unit","unit":"page","page":2,"text":"...","method":"ocr"}
  {"t":"unit","unit":"cell","sheet":"Hoja1","row":82,"column":"D","text":"..."}
  {"t":"warn","code":"page_needs_ocr","page":3}
  {"t":"error","code":"encrypted"}
  {"t":"done","units":N}

Reglas (spec §5, §9.2): no ejecuta macros ni fórmulas, no accede a red, no
escribe en disco, no registra texto. stderr se descarta por el proceso padre.

OCR (Fase 2): Tesseract local, solo donde hace falta (imágenes, páginas sin
texto digital o mayormente imagen). La imagen se entrega a tesseract por stdin
y el texto vuelve por stdout: nada se escribe en disco. Sin --ocr-langs o sin
tesseract instalado se reporta *_needs_ocr (cobertura parcial), como en Fase 1.
"""

from __future__ import annotations

import argparse
import errno
import io
import json
import math
import os
import shutil
import subprocess
import sys
import tempfile
import warnings
import zipfile

# stdout se reserva para el protocolo: cualquier print de librerías va a stderr
# (que el proceso padre descarta).
PROTO = sys.stdout
sys.stdout = sys.stderr

MIN_TEXT_CHARS_PER_PAGE = 20
MAX_XLSX_UNCOMPRESSED = 1 << 30  # 1 GiB
MAX_XLSX_RATIO = 200
MAX_ZIP_ENTRIES = 10000
MAX_IMAGE_PIXELS = 100_000_000   # anti "bomba de píxeles": más grande = image_too_large
MAX_OCR_PIXELS = 40_000_000      # se reduce antes de OCR (memoria acotada)
MIN_OCR_SIDE = 1500              # imágenes chicas se amplían x2 (mejor precisión)
IMAGE_COVERAGE_FOR_OCR = 0.2     # página con texto pero >= 20 % imagen (anexo escaneado): también OCR


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


class OCR:
    """Tesseract como subproceso: PNG por stdin, texto por stdout."""

    def __init__(self, langs: str, timeout: int, dpi: int):
        self.langs, self.timeout, self.dpi = langs, timeout, dpi
        self.bin = shutil.which("tesseract") if langs else None

    @property
    def available(self) -> bool:
        return self.bin is not None

    def run(self, img):
        """Devuelve el texto reconocido, o None si tesseract falló o excedió el tiempo."""
        from PIL import Image

        img = img.convert("L")
        w, h = img.size
        if w * h > MAX_OCR_PIXELS:
            f = math.sqrt(MAX_OCR_PIXELS / (w * h))
            img = img.resize((max(1, int(w * f)), max(1, int(h * f))), Image.LANCZOS)
        elif max(w, h) < MIN_OCR_SIDE:
            img = img.resize((w * 2, h * 2), Image.LANCZOS)
        buf = io.BytesIO()
        img.save(buf, "PNG")
        env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "OMP_THREAD_LIMIT": "1"}
        try:
            p = subprocess.run([self.bin, "stdin", "stdout", "-l", self.langs, "--psm", "3"],
                               input=buf.getvalue(), stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                               timeout=self.timeout, env=env)
        except (subprocess.TimeoutExpired, OSError):
            return None
        if p.returncode != 0:
            return None
        return p.stdout.decode("utf-8", "replace")


def ocr_unit(ocr: OCR, img, page: int) -> None:
    text = ocr.run(img)
    if text is None:
        out({"t": "warn", "code": "ocr_failed", "page": page})
    elif text.strip():
        out({"t": "unit", "unit": "page", "page": page, "text": text, "method": "ocr"})


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


def image_coverage(page) -> float:
    """Fracción aproximada de la página cubierta por imágenes (0..1)."""
    try:
        import pypdfium2.raw as raw

        w, h = page.get_size()
        covered = 0.0
        for obj in page.get_objects(filter=[raw.FPDF_PAGEOBJ_IMAGE], max_depth=3):
            left, bottom, right, top = obj.get_bounds()
            covered += max(0.0, right - left) * max(0.0, top - bottom)
        return min(1.0, covered / (w * h)) if w * h > 0 else 0.0
    except Exception:
        return 0.0


def native_dpi(page) -> float:
    """Resolución de la imagen más nítida de la página (0 si no hay imágenes).

    Renderizar un escaneo por encima de su resolución original agranda la letra
    sin agregar detalle y empeora el OCR (p. ej. 150 dpi renderizado a 300).
    """
    try:
        import pypdfium2.raw as raw

        best = 0.0
        for obj in page.get_objects(filter=[raw.FPDF_PAGEOBJ_IMAGE], max_depth=3):
            left, _, right, _ = obj.get_bounds()
            px_w, _ = obj.get_px_size()
            if right - left > 0:
                best = max(best, px_w / ((right - left) / 72))
        return best
    except Exception:
        return 0.0


def render_page(page, dpi: int):
    w, h = page.get_size()
    nd = native_dpi(page)
    if nd > 0:
        dpi = min(dpi, max(round(nd), 100))
    scale = dpi / 72
    if w * h * scale * scale > MAX_OCR_PIXELS:
        scale = math.sqrt(MAX_OCR_PIXELS / (w * h))
    return page.render(scale=scale, grayscale=True).to_pil()


def extract_pdf(fh, max_pages: int, ocr: OCR) -> int:
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
            img = None
            try:
                page = pdf[i]
                tp = page.get_textpage()
                text = tp.get_text_range() or ""
                tp.close()
                # Sin texto digital suficiente, o página mixta (texto + escaneo): OCR.
                needs = len(text.strip()) < MIN_TEXT_CHARS_PER_PAGE
                if ocr.available and (needs or image_coverage(page) >= IMAGE_COVERAGE_FOR_OCR):
                    img = render_page(page, ocr.dpi)
                page.close()
            except pdfium.PdfiumError:
                # Página dañada: se reporta y se continúa con las demás.
                out({"t": "warn", "code": "page_unreadable", "page": i + 1})
                continue
            if text.strip():
                out({"t": "unit", "unit": "page", "page": i + 1, "text": text})
            if img is not None:
                ocr_unit(ocr, img, i + 1)
                img.close()
            elif needs:
                out({"t": "warn", "code": "page_needs_ocr", "page": i + 1})
            units += 1
    finally:
        pdf.close()
    return units


def extract_image(fh, max_pages: int, ocr: OCR) -> int:
    if not ocr.available:
        out({"t": "warn", "code": "image_needs_ocr"})
        return 0
    from PIL import Image, ImageOps

    Image.MAX_IMAGE_PIXELS = MAX_IMAGE_PIXELS
    warnings.simplefilter("error", Image.DecompressionBombWarning)
    try:
        im = Image.open(fh)
    except (Image.DecompressionBombError, Image.DecompressionBombWarning):
        out({"t": "error", "code": "image_too_large"})
        return -1
    except Exception:
        out({"t": "error", "code": "corrupt"})
        return -1
    units = 0
    with im:
        n = getattr(im, "n_frames", 1)  # TIFF multipágina
        if n > max_pages:
            out({"t": "warn", "code": "truncated_by_limit"})
            n = max_pages
        for i in range(n):
            try:
                im.seek(i)
                frame = ImageOps.exif_transpose(im)  # fotos de celular giradas
                frame.load()
            except (Image.DecompressionBombError, Image.DecompressionBombWarning):
                out({"t": "error", "code": "image_too_large"})
                return -1
            except Exception:
                out({"t": "warn", "code": "page_unreadable", "page": i + 1})
                continue
            ocr_unit(ocr, frame, i + 1)
            units += 1
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
    ap.add_argument("--kind", required=True, choices=["pdf", "xlsx", "image"])
    ap.add_argument("--max-pages", type=int, default=2000)
    ap.add_argument("--max-mem-mb", type=int, default=1024)
    ap.add_argument("--ocr-langs", default="")              # p. ej. "spa+eng"; vacío = sin OCR
    ap.add_argument("--ocr-timeout", type=int, default=60)  # segundos por página o imagen
    ap.add_argument("--ocr-dpi", type=int, default=300)
    a = ap.parse_args()
    limit_memory(a.max_mem_mb)
    ocr = OCR(a.ocr_langs, a.ocr_timeout, a.ocr_dpi)
    try:
        with tempfile.TemporaryFile(prefix="arsop-") as tmp:
            size = spool_stdin(tmp)
            if a.kind == "pdf":
                units = extract_pdf(tmp, a.max_pages, ocr)
            elif a.kind == "image":
                units = extract_image(tmp, a.max_pages, ocr)
            else:
                units = extract_xlsx(tmp, size)
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
