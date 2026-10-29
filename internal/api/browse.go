package api

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// Endpoints de solo lectura que usa la interfaz web (/ui). Igual que el
// resto de la API: requieren token, scope y quedan auditados.

//go:embed ui
var uiFiles embed.FS

// uiCSP: la interfaz solo carga sus propios archivos y solo habla con la API
// del mismo origen; sin scripts en línea ni recursos externos.
const uiCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func (s *Server) uiHandler() http.Handler {
	sub, _ := fs.Sub(uiFiles, "ui")
	files := http.StripPrefix("/ui/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", uiCSP)
		w.Header().Set("X-Frame-Options", "DENY")
		files.ServeHTTP(w, r)
	})
}

// me devuelve el principal y sus scopes para que la interfaz muestre solo
// lo que el token permite.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	scopes := []string{}
	for _, sc := range []string{ScopeSearch, ScopeFiles, ScopeNames, ScopeScans, ScopeDiagnose} {
		if p.Has(sc) {
			scopes = append(scopes, sc)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"principal": p.ID, "scopes": scopes})
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	sources, err := s.store.SourceSummaries(r.Context(), s.tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	cov, err := s.store.Coverage(r.Context(), s.tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	s.audit(r, p, "overview.read", "ok", "", nil)
	writeJSON(w, http.StatusOK, map[string]any{"coverage": cov, "sources": sources})
}

func (s *Server) listScans(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	list, err := s.store.ListScans(r.Context(), s.tenant, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	s.audit(r, p, "scans.list", "ok", "", nil)
	writeJSON(w, http.StatusOK, map[string]any{"scans": list})
}

var sourceNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// sourceFiles lista los archivos de una fuente con su cantidad de RUT y
// correos. La ruta solo se entrega con el scope files:names.
func (s *Server) sourceFiles(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	name := chi.URLParam(r, "name")
	if !sourceNameRe.MatchString(name) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 0)
	if page > 100000 {
		writeError(w, http.StatusBadRequest, "invalid_page")
		return
	}
	size := s.opt.MaxPageSize
	only := r.URL.Query().Get("with_findings") == "true"
	list, ok, err := s.store.ListSourceFiles(r.Context(), s.tenant, name, only, page*size, size+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	more := len(list) > size
	if more {
		list = list[:size]
	}
	names := p.Has(ScopeNames)
	out := make([]map[string]any, 0, len(list))
	for _, f := range list {
		m := map[string]any{"file_id": f.ID, "size_bytes": f.Size, "kind": f.Kind, "state": f.State,
			"ruts": f.RUTs, "emails": f.Emails, "ocr": f.OCR, "deferred": f.Deferred}
		if names {
			m["display_name"] = path.Base(f.RelPath)
			m["relative_path"] = f.RelPath
		}
		out = append(out, m)
	}
	n := len(out)
	s.audit(r, p, "files.list", "ok", "", &n)
	writeJSON(w, http.StatusOK, map[string]any{"source": name, "page": page, "has_more": more, "files": out})
}
