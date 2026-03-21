package middleware

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TetsWorks/Gohttp/internal/parser"
	"github.com/TetsWorks/Gohttp/internal/router"
)

// ─── Logger ───────────────────────────────────────────────────────────────────

// LoggerConfig configura o middleware de logging
type LoggerConfig struct {
	Output    *os.File
	Format    string // "combined", "common", "json"
	SkipPaths []string
}

var defaultLoggerConfig = LoggerConfig{
	Output: os.Stdout,
	Format: "combined",
}

// Logger loga cada requisição no estilo Apache Combined Log
func Logger() router.Middleware {
	return LoggerWithConfig(defaultLoggerConfig)
}

func LoggerWithConfig(cfg LoggerConfig) router.Middleware {
	logger := log.New(cfg.Output, "", 0)
	skip := make(map[string]bool)
	for _, p := range cfg.SkipPaths {
		skip[p] = true
	}

	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(w parser.ResponseWriter, r *parser.Request) {
			if skip[r.Path] {
				next(w, r)
				return
			}

			start := time.Now()
			next(w, r)
			elapsed := time.Since(start)

			status := w.Status()
			if status == 0 {
				status = 200
			}

			statusColor := colorStatus(status)
			methodColor := colorMethod(r.Method)

			switch cfg.Format {
			case "json":
				logger.Printf(`{"time":"%s","method":"%s","path":"%s","status":%d,"latency":"%s","ip":"%s"}`,
					time.Now().Format(time.RFC3339),
					r.Method, r.Path, status,
					elapsed.Round(time.Microsecond),
					r.RemoteAddr,
				)
			case "common":
				logger.Printf(`%s - - [%s] "%s %s %s" %d -`,
					r.RemoteAddr,
					time.Now().Format("02/Jan/2006:15:04:05 -0700"),
					r.Method, r.Path, r.Version, status,
				)
			default: // combined
				logger.Printf("%s %s%-7s\033[0m %s\033[0m %s%-3d\033[0m %s",
					r.RemoteAddr,
					methodColor, r.Method,
					r.Path,
					statusColor, status,
					elapsed.Round(time.Microsecond),
				)
			}
		}
	}
}

func colorStatus(code int) string {
	switch {
	case code >= 500:
		return "\033[31m" // vermelho
	case code >= 400:
		return "\033[33m" // amarelo
	case code >= 300:
		return "\033[36m" // ciano
	case code >= 200:
		return "\033[32m" // verde
	default:
		return "\033[0m"
	}
}

func colorMethod(m string) string {
	switch m {
	case "GET":
		return "\033[34m"
	case "POST":
		return "\033[32m"
	case "PUT":
		return "\033[33m"
	case "DELETE":
		return "\033[31m"
	case "PATCH":
		return "\033[35m"
	default:
		return "\033[0m"
	}
}

// ─── Recover ──────────────────────────────────────────────────────────────────

// Recover captura panics e retorna 500
func Recover() router.Middleware {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(w parser.ResponseWriter, r *parser.Request) {
			defer func() {
				if err := recover(); err != nil {
					log.Printf("PANIC: %v\n", err)
					if !w.Written() {
						w.Header().Set("Content-Type", "text/plain")
						w.WriteHeader(500)
						w.WriteString(fmt.Sprintf("500 Internal Server Error\n"))
					}
				}
			}()
			next(w, r)
		}
	}
}

// ─── CORS ─────────────────────────────────────────────────────────────────────

// CORSConfig configura o middleware CORS
type CORSConfig struct {
	AllowOrigins     []string
	AllowMethods     []string
	AllowHeaders     []string
	ExposeHeaders    []string
	AllowCredentials bool
	MaxAge           int // segundos
}

var DefaultCORSConfig = CORSConfig{
	AllowOrigins: []string{"*"},
	AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"},
	AllowHeaders: []string{"Origin", "Content-Type", "Accept", "Authorization"},
	MaxAge:       86400,
}

func CORS() router.Middleware {
	return CORSWithConfig(DefaultCORSConfig)
}

func CORSWithConfig(cfg CORSConfig) router.Middleware {
	allowMethods := strings.Join(cfg.AllowMethods, ", ")
	allowHeaders := strings.Join(cfg.AllowHeaders, ", ")
	exposeHeaders := strings.Join(cfg.ExposeHeaders, ", ")
	maxAge := strconv.Itoa(cfg.MaxAge)

	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(w parser.ResponseWriter, r *parser.Request) {
			origin := r.Headers.Get("Origin")
			if origin == "" {
				next(w, r)
				return
			}

			// Checa origem permitida
			allowed := false
			for _, o := range cfg.AllowOrigins {
				if o == "*" || o == origin {
					allowed = true
					w.Header().Set("Access-Control-Allow-Origin", o)
					break
				}
			}

			if !allowed {
				w.WriteHeader(403)
				w.WriteString("Origin not allowed")
				return
			}

			w.Header().Set("Access-Control-Allow-Methods", allowMethods)
			w.Header().Set("Access-Control-Allow-Headers", allowHeaders)
			if exposeHeaders != "" {
				w.Header().Set("Access-Control-Expose-Headers", exposeHeaders)
			}
			if cfg.AllowCredentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			if cfg.MaxAge > 0 {
				w.Header().Set("Access-Control-Max-Age", maxAge)
			}

			// Preflight
			if r.Method == "OPTIONS" {
				w.Header().Set("Content-Length", "0")
				w.WriteHeader(204)
				return
			}

			next(w, r)
		}
	}
}

// ─── Rate Limiter ─────────────────────────────────────────────────────────────

// RateLimiter usa token bucket por IP
type bucket struct {
	tokens    float64
	lastRefil time.Time
}

// RateLimiterConfig configura o rate limiter
type RateLimiterConfig struct {
	Rate       float64       // requests por segundo
	Burst      int           // burst máximo
	KeyFunc    func(*parser.Request) string // extrai chave (padrão: IP)
	OnLimited  router.HandlerFunc
}

var DefaultRateLimiterConfig = RateLimiterConfig{
	Rate:  10,
	Burst: 20,
	KeyFunc: func(r *parser.Request) string {
		ip := r.RemoteAddr
		if idx := strings.LastIndex(ip, ":"); idx >= 0 {
			ip = ip[:idx]
		}
		return ip
	},
	OnLimited: func(w parser.ResponseWriter, r *parser.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(429)
		w.WriteString("429 Too Many Requests\n")
	},
}

type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	cfg     RateLimiterConfig
	ticker  *time.Ticker
}

func NewRateLimiter(cfg RateLimiterConfig) *RateLimiter {
	rl := &RateLimiter{
		buckets: make(map[string]*bucket),
		cfg:     cfg,
		ticker:  time.NewTicker(time.Minute),
	}
	// Limpa buckets antigos periodicamente
	go func() {
		for range rl.ticker.C {
			rl.cleanup()
		}
	}()
	return rl
}

func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(rl.cfg.Burst), lastRefil: now}
		rl.buckets[key] = b
	}

	// Refil proporcional ao tempo passado
	elapsed := now.Sub(b.lastRefil).Seconds()
	b.tokens += elapsed * rl.cfg.Rate
	if b.tokens > float64(rl.cfg.Burst) {
		b.tokens = float64(rl.cfg.Burst)
	}
	b.lastRefil = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-5 * time.Minute)
	for key, b := range rl.buckets {
		if b.lastRefil.Before(cutoff) {
			delete(rl.buckets, key)
		}
	}
}

func RateLimit(cfg RateLimiterConfig) router.Middleware {
	limiter := NewRateLimiter(cfg)
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(w parser.ResponseWriter, r *parser.Request) {
			key := cfg.KeyFunc(r)
			if !limiter.Allow(key) {
				cfg.OnLimited(w, r)
				return
			}
			next(w, r)
		}
	}
}

// ─── Auth ─────────────────────────────────────────────────────────────────────

// BasicAuth middleware de autenticação Basic
func BasicAuth(realm string, users map[string]string) router.Middleware {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(w parser.ResponseWriter, r *parser.Request) {
			auth := r.Headers.Get("Authorization")
			if !strings.HasPrefix(auth, "Basic ") {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm="%s"`, realm))
				w.WriteHeader(401)
				w.WriteString("401 Unauthorized\n")
				return
			}

			payload, err := base64.StdEncoding.DecodeString(auth[6:])
			if err != nil {
				w.WriteHeader(401)
				return
			}

			parts := strings.SplitN(string(payload), ":", 2)
			if len(parts) != 2 {
				w.WriteHeader(401)
				return
			}

			expectedPass, ok := users[parts[0]]
			if !ok || subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expectedPass)) != 1 {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm="%s"`, realm))
				w.WriteHeader(401)
				w.WriteString("401 Unauthorized\n")
				return
			}

			next(w, r)
		}
	}
}

// BearerAuth middleware de autenticação Bearer Token
func BearerAuth(validate func(token string) bool) router.Middleware {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(w parser.ResponseWriter, r *parser.Request) {
			auth := r.Headers.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				w.WriteHeader(401)
				w.WriteString("401 Unauthorized\n")
				return
			}
			token := auth[7:]
			if !validate(token) {
				w.WriteHeader(401)
				w.WriteString("401 Unauthorized\n")
				return
			}
			next(w, r)
		}
	}
}

// ─── Timeout ──────────────────────────────────────────────────────────────────

// RequestID adiciona X-Request-ID único a cada request
func RequestID() router.Middleware {
	var counter uint64
	var mu sync.Mutex
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(w parser.ResponseWriter, r *parser.Request) {
			mu.Lock()
			counter++
			id := fmt.Sprintf("%d-%d", time.Now().UnixNano(), counter)
			mu.Unlock()
			w.Header().Set("X-Request-ID", id)
			next(w, r)
		}
	}
}

// SecurityHeaders adiciona headers de segurança padrão
func SecurityHeaders() router.Middleware {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(w parser.ResponseWriter, r *parser.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("X-XSS-Protection", "1; mode=block")
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
			next(w, r)
		}
	}
}

// Cache define headers de cache
func Cache(maxAge int) router.Middleware {
	val := fmt.Sprintf("public, max-age=%d", maxAge)
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(w parser.ResponseWriter, r *parser.Request) {
			w.Header().Set("Cache-Control", val)
			next(w, r)
		}
	}
}

// NoCache desativa cache
func NoCache() router.Middleware {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(w parser.ResponseWriter, r *parser.Request) {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			next(w, r)
		}
	}
}
