package engine

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// domain.connect — one-click custom-domain connect from the admin panel.
//
// The admin types a hostname; the engine verifies DNS actually points at this
// host (fail-fast, same spirit as Stripe Connect's key-prefix check), then
// adds it to the live Let's Encrypt allowlist so the certificate is issued on
// the next TLS handshake for that host. No restart needed.
//
// Session-only semantics mirror stripe.connect:
//
//   - Domains added here live in process memory until restart. Startup flags
//     (--domain / env SPINE_TLS_DOMAINS) keep precedence and are re-applied on
//     the next boot.
//   - Only successful connects are remembered; nothing is written to the DB.
//   - BYO-certificate mode (--tls-cert/--tls-key) cannot accept new domains at
//     runtime — refuse loudly instead of silently doing nothing.

// tlsDomainsExtra holds admin-connected domains as "host" -> true. Swapped
// atomically so concurrent TLS handshakes never see a torn map. nil = none.
var tlsDomainsExtra atomic.Pointer[map[string]bool]

// domainMode records how TLS was configured at startup: "acme", "byocert",
// or "" (plain HTTP). domain.connect only works in ACME mode; ListenAndServeTLS
// sets it before serving.
var domainMode atomic.Pointer[string]

func setDomainMode(mode string) { domainMode.Store(&mode) }

// SetDomainMode overrides the startup TLS mode for tests: "acme", "byocert",
// or "" (plain HTTP, the default).
func (e *Engine) SetDomainMode(mode string) { setDomainMode(mode) }

// acmeHostAllowed decides whether host may receive a certificate: either it
// was in the startup --domain list (static) or added at runtime by an admin
// via domain.connect (extras).
func acmeHostAllowed(host string, allowed []string) bool {
	for _, d := range allowed {
		if strings.EqualFold(d, host) {
			return true
		}
	}
	if p := tlsDomainsExtra.Load(); p != nil {
		return (*p)[strings.ToLower(host)]
	}
	return false
}

// DomainConnection reports whether runtime domain-connect is possible and,
// when it is, which domains the admin has attached this session (sorted).
func (b *Bus) DomainConnection() (available bool, domains []string) {
	if m := domainMode.Load(); m == nil || *m != "acme" {
		return false, nil
	}
	out := []string{}
	if p := tlsDomainsExtra.Load(); p != nil {
		for d := range *p {
			out = append(out, d)
		}
	}
	sortStrings(out)
	return true, out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// normalizeDomain lowercases, strips a pasted scheme/port/path, and validates
// the result is a plausible certifiable hostname. "" = invalid.
func normalizeDomain(raw string) string {
	d := strings.TrimSpace(strings.ToLower(raw))
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimPrefix(d, "http://")
	if i := strings.IndexByte(d, '/'); i >= 0 {
		d = d[:i]
	}
	if i := strings.LastIndexByte(d, ':'); i >= 0 && !strings.Contains(d, "]") {
		d = d[:i]
	}
	if d == "" || len(d) > 253 || strings.Contains(d, "*") {
		return ""
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" || len(label) > 63 {
			return ""
		}
		for _, r := range label {
			ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-'
			if !ok {
				return ""
			}
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
	}
	if !strings.Contains(d, ".") { // bare "localhost" is not certifiable
		return ""
	}
	return d
}

// verifyDomainPointsHere checks the hostname resolves to this machine — one of
// our non-loopback interface IPs directly, or the public IP observed on the
// egress path (NAT / reverse-proxy deployments). Human-readable error on fail.
func verifyDomainPointsHere(ctx context.Context, host string) error {
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("DNS lookup failed — add an A record pointing %s at this server's IP first", host)
	}

	mine := localIPs()
	var publicIP net.IP
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			continue
		}
		for _, l := range mine {
			if ip.Equal(l) {
				return nil // resolves straight to one of our interfaces
			}
		}
	}

	publicIP = discoverPublicIP(ctx)
	if publicIP != nil {
		for _, a := range addrs {
			if ip := net.ParseIP(a); ip != nil && ip.Equal(publicIP) {
				return nil
			}
		}
	}

	shown := "somewhere else"
	if publicIP != nil {
		shown = publicIP.String()
	}
	return fmt.Errorf("%s resolves to %v but this server looks like %s — update the A record (propagation can take a few minutes) and try again", host, addrs, shown)
}

// localIPs lists non-loopback IPs bound to this machine.
func localIPs() []net.IP {
	var out []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && !ip.IsLoopback() {
				out = append(out, ip)
			}
		}
	}
	return out
}

// discoverPublicIP asks public echo endpoints over HTTPS for our apparent IP.
// Best-effort: nil when everything fails (offline etc). Callers already hold
// a timeout ctx.
func discoverPublicIP(ctx context.Context) net.IP {
	services := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://checkip.amazonaws.com",
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, s := range services {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		buf := make([]byte, 64)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		if ip := net.ParseIP(strings.TrimSpace(string(buf[:n]))); ip != nil {
			return ip
		}
	}
	return nil
}

// domainConnect implements the `domain.connect` action.
//
//	connect:    payload { domain: "shop.example.com" }
//	disconnect: payload { domain: "...", mode: "disconnect" }, or empty payload drops all runtime domains
//
// Emits nothing itself; the manifest route broadcasts the resulting state.
func (b *Bus) domainConnect(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	mode := ""
	if m := domainMode.Load(); m != nil {
		mode = *m
	}
	switch mode {
	case "byocert":
		return fmt.Errorf("domain.connect needs Let's Encrypt mode — restart spine with --domain <name> (BYO-cert mode serves fixed PEM files)")
	case "":
		return fmt.Errorf("domain.connect needs TLS enabled — start spine with --domain <your-domain> first")
	}

	disconnecting := strings.ToLower(ResolveVariables(step.Config["mode"], eventName, payload)) == "disconnect"

	raw := strings.TrimSpace(ResolveVariables("$event.payload.domain", eventName, payload))
	if disconnecting && raw == "" {
		tlsDomainsExtra.Store(nil)
		log.Printf("[domains] admin cleared all runtime-connected domains")
		return nil
	}
	if raw == "" {
		return fmt.Errorf("domain.connect requires payload 'domain'")
	}
	d := normalizeDomain(raw)
	if d == "" {
		return fmt.Errorf("invalid domain %q — use a plain hostname like shop.example.com (no wildcards, no URLs)", raw)
	}

	if !disconnecting && os.Getenv("SPINE_TRUST_DOMAIN_ON_CONNECT") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := verifyDomainPointsHere(ctx, d); err != nil {
			log.Printf("[domains] connect %s rejected: %v", d, err)
			return err
		}
	}

	// Merge into the live extras allowlist.
	cur := map[string]bool{}
	if p := tlsDomainsExtra.Load(); p != nil {
		for k, v := range *p {
			cur[k] = v
		}
	}
	if disconnecting {
		delete(cur, d)
	} else {
		cur[d] = true
	}
	if len(cur) == 0 {
		tlsDomainsExtra.Store(nil)
	} else {
		tlsDomainsExtra.Store(&cur)
	}

	if disconnecting {
		log.Printf("[domains] disconnected %s from the ACME allowlist", d)
	} else {
		log.Printf("[domains] connected %s — certificate will be issued on first visit", d)
	}
	return nil
}
