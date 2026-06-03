package httpapi

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

func accessLog(writer io.Writer, trustedProxies []netip.Prefix, next http.Handler) http.Handler {
	if writer == nil {
		return next
	}
	return &accessLogHandler{
		writer:         writer,
		trustedProxies: trustedProxies,
		next:           next,
	}
}

type accessLogHandler struct {
	writer         io.Writer
	trustedProxies []netip.Prefix
	next           http.Handler
	mu             sync.Mutex
}

func (handler *accessLogHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	recorder := &accessLogResponseWriter{ResponseWriter: w}

	defer func() {
		recovered := recover()
		status := recorder.status
		if recovered != nil && status == 0 {
			status = http.StatusInternalServerError
		}
		if status == 0 {
			status = http.StatusOK
		}
		path := requestPath(r)
		if !skipAccessLog(path) {
			handler.write(accessLogEntry{
				Timestamp:  start.UTC().Format(time.RFC3339Nano),
				Method:     r.Method,
				Path:       path,
				Status:     status,
				Bytes:      recorder.bytes,
				DurationMs: time.Since(start).Milliseconds(),
				RemoteIP:   requestRemoteIP(r, handler.trustedProxies),
				Scheme:     requestScheme(r, handler.trustedProxies),
				Host:       r.Host,
				UserAgent:  r.UserAgent(),
			})
		}
		if recovered != nil {
			panic(recovered)
		}
	}()

	handler.next.ServeHTTP(recorder, r)
}

func (handler *accessLogHandler) write(entry accessLogEntry) {
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	_, _ = handler.writer.Write(append(line, '\n'))
}

type accessLogEntry struct {
	Timestamp  string `json:"timestamp"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	Bytes      int    `json:"bytes"`
	DurationMs int64  `json:"durationMs"`
	RemoteIP   string `json:"remoteIp"`
	Scheme     string `json:"scheme"`
	Host       string `json:"host"`
	UserAgent  string `json:"userAgent,omitempty"`
}

type accessLogResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (w *accessLogResponseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.status = status
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *accessLogResponseWriter) Write(body []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}

func (w *accessLogResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		if !w.wrote {
			w.WriteHeader(http.StatusOK)
		}
		flusher.Flush()
	}
}

func (w *accessLogResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func requestPath(r *http.Request) string {
	path := r.URL.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}

func skipAccessLog(path string) bool {
	return path == "/" || path == "/healthz"
}

func requestRemoteIP(r *http.Request, trustedProxies []netip.Prefix) string {
	remoteAddr, remoteAddrOK := peerAddr(r.RemoteAddr)
	if remoteAddrOK && isTrustedProxy(remoteAddr, trustedProxies) {
		if forwarded, ok := firstForwardedAddr(r.Header.Get("X-Forwarded-For")); ok {
			return forwarded.String()
		}
		if realIP, ok := headerAddr(r.Header.Get("X-Real-IP")); ok {
			return realIP.String()
		}
	}
	if remoteAddrOK {
		return remoteAddr.String()
	}
	return r.RemoteAddr
}

func requestScheme(r *http.Request, trustedProxies []netip.Prefix) string {
	if remoteAddr, ok := peerAddr(r.RemoteAddr); ok && isTrustedProxy(remoteAddr, trustedProxies) {
		if proto := firstHeaderValue(r.Header.Get("X-Forwarded-Proto")); proto == "http" || proto == "https" {
			return proto
		}
	}
	if r.URL.Scheme != "" {
		return r.URL.Scheme
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func peerAddr(remoteAddr string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = strings.Trim(remoteAddr, "[]")
	}
	addr, err := netip.ParseAddr(host)
	return addr, err == nil
}

func firstForwardedAddr(header string) (netip.Addr, bool) {
	for _, part := range strings.Split(header, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		return headerAddr(part)
	}
	return netip.Addr{}, false
}

func headerAddr(value string) (netip.Addr, bool) {
	value = strings.Trim(strings.TrimSpace(value), "\"")
	addr, err := netip.ParseAddr(value)
	return addr, err == nil
}

func firstHeaderValue(header string) string {
	value, _, _ := strings.Cut(header, ",")
	return strings.ToLower(strings.TrimSpace(value))
}

func isTrustedProxy(addr netip.Addr, trustedProxies []netip.Prefix) bool {
	for _, prefix := range trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
