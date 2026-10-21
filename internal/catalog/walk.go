// Package catalog inventaría raíces autorizadas en modo solo lectura y abre
// archivos sin salir de la raíz (spec §9.2, ADR-0005).
package catalog

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Motivos de omisión explícitos (cobertura, spec §10).
const (
	SkipSymlink    = "symlink"
	SkipSpecial    = "special_file"
	SkipOtherMount = "other_mount"
	SkipTooLarge   = "too_large"
	SkipNoAccess   = "permission_denied"
)

// Entry es un archivo observado. RelPath usa '/' y es relativo a la raíz.
type Entry struct {
	RelPath    string
	Size       int64
	ModTime    time.Time
	Dev, Inode uint64
	SkipReason string
}

// ErrRootUnavailable indica que la raíz no existe o no es accesible: nunca
// debe interpretarse como "todos los archivos fueron borrados" (spec §10).
var ErrRootUnavailable = errors.New("raíz de fuente no disponible")

// Walk recorre root sin seguir enlaces simbólicos ni cruzar a otros montajes.
// Los directorios ilegibles se reportan como entradas omitidas y el recorrido
// continúa. fn puede devolver error para abortar.
func Walk(root string, maxBytes int64, fn func(Entry) error) error {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() {
		return ErrRootUnavailable
	}
	rootDev, _, _ := devIno(rootInfo)
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if werr != nil {
			if p == root {
				return ErrRootUnavailable
			}
			return fn(Entry{RelPath: rel, SkipReason: SkipNoAccess})
		}
		if p == root {
			return nil
		}
		typ := d.Type()
		if typ&fs.ModeSymlink != 0 {
			return fn(Entry{RelPath: rel, SkipReason: SkipSymlink})
		}
		info, ierr := d.Info()
		if ierr != nil {
			return fn(Entry{RelPath: rel, SkipReason: SkipNoAccess})
		}
		dev, ino, ok := devIno(info)
		if d.IsDir() {
			if ok && dev != rootDev {
				if err := fn(Entry{RelPath: rel, SkipReason: SkipOtherMount}); err != nil {
					return err
				}
				return filepath.SkipDir
			}
			return nil
		}
		if !typ.IsRegular() {
			return fn(Entry{RelPath: rel, SkipReason: SkipSpecial})
		}
		e := Entry{RelPath: rel, Size: info.Size(), ModTime: info.ModTime().UTC(), Dev: dev, Inode: ino}
		if ok && dev != rootDev {
			e.SkipReason = SkipOtherMount
		} else if info.Size() > maxBytes {
			e.SkipReason = SkipTooLarge
		}
		return fn(e)
	})
}

// ValidRelPath rechaza rutas absolutas, con '..' o no canónicas.
func ValidRelPath(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.ContainsAny(rel, "\x00\\") {
		return false
	}
	if path.Clean(rel) != rel {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." || seg == "." {
			return false
		}
	}
	return true
}

// ErrOutsideRoot se devuelve si la ruta escaparía de la raíz.
var ErrOutsideRoot = errors.New("ruta fuera de la raíz autorizada o enlace simbólico")
