// Comando api: servidor HTTP /api/v1 (spec §24). Funciona aunque el escáner
// esté detenido. Escucha solo en la interfaz configurada (loopback por defecto).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"arsop-scanner/internal/api"
	"arsop-scanner/internal/app"
	"arsop-scanner/internal/config"
)

func main() {
	file := flag.String("file", "/etc/arsop/scanner.yaml", "ruta del archivo de configuración YAML")
	flag.Parse()
	log := app.NewLogger(os.Stderr)
	cfg, err := config.LoadFile(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rt, err := app.Open(ctx, cfg, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer rt.Close()
	tenant, _, err := rt.EnsureCatalog(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catálogo (¿ejecutó scanner init?):", err)
		os.Exit(1)
	}
	pf, err := os.Open(cfg.API.Auth.PrincipalsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "principals_file:", err)
		os.Exit(1)
	}
	mtls := cfg.API.Auth.Mode == "mtls_token"
	ps, err := api.ParsePrincipals(pf, mtls)
	pf.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !mtls {
		log.Warn("modo de autenticación static_token_dev: solo para pruebas; con clientes use mtls_token (ADR-0006)")
	}
	if ids := ps.Expiring(14 * 24 * time.Hour); len(ids) > 0 {
		log.Warn("credenciales de API vencen en menos de 14 días o ya vencieron; rotar", "principals", ids)
	}
	var nets []*net.IPNet
	for _, c := range cfg.API.AllowedCIDRs {
		_, n, _ := net.ParseCIDR(c) // validado en config
		nets = append(nets, n)
	}
	srv := api.New(rt.Store, rt.Key, rt.Sealer, tenant, ps, api.Options{
		MaxBodyBytes: cfg.API.MaxBodyBytes, MaxPageSize: cfg.API.MaxPageSize,
		SearchRatePerMinute: cfg.API.SearchRatePerMinute, SearchBurst: cfg.API.SearchBurst,
		AllowedNets: nets, RequireClientCert: mtls,
	}, log)
	tlsCfg, err := api.ServerTLS(cfg.API.TLS.ClientCAFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tls:", err)
		os.Exit(1)
	}
	listen := cfg.API.Bind
	if cfg.API.ContainerBindAll {
		listen = "0.0.0.0" // aislado por la red Docker; publicado solo en api.bind del host
	}
	hs := &http.Server{
		Addr:              net.JoinHostPort(listen, strconv.Itoa(cfg.API.Port)),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		TLSConfig:         tlsCfg,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Info("api escuchando", "addr", hs.Addr, "tls", cfg.API.TLS.CertFile != "", "auth", cfg.API.Auth.Mode,
		"allowed_cidrs", len(nets))
	if cfg.API.TLS.CertFile != "" {
		err = hs.ListenAndServeTLS(cfg.API.TLS.CertFile, cfg.API.TLS.KeyFile)
	} else {
		err = hs.ListenAndServe() // solo loopback (validado en config)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
