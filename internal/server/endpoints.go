package server

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/dockerx"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// endpointPool owns the Docker Engine API clients for directly-polled remote
// hosts (servers of kind "direct"). Clients are keyed by server id and rebuilt
// whenever that endpoint's connection settings change. The pool is safe for
// concurrent use.
type endpointPool struct {
	mu      sync.Mutex
	entries map[string]*endpointEntry
}

type endpointEntry struct {
	// fingerprint captures the exact settings the client was built from.
	fingerprint string
	client      *dockerx.Client
	err         error
}

func newEndpointPool() *endpointPool {
	return &endpointPool{entries: map[string]*endpointEntry{}}
}

// dockerConfigFor maps a stored direct server onto a Docker client config. TLS
// material is always a mounted file path supplied by the operator.
func dockerConfigFor(srv store.Server) config.DockerConfig {
	return config.DockerConfig{
		Host:    srv.DockerHost,
		TLS:     srv.TLSEnabled(),
		TLSCA:   srv.TLSCA,
		TLSCert: srv.TLSCert,
		TLSKey:  srv.TLSKey,
	}
}

func endpointFingerprint(srv store.Server) string {
	return strings.Join([]string{srv.DockerHost, srv.TLSCA, srv.TLSCert, srv.TLSKey}, "\x00")
}

// get returns the Docker client for a direct server, building and caching it on
// first use. The returned error is sticky for a given fingerprint so a broken
// endpoint is not rebuilt on every poll.
func (p *endpointPool) get(srv store.Server) (*dockerx.Client, error) {
	fp := endpointFingerprint(srv)
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.entries[srv.ID]; ok && e.fingerprint == fp {
		return e.client, e.err
	}
	if old, ok := p.entries[srv.ID]; ok && old.client != nil {
		_ = old.client.Close()
	}
	e := &endpointEntry{fingerprint: fp}
	e.client, e.err = dockerx.New(dockerConfigFor(srv))
	p.entries[srv.ID] = e
	return e.client, e.err
}

// forget drops a cached client, e.g. when a server is deleted or reclassified.
func (p *endpointPool) forget(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.entries[id]; ok {
		if e.client != nil {
			_ = e.client.Close()
		}
		delete(p.entries, id)
	}
}

// closeAll releases every pooled client on shutdown.
func (p *endpointPool) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, e := range p.entries {
		if e.client != nil {
			_ = e.client.Close()
		}
		delete(p.entries, id)
	}
}

// ---------------------------------------------------------------------------
// Endpoint validation
// ---------------------------------------------------------------------------

// endpointSpec is a validated direct-endpoint definition.
type endpointSpec struct {
	Host    string
	TLSCA   string
	TLSCert string
	TLSKey  string
}

// TLSEnabled reports whether the spec carries TLS material.
func (e endpointSpec) TLSEnabled() bool {
	return e.TLSCA != "" || e.TLSCert != "" || e.TLSKey != ""
}

// normalizeDockerHost canonicalises a user-supplied Docker endpoint URL.
//
// A bare host:port is treated as tcp://. Plaintext transport is only accepted
// for loopback hosts and unix sockets: sending the Docker API over an
// unencrypted network link would hand full control of the host to anyone able
// to observe the traffic.
func normalizeDockerHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("docker host is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "tcp://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid docker host: %w", err)
	}
	switch u.Scheme {
	case "unix":
		return "unix://" + u.Path, nil
	case "tcp", "http", "https":
		if u.Host == "" {
			return "", errors.New("docker host must include a hostname")
		}
		host, port, splitErr := net.SplitHostPort(u.Host)
		if splitErr != nil {
			host = u.Host
			port = ""
		}
		if host == "" {
			return "", errors.New("docker host must include a hostname")
		}
		if port == "" {
			// 2376 is the conventional TLS port, 2375 the plaintext one.
			if u.Scheme == "https" {
				port = "2376"
			} else {
				port = "2375"
			}
		}
		return "tcp://" + net.JoinHostPort(host, port), nil
	case "ssh":
		return "", errors.New("ssh:// endpoints are not supported; use a TLS-protected tcp:// endpoint")
	default:
		return "", fmt.Errorf("unsupported docker host scheme %q", u.Scheme)
	}
}

// hostIsLoopback reports whether a docker host URL points at this machine.
func hostIsLoopback(host string) bool {
	u, err := url.Parse(host)
	if err != nil {
		return false
	}
	if u.Scheme == "unix" {
		return true
	}
	h := u.Hostname()
	if h == "" {
		return false
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// validateEndpoint normalises and checks a direct-endpoint definition. It
// rejects plaintext remote endpoints and unreadable TLS material, so a
// misconfiguration is reported at save time rather than silently at poll time.
func validateEndpoint(rawHost, ca, cert, key string) (endpointSpec, error) {
	host, err := normalizeDockerHost(rawHost)
	if err != nil {
		return endpointSpec{}, err
	}
	spec := endpointSpec{
		Host:    host,
		TLSCA:   strings.TrimSpace(ca),
		TLSCert: strings.TrimSpace(cert),
		TLSKey:  strings.TrimSpace(key),
	}
	if !hostIsLoopback(host) && !spec.TLSEnabled() {
		return endpointSpec{}, fmt.Errorf(
			"refusing to use the unencrypted Docker API at %s: remote endpoints require TLS "+
				"(provide a CA, and a client certificate + key for mutual TLS)", host)
	}
	if spec.TLSCA == "" && (spec.TLSCert != "" || spec.TLSKey != "") {
		return endpointSpec{}, errors.New("a TLS CA certificate is required when using client certificates")
	}
	if (spec.TLSCert == "") != (spec.TLSKey == "") {
		return endpointSpec{}, errors.New("client certificate and key must be provided together")
	}
	for label, path := range map[string]string{
		"CA certificate":     spec.TLSCA,
		"client certificate": spec.TLSCert,
		"client key":         spec.TLSKey,
	} {
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return endpointSpec{}, fmt.Errorf("%s not readable at %s: %w", label, path, err)
		}
		if info.IsDir() {
			return endpointSpec{}, fmt.Errorf("%s path %s is a directory, expected a file", label, path)
		}
	}
	return spec, nil
}
