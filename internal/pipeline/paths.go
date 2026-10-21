package pipeline

import "path/filepath"

// joinRoot construye la ruta absoluta solo para la verificación posterior de
// estabilidad (Lstat, sin seguir enlaces). rel ya fue validada por catalog.
func joinRoot(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
