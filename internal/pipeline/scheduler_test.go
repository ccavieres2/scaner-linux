package pipeline_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"arsop-scanner/internal/pipeline"
)

func readFixture(t *testing.T, parts ...string) []byte {
	b, err := os.ReadFile(filepath.Join(append([]string{"..", "..", "tests", "fixtures"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// bigPDF devuelve el PDF de ejemplo con relleno al final (válido para pdfium).
func bigPDF(t *testing.T, extraKB int) []byte {
	pad := bytes.Repeat([]byte("% relleno de prueba para tamano\n"), extraKB*1024/32)
	return append(readFixture(t, "synthetic", "tree", "contrato.pdf"), pad...)
}

type tracker struct {
	mu                    sync.Mutex
	reserved, maxReserved int64
	heavy, maxHeavy       int
	order                 map[string][]string
}

func (tr *tracker) hooks() *pipeline.Hooks {
	tr.order = map[string][]string{}
	return &pipeline.Hooks{
		OnStart: func(class, rel string, mb int64) {
			tr.mu.Lock()
			defer tr.mu.Unlock()
			tr.reserved += mb
			tr.maxReserved = max(tr.maxReserved, tr.reserved)
			if class == pipeline.ClassHeavy {
				tr.heavy++
				tr.maxHeavy = max(tr.maxHeavy, tr.heavy)
			}
			tr.order[class] = append(tr.order[class], rel)
		},
		OnEnd: func(class, rel string, mb int64) {
			tr.mu.Lock()
			defer tr.mu.Unlock()
			tr.reserved -= mb
			if class == pipeline.ClassHeavy {
				tr.heavy--
			}
		},
	}
}

// El presupuesto de memoria limita la concurrencia real de workers pesados,
// aunque haya más workers configurados; y los archivos dañados no detienen el escaneo.
func TestMemoryBudgetAndHostileFilesInScan(t *testing.T) {
	e := newEnv(t, 1000)
	root := e.src.Root
	os.MkdirAll(filepath.Join(root, "lote"), 0o755)
	for i := 0; i < 6; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("lote/grande_%d.pdf", i)), bigPDF(t, 1200+100*i), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "factura.txt"), readFixture(t, "synthetic", "tree", "contrato.pdf"), 0o644) // PDF disfrazado
	os.WriteFile(filepath.Join(root, "con_clave.pdf"), readFixture(t, "hostile", "con_clave.pdf"), 0o644)
	pdf := readFixture(t, "synthetic", "tree", "contrato.pdf")
	os.WriteFile(filepath.Join(root, "truncado.pdf"), pdf[:len(pdf)/3], 0o644)

	tr := &tracker{}
	e.pipe.Hooks = tr.hooks()
	e.pipe.LightWorkers, e.pipe.HeavyWorkers = 2, 4
	e.pipe.WorkerMemoryMB = 512
	e.pipe.MemoryBudgetMB = 2*512 + 2*pipeline.LightReserveMB // alcanza para solo 2 pesados a la vez

	if status := e.scan(t, "incremental"); status != "partial" {
		t.Fatalf("estado: %s", status)
	}
	t.Logf("reserva máxima=%d MB (presupuesto %d), pesados simultáneos máx=%d", tr.maxReserved, e.pipe.MemoryBudgetMB, tr.maxHeavy)
	if tr.maxReserved > int64(e.pipe.MemoryBudgetMB) {
		t.Fatalf("se excedió el presupuesto de memoria: %d > %d", tr.maxReserved, e.pipe.MemoryBudgetMB)
	}
	if tr.maxHeavy > 2 || tr.maxHeavy < 1 {
		t.Fatalf("concurrencia pesada fuera de lo esperado: %d", tr.maxHeavy)
	}
	// El PDF disfrazado de .txt pasó por la cola liviana y fue reclasificado a pesada.
	if !contains(tr.order[pipeline.ClassLight], "factura.txt") || !contains(tr.order[pipeline.ClassHeavy], "factura.txt") {
		t.Fatalf("reclasificación: light=%v heavy=%v", tr.order[pipeline.ClassLight], tr.order[pipeline.ClassHeavy])
	}
	// Sus datos quedaron indexados con la página correcta, igual que los PDF grandes.
	_, body := e.post(t, e.tokens["full"], map[string]any{"type": "rut", "value": "5.126.663-3", "case_reference": "C-1"})
	if n := int(body["result_count"].(float64)); n != 8 { // contrato + factura.txt + 6 grandes
		t.Fatalf("archivos con el RUT: %d", n)
	}
	// Errores clasificados, sin detener el escaneo.
	errs, _ := e.store.ListScanErrors(context.Background(), e.tenant, e.lastScan, 0, 100)
	codes := map[string]bool{}
	for _, er := range errs {
		codes[er.Code] = true
	}
	if !codes["encrypted"] || !codes["corrupt"] {
		t.Fatalf("códigos de error: %v", codes)
	}
}

// Orden: dentro de cada cola primero los pequeños y los >= large_file_bytes al final.
func TestSmallFilesFirst(t *testing.T) {
	e := newEnv(t, 1000)
	root := e.src.Root
	sizes := map[string]int{"g3.pdf": 3000, "g1.pdf": 1100, "g2.pdf": 2000}
	for name, kb := range sizes {
		os.WriteFile(filepath.Join(root, name), bigPDF(t, kb), 0o644)
	}
	tr := &tracker{}
	e.pipe.Hooks = tr.hooks()
	e.pipe.LightWorkers, e.pipe.HeavyWorkers = 1, 1 // secuencial para observar el orden exacto
	e.scan(t, "incremental")
	heavy := tr.order[pipeline.ClassHeavy]
	t.Logf("orden cola pesada: %v", heavy)
	var last int64 = -1
	seenLarge := false
	for _, rel := range heavy {
		fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		large := fi.Size() >= e.pipe.LargeFileBytes
		if seenLarge && !large {
			t.Fatalf("un archivo pequeño (%s) se procesó después de uno grande: %v", rel, heavy)
		}
		if large {
			seenLarge = true
		}
		if fi.Size() < last {
			t.Fatalf("orden no ascendente por tamaño: %v", heavy)
		}
		last = fi.Size()
	}
	if len(heavy) != 5 || !strings.HasSuffix(heavy[len(heavy)-1], "g3.pdf") {
		t.Fatalf("orden: %v", heavy)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
