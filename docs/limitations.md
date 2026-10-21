# Limitaciones conocidas

- El producto **no** certifica cumplimiento de la Ley 21.719 ni garantiza detectar todos los datos personales.
- Fase 1 no hace OCR: imágenes y páginas PDF escaneadas quedan como cobertura parcial explícita.
- DOCX, XLS/DOC antiguos y ZIP se omiten con código explícito (`unsupported_*`).
- La autenticación de la API es solo de desarrollo (tokens estáticos hasheados).
- No encontrar un identificador no implica que no exista (cobertura parcial, OCR fallido, archivos omitidos).
- Una coincidencia (incluso con DV válido) no prueba identidad del titular.
- Archivos cifrados con contraseña se registrarán como inaccesibles; nunca se intentará romperlos.
- El plazo de 14 días es un objetivo a validar con benchmark, no una garantía.
