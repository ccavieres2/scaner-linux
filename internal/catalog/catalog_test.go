package catalog

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func write(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWalkAndOpen(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), []byte("hola"))
	write(t, filepath.Join(root, "sub", "b.csv"), []byte("x,y"))
	write(t, filepath.Join(root, "big.bin"), bytes.Repeat([]byte{1}, 100))

	var got []string
	skipped := map[string]string{}
	err := Walk(root, 50, func(e Entry) error {
		if e.SkipReason != "" {
			skipped[e.RelPath] = e.SkipReason
			return nil
		}
		got = append(got, e.RelPath)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if len(got) != 2 || got[0] != "a.txt" || got[1] != "sub/b.csv" || skipped["big.bin"] != SkipTooLarge {
		t.Fatalf("got=%v skipped=%v", got, skipped)
	}
	f, err := OpenBeneath(root, "sub/b.csv")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "x,y" {
		t.Fatalf("contenido %q", b)
	}
}

// SEC-004: traversal y enlaces fuera de raíz.
func TestOpenBeneathRejectsEscapes(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	write(t, filepath.Join(root, "ok.txt"), []byte("ok"))
	write(t, filepath.Join(base, "secreto.txt"), []byte("no"))
	for _, rel := range []string{"../secreto.txt", "/etc/passwd", "sub/../../secreto.txt", "./ok.txt", "", `..\secreto.txt`} {
		if f, err := OpenBeneath(root, rel); err == nil {
			f.Close()
			t.Errorf("OpenBeneath(%q) debía fallar", rel)
		}
	}
	if err := os.Symlink(filepath.Join(base, "secreto.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("no se pueden crear symlinks en este entorno: %v", err)
	}
	if f, err := OpenBeneath(root, "link.txt"); err == nil {
		f.Close()
		t.Fatal("no debe seguir symlink fuera de raíz")
	}
	if err := os.Symlink(base, filepath.Join(root, "dirlink")); err == nil {
		if f, err := OpenBeneath(root, "dirlink/secreto.txt"); err == nil {
			f.Close()
			t.Fatal("no debe atravesar directorio enlazado")
		}
	}
	var reasons []string
	_ = Walk(root, 1<<20, func(e Entry) error {
		if e.SkipReason != "" {
			reasons = append(reasons, e.RelPath+":"+e.SkipReason)
		}
		return nil
	})
	if len(reasons) == 0 {
		t.Fatal("los symlinks deben reportarse como omitidos")
	}
}

func TestWalkRootUnavailable(t *testing.T) {
	err := Walk(filepath.Join(t.TempDir(), "no-existe"), 1, func(Entry) error { return nil })
	if !errors.Is(err, ErrRootUnavailable) {
		t.Fatalf("esperado ErrRootUnavailable, got %v", err)
	}
}

func zipWith(t *testing.T, name string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(name)
	w.Write([]byte("<x/>"))
	zw.Close()
	return buf.Bytes()
}

func TestSniff(t *testing.T) {
	cases := []struct {
		data []byte
		name string
		want string
	}{
		{[]byte("%PDF-1.7\n..."), "x.txt", KindPDF}, // firma gana a extensión
		{zipWith(t, "xl/workbook.xml"), "a.zip", KindXLSX},
		{zipWith(t, "word/document.xml"), "a.xlsx", KindDOCX},
		{zipWith(t, "otra.txt"), "a.docx", KindZip},
		{[]byte("\x89PNG\r\n\x1a\nxxxx"), "a.pdf", KindImage},
		{[]byte("nombre,rut\nX,1"), "datos.CSV", KindCSV},
		{[]byte("texto con ñ y tildes á"), "notas", KindText},
		{[]byte("Latin1 \xf1and\xe1"), "l.txt", KindText},
		{[]byte{0x7f, 'E', 'L', 'F', 0, 0, 1, 2}, "bin.txt", KindUnknown},
		// Imágenes descargadas o de celular con extensión engañosa.
		{[]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), "carnet.jpg", KindImage},
		{[]byte("GIF89a\x01\x00\x01\x00"), "a.gif", KindImage},
		{append([]byte("BM\x00\x00\x00\x00\x00\x00\x00\x00\x36\x00\x00\x00\x28\x00\x00\x00"), make([]byte, 16)...), "a.bmp", KindImage},
		{[]byte("BM es el comienzo de este texto plano bastante largo"), "notas.txt", KindText},
		{[]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), "IMG_0001.jpg", KindHEIC},
	}
	for _, c := range cases {
		if got := Sniff(bytes.NewReader(c.data), int64(len(c.data)), c.name); got != c.want {
			t.Errorf("Sniff(%s) = %s; esperado %s", c.name, got, c.want)
		}
	}
}
