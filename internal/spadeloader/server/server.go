package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mblsha/spadeforge/internal/spadeloader/config"
	"github.com/mblsha/spadeforge/internal/spadeloader/job"
	"github.com/mblsha/spadeforge/internal/spadeloader/queue"
)

var boardPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

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
	a.mux.Handle("GET /v1/jobs/{id}/log", a.guard(http.HandlerFunc(a.handleGetLog)))
	a.mux.Handle("GET /v1/jobs/{id}/tail", a.guard(http.HandlerFunc(a.handleGetTail)))
	a.mux.Handle("GET /v1/jobs/{id}/events", a.guard(http.HandlerFunc(a.handleGetEvents)))
	a.mux.Handle("GET /v1/designs/recent", a.guard(http.HandlerFunc(a.handleGetRecentDesigns)))
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
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	board := strings.TrimSpace(r.FormValue("board"))
	designName := strings.TrimSpace(r.FormValue("design_name"))

	if err := validateBoard(board); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !a.cfg.BoardAllowed(board) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "board is not allowed by server policy"})
		return
	}
	if err := validateDesignName(designName); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	file, header, err := r.FormFile("bitstream")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing bitstream file field"})
		return
	}
	defer file.Close()

	bitstreamName := strings.TrimSpace(header.Filename)
	if err := validateBitstreamName(bitstreamName); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	rec, err := a.manager.Submit(r.Context(), queue.SubmitRequest{
		Board:         board,
		DesignName:    designName,
		BitstreamName: filepath.Base(bitstreamName),
		Bitstream:     file,
	})
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

func (a *API) handleGetTail(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if _, ok := a.manager.Get(jobID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	lines := 200
	if rawLines := strings.TrimSpace(r.URL.Query().Get("lines")); rawLines != "" {
		n, err := strconv.Atoi(rawLines)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid lines query value"})
			return
		}
		lines = n
	}
	raw, err := a.manager.ReadConsoleTail(jobID, lines)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (a *API) handleGetEvents(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	since := int64(0)
	if rawSince := strings.TrimSpace(r.URL.Query().Get("since")); rawSince != "" {
		n, err := strconv.ParseInt(rawSince, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid since query value"})
			return
		}
		since = n
	}

	backlog, ch, cancel, ok := a.manager.SubscribeEvents(jobID, since)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	defer cancel()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	for _, ev := range backlog {
		if err := writeSSEEvent(w, ev); err != nil {
			return
		}
		flusher.Flush()
	}
	if ch == nil {
		return
	}

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			_, _ = w.Write([]byte(": keepalive\n\n"))
			flusher.Flush()
			if rec, ok := a.manager.Get(jobID); !ok || rec.Terminal() {
				return
			}
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSEEvent(w, ev); err != nil {
				return
			}
			flusher.Flush()
			if ev.Terminal() {
				return
			}
		}
	}
}

func (a *API) handleGetRecentDesigns(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid limit query value"})
			return
		}
		limit = n
	}
	if limit > a.cfg.HistoryLimit {
		limit = a.cfg.HistoryLimit
	}

	items, err := a.manager.ListRecentDesigns(limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func writeSSEEvent(w http.ResponseWriter, ev job.Event) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, string(raw)); err != nil {
		return err
	}
	return nil
}

func validateBoard(board string) error {
	if !boardPattern.MatchString(board) {
		return fmt.Errorf("invalid board; expected pattern %s", boardPattern.String())
	}
	return nil
}

func validateDesignName(designName string) error {
	n := len(strings.TrimSpace(designName))
	if n < 1 || n > 128 {
		return errors.New("design_name must be between 1 and 128 characters")
	}
	return nil
}

func validateBitstreamName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("bitstream file name is required")
	}
	if strings.ToLower(filepath.Ext(name)) != ".bit" {
		return errors.New("bitstream file must have .bit extension")
	}
	return nil
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
