package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/mblsha/spadeforge/internal/config"
	"github.com/mblsha/spadeforge/internal/queue"
)

type API struct {
	cfg     config.Config
	manager *queue.Manager
	mux     *http.ServeMux
}

func New(cfg config.Config, manager *queue.Manager) *API {
	a := &API{cfg: cfg, manager: manager, mux: http.NewServeMux()}
	a.routes()
	return a
}

func (a *API) Handler() http.Handler {
	return a.mux
}

func (a *API) routes() {
	a.mux.HandleFunc("GET /healthz", a.handleHealthz)
	a.mux.Handle("POST /v1/jobs", a.guard(http.HandlerFunc(a.handleSubmitJob)))
	a.mux.Handle("GET /v1/jobs/{id}", a.guard(http.HandlerFunc(a.handleGetJob)))
	a.mux.Handle("GET /v1/jobs/{id}/artifacts", a.guard(http.HandlerFunc(a.handleGetArtifacts)))
	a.mux.Handle("GET /v1/jobs/{id}/log", a.guard(http.HandlerFunc(a.handleGetLog)))
}

func (a *API) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := a.checkAllowlist(r); err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		if err := a.checkToken(r); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) checkToken(r *http.Request) error {
	if strings.TrimSpace(a.cfg.Token) == "" {
		return nil
	}
	if strings.TrimSpace(r.Header.Get(a.cfg.AuthHeader)) != a.cfg.Token {
		return errors.New("invalid token")
	}
	return nil
}

func (a *API) checkAllowlist(r *http.Request) error {
	if !a.cfg.AllowlistEnabled() {
		return nil
	}
	ip, err := remoteIP(r.RemoteAddr)
	if err != nil {
		return err
	}
	for _, allow := range a.cfg.Allowlist {
		if allowEntryMatches(allow, ip) {
			return nil
		}
	}
	return fmt.Errorf("remote ip %s is not allowed", ip.String())
}

func (a *API) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) handleSubmitJob(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxUploadBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": err.Error()})
		return
	}
	file, _, err := r.FormFile("bundle")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing bundle file field"})
		return
	}
	defer file.Close()

	rec, err := a.manager.Submit(r.Context(), file)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"job_id": rec.ID,
		"state":  string(rec.State),
	})
}

func (a *API) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	rec, ok := a.manager.Get(jobID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (a *API) handleGetArtifacts(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if _, ok := a.manager.Get(jobID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}

	var payload bytes.Buffer
	if err := a.manager.DownloadArtifacts(jobID, &payload); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", jobID+"-artifacts.zip"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload.Bytes())
}

func (a *API) handleGetLog(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if _, ok := a.manager.Get(jobID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	raw, err := a.manager.ReadConsoleLog(jobID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func remoteIP(remoteAddr string) (net.IP, error) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return nil, fmt.Errorf("parse remote addr: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("invalid remote ip: %s", host)
	}
	return ip, nil
}

func allowEntryMatches(entry string, ip net.IP) bool {
	if strings.Contains(entry, "/") {
		_, cidr, err := net.ParseCIDR(entry)
		if err != nil {
			return false
		}
		return cidr.Contains(ip)
	}
	allowed := net.ParseIP(entry)
	if allowed == nil {
		return false
	}
	return allowed.Equal(ip)
}
