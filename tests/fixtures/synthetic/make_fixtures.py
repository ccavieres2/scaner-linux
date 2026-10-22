"""Genera fixtures binarios sintéticos (datos 100 % ficticios).

Uso: python -I tests/fixtures/synthetic/make_fixtures.py tests/fixtures/synthetic/tree
     python -I tests/fixtures/synthetic/make_fixtures.py --ocr tests/fixtures/synthetic/ocr
"""

import sys
from pathlib import Path


def pdf_with_pages(pages: list[str]) -> bytes:
    """PDF mínimo escrito a mano: una página por texto ('' = página sin texto)."""
    objs: list[bytes] = []
    n = len(pages)
    font_id = 3 + 2 * n
    kids = " ".join(f"{3 + 2 * i} 0 R" for i in range(n))
    objs.append(b"<< /Type /Catalog /Pages 2 0 R >>")
    objs.append(f"<< /Type /Pages /Kids [{kids}] /Count {n} >>".encode())
    for i, text in enumerate(pages):
        lines = []
        for j, line in enumerate(text.split("\n")):
            esc = line.replace("\\", "\\\\").replace("(", "\\(").replace(")", "\\)")
            lines.append(f"BT /F1 11 Tf 50 {780 - 16 * j} Td ({esc}) Tj ET")
        stream = "\n".join(lines).encode("latin-1")
        objs.append(f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "
                    f"/Resources << /Font << /F1 {font_id} 0 R >> >> /Contents {4 + 2 * i} 0 R >>".encode())
        objs.append(b"<< /Length " + str(len(stream)).encode() + b" >>\nstream\n" + stream + b"\nendstream")
    objs.append(b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
    out = bytearray(b"%PDF-1.4\n")
    offsets = []
    for k, o in enumerate(objs, start=1):
        offsets.append(len(out))
        out += f"{k} 0 obj\n".encode() + o + b"\nendobj\n"
    xref = len(out)
    out += f"xref\n0 {len(objs) + 1}\n0000000000 65535 f \n".encode()
    for off in offsets:
        out += f"{off:010d} 00000 n \n".encode()
    out += f"trailer\n<< /Size {len(objs) + 1} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode()
    return bytes(out)


def main(dest: str) -> None:
    d = Path(dest)
    d.mkdir(parents=True, exist_ok=True)
    (d / "contrato.pdf").write_bytes(pdf_with_pages([
        "CONTRATO FICTICIO DE PRUEBA - datos inventados\nComparece Persona Ficticia Siete",
        "",  # página escaneada simulada: sin texto, requiere OCR
        "Pagina 3\nRUT del firmante: 5.126.663-3\nCorreo: siete.ficticia@example.org",
    ]))

    import openpyxl
    wb = openpyxl.Workbook()
    ws = wb.active
    ws.title = "Clientes"
    ws.append(["Nombre", "RUT", "Correo"])
    ws.append(["Persona Ficticia Ocho", "20.000.005-6", "ocho@example.cl"])
    ws.append(["Persona Ficticia Uno", "12.345.678-5", "uno.ficticio@example.cl"])
    hidden = wb.create_sheet("Oculta")
    hidden.sheet_state = "hidden"
    hidden["B3"] = "Respaldo: 10000013-K"
    wb.save(d / "planilla.xlsx")


def scanned_page(lines: list[str], size=(1240, 1754), rotate: float = 0.0):
    """Imagen tipo "hoja escaneada" (A4 a 150 dpi) con texto ficticio."""
    from PIL import Image, ImageDraw, ImageFont

    img = Image.new("L", size, 255)
    draw = ImageDraw.Draw(img)
    font = ImageFont.load_default(size=40)
    y = 150
    for line in lines:
        draw.text((120, y), line, fill=0, font=font)
        y += 70
    return img.rotate(rotate, fillcolor=255, expand=False) if rotate else img


def main_ocr(dest: str) -> None:
    """Fixtures para OCR (Fase 2): todo el texto está en imágenes, sin texto digital."""
    d = Path(dest)
    d.mkdir(parents=True, exist_ok=True)
    scanned_page([
        "CERTIFICADO FICTICIO DE PRUEBA",
        "Nombre: Persona Ficticia Nueve",
        "RUT: 7.654.321-6",
        "Correo: nueve.ficticia@example.cl",
    ]).save(d / "escaneado.png")
    # PDF escaneado: la página es solo una imagen (como sale de un escáner).
    p1 = scanned_page(["DECLARACION FICTICIA", "Firmante: Persona Ficticia Diez"])
    p2 = scanned_page(["Pagina 2", "RUT: 15.555.555-6", "Correo: diez.ficticio@example.org"])
    p1.save(d / "escaneado.pdf", save_all=True, append_images=[p2], resolution=150)
    # Foto JPEG levemente girada (como una foto de celular).
    scanned_page(["Boleta ficticia", "RUT cliente: 7.654.321-6"], rotate=1.5).convert("RGB").save(
        d / "foto.jpg", quality=85)
    mixed_pdf(d / "mixto.pdf")


def mixed_pdf(path: Path) -> None:
    """PDF mixto: texto digital suficiente + un escaneo pegado con un RUT que solo está en la imagen."""
    import ctypes
    import io

    import pypdfium2 as pdfium
    import pypdfium2.raw as raw

    pdf = pdfium.PdfDocument.new()
    page = pdf.new_page(595, 842)
    font = raw.FPDFText_LoadStandardFont(pdf.raw, b"Helvetica")
    for i, line in enumerate(["Documento mixto ficticio: este texto es digital y seleccionable.",
                              "El anexo de abajo es un escaneo pegado como imagen."]):
        obj = raw.FPDFPageObj_CreateTextObj(pdf.raw, font, 12.0)
        buf = ctypes.create_string_buffer((line + "\0").encode("utf-16-le"))
        raw.FPDFText_SetText(obj, ctypes.cast(buf, ctypes.POINTER(ctypes.c_ushort)))
        raw.FPDFPageObj_Transform(obj, 1, 0, 0, 1, 50, 790 - 20 * i)
        raw.FPDFPage_InsertObject(page.raw, obj)
    jpg = io.BytesIO()
    scanned_page(["ANEXO ESCANEADO", "RUT: 9.876.543-3"], size=(1240, 700)).convert("RGB").save(jpg, "JPEG", quality=90)
    jpg.seek(0)
    img = pdfium.PdfImage.new(pdf)
    img.load_jpeg(jpg, pages=[page], inline=True)
    img.set_matrix(pdfium.PdfMatrix().scale(495, 280).translate(50, 400))
    page.insert_obj(img)
    page.gen_content()
    pdf.save(str(path))


if __name__ == "__main__":
    if sys.argv[1] == "--ocr":
        main_ocr(sys.argv[2])
    else:
        main(sys.argv[1])
