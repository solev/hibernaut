package egress

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/egress/swap"
)

func (s *Server) endpoint(sni string, r *http.Request) (Endpoint, bool) {
	for _, ep := range s.Endpoints {
		if ep.Handle != nil && normalizeHost(ep.Site) == sni && ep.Method == r.Method && ep.Path == r.URL.Path {
			return ep, true
		}
	}
	return Endpoint{}, false
}

func (s *Server) serveEndpoint(w http.ResponseWriter, r *http.Request, sni string, ep Endpoint, identify *connIdentity) {
	w.Header().Set("Cache-Control", "no-store")
	body, status := s.readEndpointBody(w, r)
	if status != 0 {
		http.Error(w, http.StatusText(status)+"\n", status)
		return
	}
	resp := ep.Handle(r.Context(), EndpointRequest{
		Header: r.Header.Clone(),
		Body:   body,
		Audit: func(secret string) error {
			if s.Audit == nil {
				return nil
			}
			machine, machineID := identify.get()
			err := s.Audit(audit.Entry{
				Time: s.now(), Machine: machine, MachineID: machineID, Site: sni,
				Method: r.Method, Path: r.URL.EscapedPath(), Secrets: []string{secret},
			})
			if err != nil {
				s.logf("egress audit %s %s: %v", r.Method, r.URL.EscapedPath(), err)
			}
			return err
		},
	})
	sw := s.currentSwapper()
	if sw == nil {
		s.logf("egress endpoint %s %s: can't load the secrets to scrub its answer", r.Method, r.URL.EscapedPath())
		http.Error(w, http.StatusText(http.StatusServiceUnavailable)+"\n", http.StatusServiceUnavailable)
		return
	}
	writeEndpointResponse(w, sw, resp)
}

func writeEndpointResponse(w http.ResponseWriter, sw *swap.Swapper, resp EndpointResponse) {
	header := w.Header()
	for name, values := range resp.Header {
		header[name] = slices.Clone(values)
	}
	sw.ScrubHeader(header)
	header.Del("Transfer-Encoding")
	header.Set("Cache-Control", "no-store")
	body, err := io.ReadAll(sw.ScrubBody(io.NopCloser(bytes.NewReader(resp.Body))))
	if err != nil {
		body = nil
	}
	header.Set("Content-Length", strconv.Itoa(len(body)))
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) readEndpointBody(w http.ResponseWriter, r *http.Request) ([]byte, int) {
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Now().Add(s.readTimeout())); err != nil {
		s.logf("egress endpoint %s %s: can't limit the time to read the body: %v", r.Method, r.URL.EscapedPath(), err)
		return nil, http.StatusInternalServerError
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxEndpointBody))

	_ = rc.SetReadDeadline(time.Time{})
	var tooLarge *http.MaxBytesError
	var netErr net.Error
	switch {
	case err == nil:
		return body, 0
	case errors.As(err, &tooLarge):
		return nil, http.StatusRequestEntityTooLarge
	case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return nil, http.StatusRequestTimeout
	}
	return nil, http.StatusBadRequest
}

func (s *Server) readTimeout() time.Duration {
	if s.endpointReadTimeout > 0 {
		return s.endpointReadTimeout
	}
	return EndpointReadTimeout
}

func (s *Server) rejected(sw *swap.Swapper, swapped []string) {
	if s.Rejected == nil || sw == nil {
		return
	}
	for _, name := range swapped {
		s.Rejected(name, sw.Fingerprint(name))
	}
}
