// Package api expone /api/v1 (spec §24). Deny-by-default: toda ruta salvo
// /health requiere autenticación y un scope explícito.
package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"arsop-scanner/internal/identity"
	"arsop-scanner/internal/persistence"
	"arsop-scanner/internal/security"
)

const SchemaVersion = "1"

type Options struct {
	MaxBodyBytes        int64
	MaxPageSize         int
	SearchRatePerMinute int
	SearchBurst         int
	CursorTTL           time.Duration
	// AllowedNets: si no está vacío, solo se aceptan conexiones desde estas redes.
	AllowedNets []*net.IPNet
	// RequireClientCert: modo mtls_token; sin certificado de cliente no hay acceso.
	RequireClientCert bool
}

type Server struct {
	store      *persistence.Store
	key        *identity.Key
	sealer     *security.Sealer
	tenant     uuid.UUID
	principals *Principals
	opt        Options
	log        *slog.Logger
	cursorKey  []byte

	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

func New(store *persistence.Store, key *identity.Key, sealer *security.Sealer, tenant uuid.UUID, ps *Principals, opt Options, log *slog.Logger) *Server {
	if opt.CursorTTL == 0 {
		opt.CursorTTL = 15 * time.Minute
	}
	return &Server{
		store: store, key: key, sealer: sealer, tenant: tenant, principals: ps, opt: opt, log: log,
		// Clave de cursores derivada de la clave HMAC con separación de dominio.
		cursorKey: key.LookupToken("cursor", "v1"),
		limiters:  map[string]*rate.Limiter{},
	}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(s.common)
	r.Use(s.allowNetwork)
	r.Get("/api/v1/health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/api/v1/health/ready", s.ready)
	// Interfaz web: archivos estáticos sin datos; todo dato pasa por la API autenticada.
	r.Get("/", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ui/", http.StatusFound) })
	r.Get("/ui", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ui/", http.StatusFound) })
	r.Get("/ui/*", s.uiHandler().ServeHTTP)
	r.Group(func(r chi.Router) {
		r.Use(s.authenticate)
		r.Get("/api/v1/me", s.me)
		r.Get("/api/v1/overview", s.require(ScopeScans, s.overview))
		r.Get("/api/v1/scans", s.require(ScopeScans, s.listScans))
		r.Get("/api/v1/sources/{name}/files", s.require(ScopeFiles, s.sourceFiles))
		r.Post("/api/v1/identifiers/search", s.require(ScopeSearch, s.search))
		r.Get("/api/v1/files/{id}", s.require(ScopeFiles, s.getFile))
		r.Get("/api/v1/scans/{id}", s.require(ScopeScans, s.getScan))
		r.Get("/api/v1/scans/{id}/errors", s.require(ScopeDiagnose, s.scanErrors))
		r.Get("/api/v1/scans/{id}/pending", s.require(ScopeScans, s.scanPending))
	})
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { writeError(w, http.StatusNotFound, "not_found") })
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	})
	return r
}

type reqIDKey struct{}

func (s *Server) common(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 12)
		_, _ = rand.Read(b)
		id := hex.EncodeToString(b)
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Request-Id", id)
		h.Set("Content-Security-Policy", "default-src 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		r.Body = http.MaxBytesReader(w, r.Body, s.opt.MaxBodyBytes)
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic en handler", "request_id", id, "error_code", "internal")
				writeError(w, http.StatusInternalServerError, "internal_error")
			}
		}()
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), reqIDKey{}, id)))
	})
}

func requestID(r *http.Request) string { id, _ := r.Context().Value(reqIDKey{}).(string); return id }

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	v, err := s.store.SchemaVersion(ctx)
	if err != nil || v < 1 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "schema_version": v})
}

func (s *Server) limiter(principal string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.limiters[principal]
	if l == nil {
		l = rate.NewLimiter(rate.Limit(float64(s.opt.SearchRatePerMinute)/60), s.opt.SearchBurst)
		s.limiters[principal] = l
	}
	return l
}

// audit nunca recibe valores de identificadores.
func (s *Server) audit(r *http.Request, p *Principal, action, outcome, caseRef string, count *int) {
	pid := "anonymous"
	if p != nil {
		pid = p.ID
	}
	t := s.tenant
	err := s.store.Audit(r.Context(), persistence.AuditEvent{Tenant: &t, Principal: pid, Action: action, Outcome: outcome,
		CaseReference: caseRef, RequestID: requestID(r), ResultCount: count})
	if err != nil {
		s.log.Error("fallo al escribir auditoría", "request_id", requestID(r), "error_code", "audit_write_failed")
	}
	s.log.Info("api", "request_id", requestID(r), "principal", pid, "action", action, "outcome", outcome)
}

var caseRefRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type searchRequest struct {
	Type          string `json:"type"`
	Value         string `json:"value"`
	CaseReference string `json:"case_reference"`
	PageSize      int    `json:"page_size"`
	Cursor        string `json:"cursor"`
}

type locationOut struct {
	Page   int    `json:"page,omitempty"`
	Sheet  string `json:"sheet,omitempty"`
	Row    int    `json:"row,omitempty"`
	Column string `json:"column,omitempty"`
	Line   int    `json:"line,omitempty"`
}

type resultOut struct {
	FileID       string        `json:"file_id"`
	DisplayName  string        `json:"display_name,omitempty"`
	Locations    []locationOut `json:"locations"`
	Occurrences  int           `json:"occurrences"`
	Method       string        `json:"method"`
	Confidence   float64       `json:"confidence"`
	ReviewStatus string        `json:"review_status"`
	VersionState string        `json:"version_state"`
	ProcessedAt  *time.Time    `json:"processed_at,omitempty"`
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	// Límite antes de cualquier validación: toda solicitud cuenta (SEC-007).
	if !s.limiter(p.ID).Allow() {
		s.audit(r, p, "identifiers.search", "rate_limited", "", nil)
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	var req searchRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		s.audit(r, p, "identifiers.search", "bad_request", "", nil)
		writeError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if !caseRefRe.MatchString(req.CaseReference) {
		s.audit(r, p, "identifiers.search", "bad_request", "", nil)
		writeError(w, http.StatusBadRequest, "case_reference_required")
		return
	}
	typ := identity.Type(req.Type)
	if typ != identity.TypeRUT && typ != identity.TypeEmail {
		s.audit(r, p, "identifiers.search", "bad_request", req.CaseReference, nil)
		writeError(w, http.StatusBadRequest, "unsupported_type")
		return
	}
	norm, err := identity.Normalize(typ, req.Value)
	if err != nil {
		// No se repite el valor recibido en la respuesta ni en logs.
		s.audit(r, p, "identifiers.search", "invalid_identifier", req.CaseReference, nil)
		writeError(w, http.StatusBadRequest, "invalid_identifier")
		return
	}
	size := req.PageSize
	if size <= 0 || size > s.opt.MaxPageSize {
		size = min(25, s.opt.MaxPageSize)
	}
	token := s.key.LookupToken(typ, norm)
	qh := sha256.Sum256(token)
	after := uuid.Nil
	if req.Cursor != "" {
		after, err = s.parseCursor(req.Cursor, qh[:8])
		if err != nil {
			s.audit(r, p, "identifiers.search", "bad_cursor", req.CaseReference, nil)
			writeError(w, http.StatusBadRequest, "invalid_cursor")
			return
		}
	}
	rows, total, err := s.store.Search(r.Context(), s.tenant, string(typ), [][]byte{token}, after, size+1)
	if err != nil {
		s.log.Error("búsqueda falló", "request_id", requestID(r), "error_code", "db_error")
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	cov, err := s.store.Coverage(r.Context(), s.tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	var next *string
	if len(rows) > size {
		rows = rows[:size]
		c := s.makeCursor(rows[len(rows)-1].FileID, qh[:8])
		next = &c
	}
	names := p.Has(ScopeNames)
	results := make([]resultOut, 0, len(rows))
	for _, row := range rows {
		ro := resultOut{FileID: row.FileID.String(), Occurrences: row.Occurrences, Method: row.Method,
			Confidence: row.Confidence, ReviewStatus: reviewStatus(row.Status), VersionState: row.VersionState,
			ProcessedAt: row.ProcessedAt, Locations: []locationOut{}}
		if names {
			ro.DisplayName = path.Base(row.RelPath)
		}
		for _, l := range row.Locations {
			lo := locationOut{Page: l.Page, Row: l.Row, Column: l.Column, Line: l.Line}
			if len(l.SheetEnc) > 0 {
				if names {
					if b, err := s.sealer.Open(l.SheetEnc, row.FileVersionID[:]); err == nil {
						lo.Sheet = string(b)
					}
				} else {
					lo.Sheet = "[redacted]"
				}
			}
			ro.Locations = append(ro.Locations, lo)
		}
		results = append(results, ro)
	}
	s.audit(r, p, "identifiers.search", "ok", req.CaseReference, &total)
	writeJSON(w, http.StatusOK, map[string]any{
		"request_id":     requestID(r),
		"schema_version": SchemaVersion,
		"as_of":          time.Now().UTC().Format(time.RFC3339),
		"type":           typ,
		"result_count":   total,
		"coverage":       cov,
		"results":        results,
		"next_cursor":    next,
		"notice":         "Coincidencias potenciales: no acreditan identidad del titular; ausencia de resultados no implica ausencia de datos si la cobertura es parcial.",
	})
}

// reviewStatus traduce al vocabulario de la spec §31.
func reviewStatus(s string) string {
	if s == "validated_format" {
		return "potential_match"
	}
	return s
}

func (s *Server) makeCursor(after uuid.UUID, q []byte) string {
	exp := time.Now().Add(s.opt.CursorTTL).Unix()
	payload := after.String() + "|" + hex.EncodeToString(q) + "|" + strconv.FormatInt(exp, 10)
	m := hmac.New(sha256.New, s.cursorKey)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) parseCursor(c string, q []byte) (uuid.UUID, error) {
	bad := errors.New("cursor inválido")
	pl, sig, ok := strings.Cut(c, ".")
	if !ok || len(c) > 512 {
		return uuid.Nil, bad
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(pl)
	mac, err2 := base64.RawURLEncoding.DecodeString(sig)
	if err1 != nil || err2 != nil {
		return uuid.Nil, bad
	}
	m := hmac.New(sha256.New, s.cursorKey)
	m.Write(payload)
	if !hmac.Equal(mac, m.Sum(nil)) {
		return uuid.Nil, bad
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != 3 || parts[1] != hex.EncodeToString(q) {
		return uuid.Nil, bad
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return uuid.Nil, bad
	}
	return uuid.Parse(parts[0])
}

func parseID(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	return id, err == nil
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	id, ok := parseID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	f, err := s.store.GetFile(r.Context(), s.tenant, id)
	if errors.Is(err, persistence.ErrNotFound) {
		s.audit(r, p, "files.read", "not_found", "", nil)
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	out := map[string]any{"file_id": f.ID, "source": f.SourceName, "size_bytes": f.SizeBytes, "kind": f.MimeKind,
		"state": f.State, "skip_reason": f.SkipReason, "last_observed_at": f.LastObserved, "processed_at": f.ProcessedAt}
	if p.Has(ScopeNames) {
		out["display_name"] = path.Base(f.RelPath)
		out["relative_path"] = f.RelPath
	}
	s.audit(r, p, "files.read", "ok", "", nil)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getScan(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	id, ok := parseID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	sc, err := s.store.GetScan(r.Context(), s.tenant, id)
	if errors.Is(err, persistence.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	prog, err := s.store.ScanProgress(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	s.audit(r, p, "scans.read", "ok", "", nil)
	writeJSON(w, http.StatusOK, map[string]any{"scan_id": sc.ID, "source": sc.SourceName, "type": sc.Type,
		"status": sc.Status, "started_at": sc.StartedAt, "inventory_completed_at": sc.InventoryCompletedAt,
		"completed_at": sc.CompletedAt, "rules_version": sc.RulesVersion, "scanner_version": sc.ScannerVersion,
		"progress": prog})
}

func (s *Server) scanErrors(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	id, ok := parseID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if _, err := s.store.GetScan(r.Context(), s.tenant, id); err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	limit := s.opt.MaxPageSize
	errs, err := s.store.ListScanErrors(r.Context(), s.tenant, id, after, limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	var next *string
	if len(errs) > limit {
		errs = errs[:limit]
		c := strconv.FormatInt(errs[len(errs)-1].ID, 10)
		next = &c
	}
	out := make([]map[string]any, 0, len(errs))
	for _, e := range errs {
		out = append(out, map[string]any{"file_id": e.FileID, "stage": e.Stage, "code": e.Code, "attempts": e.Attempts,
			"first_seen_at": e.FirstSeen, "last_seen_at": e.LastSeen})
	}
	s.audit(r, p, "scans.errors", "ok", "", nil)
	writeJSON(w, http.StatusOK, map[string]any{"errors": out, "next_cursor": next})
}

// scanPending lista los archivos grandes diferidos (no procesados) del
// escaneo. La ruta solo se entrega con el scope files:names.
func (s *Server) scanPending(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	id, ok := parseID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if _, err := s.store.GetScan(r.Context(), s.tenant, id); err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	q := []byte("pending|" + id.String())
	after := uuid.Nil
	if c := r.URL.Query().Get("cursor"); c != "" {
		var err error
		if after, err = s.parseCursor(c, q); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_cursor")
			return
		}
	}
	limit := s.opt.MaxPageSize
	list, err := s.store.ListDeferred(r.Context(), s.tenant, id, after, limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	var next *string
	if len(list) > limit {
		list = list[:limit]
		c := s.makeCursor(list[len(list)-1].TaskID, q)
		next = &c
	}
	out := make([]map[string]any, 0, len(list))
	for _, d := range list {
		m := map[string]any{"file_id": d.FileID, "size_bytes": d.Size, "queue": d.Class, "deferred_at": d.DeferredAt}
		if p.Has(ScopeNames) {
			m["display_name"] = path.Base(d.RelPath)
			m["relative_path"] = d.RelPath
		}
		out = append(out, m)
	}
	n := len(out)
	s.audit(r, p, "scans.pending", "ok", "", &n)
	writeJSON(w, http.StatusOK, map[string]any{"pending": out, "next_cursor": next})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
