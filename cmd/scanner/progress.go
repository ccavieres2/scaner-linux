package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"arsop-scanner/internal/persistence"
)

// isTerminal indica si w es una consola interactiva (no un archivo ni un pipe).
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// progress dibuja una barra en una sola línea, consultando el avance en la
// base cada medio segundo. Implementa io.Writer para que los logs se impriman
// encima de la barra sin mezclarse con ella.
type progress struct {
	mu    sync.Mutex
	out   io.Writer
	line  string
	start time.Time
	stop  chan struct{}
	done  sync.WaitGroup
}

func startProgress(ctx context.Context, st *persistence.Store, tenant, scan uuid.UUID, out io.Writer) *progress {
	p := &progress{out: out, start: time.Now(), stop: make(chan struct{})}
	p.done.Go(func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			p.refresh(ctx, st, tenant, scan)
			select {
			case <-p.stop:
				p.refresh(context.WithoutCancel(ctx), st, tenant, scan)
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	})
	return p
}

func (p *progress) refresh(ctx context.Context, st *persistence.Store, tenant, scan uuid.UUID) {
	sc, err := st.GetScan(ctx, tenant, scan)
	if err != nil {
		return
	}
	pr, err := st.ScanProgress(ctx, scan)
	if err != nil {
		return
	}
	p.draw(renderProgress(pr, sc.InventoryCompletedAt != nil, time.Since(p.start)))
}

// renderProgress arma el texto de la barra. Los diferidos por tamaño no
// cuentan en el total: se procesan después con scan process-pending.
func renderProgress(pr *persistence.Progress, inventoried bool, elapsed time.Duration) string {
	total, open := 0, 0
	for state, n := range pr.Tasks {
		switch state {
		case "deferred":
			continue
		case "pending", "retry_wait", "processing":
			open += n
		}
		total += n
	}
	done := total - open
	el := elapsed.Truncate(time.Second)
	if !inventoried {
		return fmt.Sprintf("contando archivos... %d encontrados  %s", total+pr.Tasks["deferred"], el)
	}
	pct := 100
	if total > 0 {
		pct = done * 100 / total
	}
	const width = 30
	fill := pct * width / 100
	s := fmt.Sprintf("[%s%s] %3d%%  %d/%d archivos  %s", strings.Repeat("#", fill), strings.Repeat("-", width-fill),
		pct, done, total, el)
	if n := pr.Tasks["deferred"]; n > 0 {
		s += fmt.Sprintf("  (%d pendientes por tamaño)", n)
	}
	return s
}

func (p *progress) draw(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.line = line
	fmt.Fprintf(p.out, "\r\033[K%s", line)
}

// Write imprime un log: borra la barra, escribe la línea y la vuelve a dibujar.
func (p *progress) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprint(p.out, "\r\033[K")
	n, err := p.out.Write(b)
	fmt.Fprint(p.out, p.line)
	return n, err
}

// Stop dibuja el estado final y deja la barra en su propia línea.
func (p *progress) Stop() {
	close(p.stop)
	p.done.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.line != "" {
		fmt.Fprintln(p.out)
	}
}
