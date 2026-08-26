package engine

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// DefaultCertCacheDir persists issued certificates across restarts.
// Let's Encrypt enforces strict issuance rate limits, so caching is mandatory
// for production use.
const DefaultCertCacheDir = "spine_certs"

// TLSConfig configures automatic HTTPS for a Spine deployment.
//
// Two mutually exclusive modes are supported:
//
//   - ACME (Let's Encrypt) — set Domains to one or more public DNS names that
//     resolve to this host. Certificates are provisioned on first start and
//     auto-renewed before expiry. Requires inbound reachability on ports 80
//     (HTTP-01 challenge + redirect) and the HTTPS listen port.
//   - Bring-your-own certificate — set CertFile and KeyFile to PEM paths.
type TLSConfig struct {
	// Domains enables ACME mode: free, automatically renewed Let's Encrypt
	// certificates for exactly these hostnames. Requests for any other
	// hostname are refused by the HostPolicy (prevents issuance abuse).
	Domains []string

	// Email is the optional ACME account contact for expiry notices.
	Email string

	// CacheDir persists certificates between restarts.
	// Defaults to DefaultCertCacheDir ("spine_certs").
	CacheDir string

	// CertFile and KeyFile enable BYO-certificate mode instead of ACME.
	CertFile string
	KeyFile  string
}

// newACMEManager builds an autocert.Manager from cfg, validating the config.
func newACMEManager(cfg *TLSConfig) (*autocert.Manager, error) {
	if len(cfg.Domains) == 0 {
		return nil, fmt.Errorf("acme mode requires at least one domain")
	}
	if cfg.CertFile != "" || cfg.KeyFile != "" {
		return nil, fmt.Errorf("cannot combine --domain with --tls-cert/--tls-key; choose one TLS mode")
	}

	cacheDir := cfg.CacheDir
	if cacheDir == "" {
		cacheDir = DefaultCertCacheDir
	}

	allowed := make(map[string]bool, len(cfg.Domains))
	for _, d := range cfg.Domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if strings.HasPrefix(d, "*.") {
			// autocert.HostWhitelist does exact matching only — a wildcard
			// entry would silently fail for every subdomain. Refuse loudly.
			return nil, fmt.Errorf("wildcard ACME domain '%s' is not supported (autocert HostWhitelist matches exact hostnames only); list each subdomain explicitly", d)
		}
		if d == "" {
			return nil, fmt.Errorf("empty domain in TLS config")
		}
		allowed[d] = true
	}

	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Email:      cfg.Email,
		Cache:      autocert.DirCache(cacheDir),
		HostPolicy: autocert.HostWhitelist(keysOf(allowed)...),
	}
	return m, nil
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// httpsRedirectHandler upgrades plain-HTTP requests to HTTPS while leaving
// ACME HTTP-01 challenge paths untouched (the manager handles those itself).
func httpsRedirectHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if host == "" {
			http.Error(w, "missing Host header", http.StatusBadRequest)
			return
		}
		if strings.HasSuffix(host, ":80") {
			host = strings.TrimSuffix(host, ":80")
		}
		target := "https://" + host + r.RequestURI
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}

// httpChallengeServer returns a port-80 server serving ACME HTTP-01
// challenges and redirecting everything else to HTTPS.
func httpChallengeServer(m *autocert.Manager) *http.Server {
	return &http.Server{
		Addr:         ":80",
		Handler:      m.HTTPHandler(httpsRedirectHandler()),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}

// ListenAndServeTLS starts the engine over HTTPS with graceful shutdown,
// mirroring ListenAndServe. The TLS mode is selected by cfg:
//
//   - cfg.Domains non-empty  → free Let's Encrypt certs via ACME, auto-renewed,
//     cached in cfg.CacheDir; also binds :80 for challenges/redirects.
//   - cfg.CertFile+KeyFile   → serve the provided PEM certificate.
func (e *Engine) ListenAndServeTLS(addr string, cfg *TLSConfig) error {
	if cfg == nil {
		return fmt.Errorf("ListenAndServeTLS requires a *TLSConfig")
	}
	if e.spineFile != "" {
		e.StartHotReload()
	}
	mux := e.buildMux()
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	srv.SetKeepAlivesEnabled(true)

	var starts []func() error
	var servers []*http.Server

	switch {
	case len(cfg.Domains) > 0:
		m, err := newACMEManager(cfg)
		if err != nil {
			return err
		}
		srv.TLSConfig = &tls.Config{
			GetCertificate: m.GetCertificate,
			MinVersion:     tls.VersionTLS12,
			NextProtos:     []string{"h2", "http/1.1"},
		}
		challenge := httpChallengeServer(m)
		servers = append(servers, srv, challenge)
		starts = append(starts,
			func() error { return srv.ListenAndServeTLS("", "") },
			func() error { return challenge.ListenAndServe() },
		)
		log.Printf("[spine] ACME enabled for %v (cert cache: %s)", cfg.Domains, orDefault(cfg.CacheDir, DefaultCertCacheDir))
	case cfg.CertFile != "" && cfg.KeyFile != "":
		// State the TLS floor explicitly instead of relying on the Go
		// server default: TLS 1.2 minimum, HTTP/2 negotiated.
		srv.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"h2", "http/1.1"},
		}
		servers = append(servers, srv)
		starts = append(starts, func() error { return srv.ListenAndServeTLS(cfg.CertFile, cfg.KeyFile) })
	default:
		return fmt.Errorf("TLSConfig needs either Domains (Let's Encrypt) or CertFile+KeyFile")
	}

	return e.serveWithShutdown(servers, starts)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// serveWithShutdown runs all server start functions concurrently and blocks
// until one fails or SIGINT/SIGTERM arrives, then drains every server with a
// 10-second grace period.
func (e *Engine) serveWithShutdown(servers []*http.Server, starts []func() error) error {
	errCh := make(chan error, len(starts))
	for _, start := range starts {
		go func(s func() error) { errCh <- s() }(start)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		// One server failed to start/serve — drain the rest so no listener
		// is left running when the caller tears down (avoids leaked servers
		// and half-open ports after e.g. "port already in use").
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, srv := range servers {
			_ = srv.Shutdown(ctx)
		}
		return err
	case sig := <-quit:
		log.Printf("[spine] received signal %v, shutting down gracefully...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var firstErr error
		for _, srv := range servers {
			if err := srv.Shutdown(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if firstErr != nil {
			log.Printf("[spine] graceful shutdown error: %v", firstErr)
			return firstErr
		}
		log.Printf("[spine] server stopped")
		return nil
	}
}
