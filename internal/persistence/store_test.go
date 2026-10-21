package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"arsop-scanner/internal/testpg"
)

var baseDSN string

func TestMain(m *testing.M) { os.Exit(testpg.Main(m, &baseDSN)) }

func setup(t *testing.T) (*Store, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, testpg.FreshDB(t, baseDSN), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil { // idempotente
		t.Fatal(err)
	}
	ten, _ := st.EnsureTenant(ctx, "t")
	src, _ := st.EnsureSource(ctx, ten, "rrhh", "/data/input/rrhh", true)
	return st, ten, src
}

func obs(ten, src, scan uuid.UUID, rel string, size int64) Observation {
	return Observation{Tenant: ten, Source: src, Scan: scan, RelPath: rel, Size: size,
		MTime: time.Unix(1700000000, 0).UTC(), Identity: "1:2", RulesVersion: "r1"}
}

func result(token byte) FileResult {
	return FileResult{MimeKind: "text", Findings: []FindingRow{{
		Type: "rut", KeyVersion: 1, Token: bytes.Repeat([]byte{token}, 32), Status: "validated_format",
		Method: "digital", Confidence: 1, Detector: "rut@1", Occurrences: 1,
		Locations: []LocationRow{{Key: []byte{1}, Line: 3}},
	}}}
}

// DATA-001 / REL-001: reprocesar y reintentar tras un crash no duplica.
func TestIdempotentPipeline(t *testing.T) {
	ctx := context.Background()
	st, ten, src := setup(t)
	scan, err := st.CreateScan(ctx, ten, src, "incremental", "r1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateScan(ctx, ten, src, "incremental", "r1", "test"); !errors.Is(err, ErrScanActive) {
		t.Fatalf("segundo escaneo activo debe rechazarse: %v", err)
	}
	for i := 0; i < 2; i++ { // inventario repetido (reinicio)
		if _, err := st.ObserveFile(ctx, obs(ten, src, scan, "a.txt", 10)); err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := st.ClaimTasks(ctx, scan, "light", "w1", 10, time.Minute)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("claim: %v %d", err, len(tasks))
	}
	// Simula crash: el lease vence y otro worker reclama la misma tarea.
	if _, err := st.pool.Exec(ctx, "UPDATE file_tasks SET lease_expires_at = now() - interval '1s'"); err != nil {
		t.Fatal(err)
	}
	again, _ := st.ClaimTasks(ctx, scan, "light", "w2", 10, time.Minute)
	if len(again) != 1 || again[0].Attempt != 2 {
		t.Fatalf("reentrega tras lease vencido: %+v", again)
	}
	// El worker original ya no puede publicar.
	if err := st.CompleteTask(ctx, ten, tasks[0], result(9)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("esperado ErrLeaseLost, got %v", err)
	}
	if err := st.CompleteTask(ctx, ten, again[0], result(9)); err != nil {
		t.Fatal(err)
	}
	status, err := st.FinishScan(ctx, ten, scan)
	if err != nil || status != "completed" {
		t.Fatalf("finish: %s %v", status, err)
	}

	// Escaneo completo reprocesa la misma versión: sin duplicados.
	scan2, _ := st.CreateScan(ctx, ten, src, "full", "r1", "test")
	o := obs(ten, src, scan2, "a.txt", 10)
	o.Full = true
	if q, _ := st.ObserveFile(ctx, o); !q {
		t.Fatal("escaneo full debe reencolar")
	}
	tk, _ := st.ClaimTasks(ctx, scan2, "light", "w1", 10, time.Minute)
	if err := st.CompleteTask(ctx, ten, tk[0], result(9)); err != nil {
		t.Fatal(err)
	}
	var nf, nl, ni int
	st.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM findings),(SELECT count(*) FROM finding_locations),(SELECT count(*) FROM identifiers)").Scan(&nf, &nl, &ni)
	if nf != 1 || nl != 1 || ni != 1 {
		t.Fatalf("duplicados: findings=%d locations=%d identifiers=%d", nf, nl, ni)
	}
	st.FinishScan(ctx, ten, scan2)

	// Incremental sin cambios: no encola.
	scan3, _ := st.CreateScan(ctx, ten, src, "incremental", "r1", "test")
	if q, _ := st.ObserveFile(ctx, obs(ten, src, scan3, "a.txt", 10)); q {
		t.Fatal("archivo sin cambios no debe reencolarse en incremental")
	}
	// Cambio de versión: encola.
	if q, _ := st.ObserveFile(ctx, obs(ten, src, scan3, "a.txt", 11)); !q {
		t.Fatal("archivo modificado debe encolarse")
	}
}

func TestSearchAndRetryAndAudit(t *testing.T) {
	ctx := context.Background()
	st, ten, src := setup(t)
	scan, _ := st.CreateScan(ctx, ten, src, "incremental", "r1", "test")
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		st.ObserveFile(ctx, obs(ten, src, scan, n, 1))
	}
	tasks, _ := st.ClaimTasks(ctx, scan, "light", "w", 10, time.Minute)
	for _, tk := range tasks[:2] {
		if err := st.CompleteTask(ctx, ten, tk, result(7)); err != nil {
			t.Fatal(err)
		}
	}
	// Tercera tarea falla de forma reintentable, luego definitiva.
	if err := st.FailTask(ctx, tasks[2], "extract", "timeout", true, 1, "failed"); err != nil {
		t.Fatal(err)
	}
	st.pool.Exec(ctx, "UPDATE file_tasks SET available_at = now() WHERE state='retry_wait'")
	tk, _ := st.ClaimTasks(ctx, scan, "light", "w", 10, time.Minute)
	if len(tk) != 1 || tk[0].Attempt != 2 {
		t.Fatalf("reintento: %+v", tk)
	}
	if err := st.FailTask(ctx, tk[0], "extract", "timeout", true, 1, "failed"); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.PendingTasks(ctx, scan); n != 0 {
		t.Fatalf("pendientes: %d", n)
	}
	status, _ := st.FinishScan(ctx, ten, scan)
	if status != "partial" {
		t.Fatalf("con archivo fallido el escaneo debe ser partial, got %s", status)
	}

	// API-001: paginación estable por cursor.
	tok := [][]byte{bytes.Repeat([]byte{7}, 32)}
	page1, total, err := st.Search(ctx, ten, "rut", tok, uuid.Nil, 1)
	if err != nil || len(page1) != 1 || total != 2 || page1[0].Locations[0].Line != 3 {
		t.Fatalf("búsqueda p1: %v %d %+v", err, total, page1)
	}
	page2, _, _ := st.Search(ctx, ten, "rut", tok, page1[0].FileID, 1)
	if len(page2) != 1 || page2[0].FileID == page1[0].FileID {
		t.Fatalf("búsqueda p2: %+v", page2)
	}
	// Aislamiento por tenant (SEC-003).
	other, _ := st.EnsureTenant(ctx, "otro")
	if r, n, _ := st.Search(ctx, other, "rut", tok, uuid.Nil, 10); len(r) != 0 || n != 0 {
		t.Fatal("fuga entre tenants")
	}
	if _, err := st.GetFile(ctx, other, page1[0].FileID); !errors.Is(err, ErrNotFound) {
		t.Fatal("GetFile cruzado debe dar NotFound")
	}
	cov, _ := st.Coverage(ctx, ten)
	if cov.Status != "partial" || cov.FilesNotCovered != 1 {
		t.Fatalf("cobertura: %+v", cov)
	}
	errs, _ := st.ListScanErrors(ctx, ten, scan, 0, 10)
	if len(errs) != 1 || errs[0].Code != "timeout" || errs[0].Attempts != 2 {
		t.Fatalf("errores: %+v", errs)
	}
	// Auditoría append-only.
	if err := st.Audit(ctx, AuditEvent{Tenant: &ten, Principal: "p", Action: "search", Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, "DELETE FROM audit_events"); err == nil {
		t.Fatal("audit_events debe ser append-only")
	}
}

// Colas por clase, orden pequeños→grandes y reclasificación.
func TestClassesAndSizeOrder(t *testing.T) {
	ctx := context.Background()
	st, ten, src := setup(t)
	scan, _ := st.CreateScan(ctx, ten, src, "incremental", "r1", "test")
	add := func(name string, size int64, class string, prio int) {
		o := obs(ten, src, scan, name, size)
		o.Class, o.Priority = class, prio
		if _, err := st.ObserveFile(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	add("grande.pdf", 5<<20, "heavy", 200)
	add("mediano.pdf", 500<<10, "heavy", 100)
	add("chico.pdf", 10<<10, "heavy", 100)
	add("a.txt", 300, "light", 100)
	add("b.txt", 100, "light", 100)

	var order []string
	for {
		tk, err := st.ClaimTasks(ctx, scan, "heavy", "w", 1, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if len(tk) == 0 {
			break
		}
		order = append(order, tk[0].RelPath)
		st.CompleteTask(ctx, ten, tk[0], FileResult{MimeKind: "pdf"})
	}
	if got := fmt.Sprint(order); got != "[chico.pdf mediano.pdf grande.pdf]" {
		t.Fatalf("orden heavy: %s", got)
	}
	// La cola heavy no entrega archivos light.
	lt, _ := st.ClaimTasks(ctx, scan, "light", "w", 10, time.Minute)
	if len(lt) != 2 || lt[0].RelPath != "b.txt" {
		t.Fatalf("light: %+v", lt)
	}
	// Reclasificar: pasa a heavy sin consumir intento.
	if err := st.ReclassifyTask(ctx, lt[1], "heavy"); err != nil {
		t.Fatal(err)
	}
	hk, _ := st.ClaimTasks(ctx, scan, "heavy", "w2", 10, time.Minute)
	if len(hk) != 1 || hk[0].RelPath != "a.txt" || hk[0].Attempt != 1 {
		t.Fatalf("reclasificación: %+v", hk)
	}
	if err := st.ReclassifyTask(ctx, lt[1], "light"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("lease viejo no debe poder reclasificar: %v", err)
	}
}
