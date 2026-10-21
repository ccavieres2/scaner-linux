"""Genera fixtures binarios sintéticos (datos 100 % ficticios).

Uso: python -I tests/fixtures/synthetic/make_fixtures.py tests/fixtures/synthetic/tree
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


if __name__ == "__main__":
    main(sys.argv[1])
