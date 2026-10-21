//go:build linux

package catalog

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func devIno(fi fs.FileInfo) (dev, ino uint64, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint64(st.Dev), st.Ino, true
}

// OpenBeneath abre root/rel en solo lectura con openat2(RESOLVE_BENEATH |
// RESOLVE_NO_SYMLINKS | RESOLVE_NO_XDEV). En kernels < 5.6 recurre a abrir
// componente por componente con O_NOFOLLOW.
func OpenBeneath(root, rel string) (*os.File, error) {
	if !ValidRelPath(rel) {
		return nil, ErrOutsideRoot
	}
	rootFd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrRootUnavailable
	}
	defer unix.Close(rootFd)
	how := unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV | unix.RESOLVE_NO_MAGICLINKS,
	}
	fd, err := unix.Openat2(rootFd, rel, &how)
	if errors.Is(err, unix.ENOSYS) {
		fd, err = openFallback(rootFd, rel)
	}
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EXDEV) {
			return nil, ErrOutsideRoot
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), rel)
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, ErrOutsideRoot
	}
	return f, nil
}

func openFallback(rootFd int, rel string) (int, error) {
	segs := splitRel(rel)
	dir, err := unix.Dup(rootFd)
	if err != nil {
		return -1, err
	}
	for i, s := range segs {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if i < len(segs)-1 {
			flags |= unix.O_DIRECTORY
		} else {
			flags |= unix.O_NONBLOCK
		}
		next, err := unix.Openat(dir, s, flags, 0)
		unix.Close(dir)
		if err != nil {
			return -1, err
		}
		dir = next
	}
	return dir, nil
}
