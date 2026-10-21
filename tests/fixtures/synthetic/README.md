# Fixtures sintéticos

**Solo datos ficticios.** Prohibido incorporar datos reales de personas (spec §29).
RUT generados con DV módulo 11 calculado; dominios reservados (`example.*`, `.test`).

- `tree/` — árbol mínimo para la prueba end-to-end de Fase 1.
- `expected.json` — resultados esperados (oráculo de pruebas).
- Fixtures hostiles (zip bomb, PDF malformado, imagen enorme) se generan en
  `tests/fixtures/hostile/` por script en Fase 2; no se comitean binarios peligrosos.
