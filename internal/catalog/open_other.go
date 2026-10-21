//go:build !linux

package catalog

import (
	"io/fs"
	"os"
	"path/filepath"
)

// En plataformas de desarrollo no Linux no hay identidad de dispositivo
// portable; la verificación de montaje solo se aplica en Linux (producción).
func devIno(fs.FileInfo) (dev, ino uint64, ok bool) { return 0, 0, false }

// OpenBeneath (no Linux, solo desarrollo): verifica con Lstat que ningún
// componente sea un enlace y que el archivo abierto sea el mismo inspeccionado.
func OpenBeneath(root, rel string) (*os.File, error) {
	if !ValidRelPath(rel) {
		return nil, ErrOutsideRoot
	}
	cur := root
	var last fs.FileInfo
	for _, s := range splitRel(rel) {
		cur = filepath.Join(cur, s)
		fi, err := os.Lstat(cur)
		if err != nil {
			return nil, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, ErrOutsideRoot
		}
		last = fi
	}
	if !last.Mode().IsRegular() {
		return nil, ErrOutsideRoot
	}
	f, err := os.Open(cur)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !os.SameFile(st, last) {
		f.Close()
		return nil, ErrOutsideRoot
	}
	return f, nil
}
