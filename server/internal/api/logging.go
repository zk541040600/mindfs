package api

import (
	"bufio"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *loggingResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *loggingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response does not implement http.Hijacker")
	}
	return hijacker.Hijack()
}

func (w *loggingResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *loggingResponseWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}

func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &loggingResponseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		next.ServeHTTP(lw, r)

		log.Printf("[http] %s %s status=%d duration=%s remote=%s",
			r.Method,
			r.URL.RequestURI(),
			lw.statusCode,
			time.Since(start).Round(time.Millisecond),
			r.RemoteAddr,
		)
	})
}

// CORSMiddleware adds CORS and Private Network Access headers to all HTTP responses.
// This enables the Capacitor App WebView (and browser) to make cross-origin requests
// from origins like https://localhost or http://localhost to the MindFS API server.
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		allowedOrigin := allowedCORSOrigin(origin, r.Host)
		if origin != "" && allowedOrigin == "" {
			http.Error(w, "cors origin not allowed", http.StatusForbidden)
			return
		}
		if allowedOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
			// Private Network Access header: allows WebView (secure context) to reach local network resources.
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
		w.Header().Set("Access-Control-Allow-Headers", strings.Join([]string{
			"Authorization",
			"Content-Type",
			"X-Requested-With",
			e2eeHeaderName,
			clientIDHeaderName,
			e2eeProofHeaderName,
			e2eeTSHeaderName,
		}, ", "))

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func allowedCORSOrigin(origin, requestHost string) string {
	origin = strings.TrimSpace(origin)
	if origin == "" || strings.EqualFold(origin, "null") {
		return ""
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		if strings.EqualFold(parsed.Host, strings.TrimSpace(requestHost)) || isLoopbackOrMindFSHost(host) {
			return origin
		}
	case "capacitor", "ionic":
		if host == "localhost" || host == "mindfs.local" {
			return origin
		}
	}
	return ""
}

func isLoopbackOrMindFSHost(host string) bool {
	if host == "localhost" || host == "mindfs.local" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
