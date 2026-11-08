// Package httpapi 将日历契约映射为 HTTP，并统一使用 KCQ V1 envelope。
package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"kcq-calendar-connector/internal/calendar"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// 路由和响应头集中定义，业务处理不拼装路径。
const (
	HealthPath      = "/healthz"
	MarketsPath     = "/api/v1/calendars"
	DaysPattern     = MarketsPath + "/{exchange}/days"
	NextPattern     = MarketsPath + "/{exchange}/next"
	V1Path          = "/api/v1/market-data/trading-calendar"
	RequestIDHeader = "X-Request-ID"
	maxBodyBytes    = 64 << 10
)

// envelope 保持已有成功响应字段形状。
type envelope struct {
	Data      any    `json:"data"`
	RequestID string `json:"requestId"`
}

// apiError 与 KCQ market-data V1 错误对象一致。
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// errorEnvelope 不向调用方泄露底层数据库路径或 SQL。
type errorEnvelope struct {
	Error     apiError `json:"error"`
	RequestID string   `json:"requestId"`
}

// Server 将传输层所需的依赖显式装配。
type Server struct {
	service *calendar.Service
	logger  *slog.Logger
	origins map[string]bool
}

// New 创建可由 httptest 或生产 HTTP server 使用的处理器。
func New(service *calendar.Service, logger *slog.Logger, allowedOrigins []string) http.Handler {
	s := &Server{service: service, logger: logger, origins: make(map[string]bool)}
	for _, origin := range allowedOrigins {
		if value := strings.TrimSpace(origin); value != "" {
			s.origins[value] = true
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc(HealthPath, s.health)
	mux.HandleFunc(MarketsPath, s.markets)
	mux.HandleFunc(DaysPattern, s.days)
	mux.HandleFunc(NextPattern, s.next)
	mux.HandleFunc(V1Path, s.future)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, http.StatusNotFound, calendar.CodeInvalidRequest, "route not found")
	})
	return s.middleware(mux)
}

// middleware 生成请求标识、配置 CORS，并将意外 panic 转换为明确的服务端错误。
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			s.fail(w, http.StatusInternalServerError, calendar.CodeInternal, "request id unavailable")
			return
		}
		w.Header().Set(RequestIDHeader, hex.EncodeToString(id[:]))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Add("Vary", "Origin")
			if !s.origins[origin] && !s.origins["*"] {
				s.fail(w, http.StatusForbidden, calendar.CodeInvalidRequest, "origin is not allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Expose-Headers", RequestIDHeader)
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		defer func() {
			if value := recover(); value != nil {
				s.logger.Error("request panic", "requestId", w.Header().Get(RequestIDHeader), "panic", value)
				s.fail(w, http.StatusInternalServerError, calendar.CodeInternal, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// method 为每个路由统一返回结构化的 405 响应。
func (s *Server) method(w http.ResponseWriter, r *http.Request, expected string) bool {
	if r.Method == expected {
		return true
	}
	w.Header().Set("Allow", expected)
	s.fail(w, http.StatusMethodNotAllowed, calendar.CodeInvalidRequest, "method not allowed")
	return false
}

// write 返回带请求标识的成功响应。
func (s *Server) write(w http.ResponseWriter, data any) {
	_ = json.NewEncoder(w).Encode(envelope{Data: data, RequestID: w.Header().Get(RequestIDHeader)})
}

// fail 返回带请求标识的错误响应。
func (s *Server) fail(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: apiError{Code: code, Message: message}, RequestID: w.Header().Get(RequestIDHeader)})
}

// respondError 只向调用方暴露领域错误，其余错误写入服务日志。
func (s *Server) respondError(w http.ResponseWriter, err error) {
	var domain *calendar.Error
	if errors.As(err, &domain) {
		status := http.StatusBadRequest
		if domain.Code == calendar.CodeUnsupported {
			status = http.StatusUnprocessableEntity
		}
		s.fail(w, status, domain.Code, domain.Message)
		return
	}
	s.logger.Error("calendar query failed", "requestId", w.Header().Get(RequestIDHeader), "error", err)
	s.fail(w, http.StatusInternalServerError, calendar.CodeInternal, "calendar database query failed")
}

// health 实际检查数据库连接，避免空壳进程被误判为健康。
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if !s.method(w, r, http.MethodGet) {
		return
	}
	if err := s.service.Ping(r.Context()); err != nil {
		s.fail(w, http.StatusServiceUnavailable, calendar.CodeInternal, "calendar database unavailable")
		return
	}
	s.write(w, map[string]string{"status": "ok"})
}

// markets 返回真实导入的市场及数据版本。
func (s *Server) markets(w http.ResponseWriter, r *http.Request) {
	if !s.method(w, r, http.MethodGet) {
		return
	}
	result, err := s.service.Markets(r.Context())
	if err != nil {
		s.respondError(w, err)
		return
	}
	s.write(w, result)
}

// days 返回完整日期区间，openOnly 只接受明确的布尔值。
func (s *Server) days(w http.ResponseWriter, r *http.Request) {
	if !s.method(w, r, http.MethodGet) {
		return
	}
	q := r.URL.Query()
	openOnly := false
	if value := q.Get("openOnly"); value != "" {
		if value != "true" && value != "false" {
			s.respondError(w, calendar.Invalid("openOnly must be true or false"))
			return
		}
		openOnly = value == "true"
	}
	result, err := s.service.Days(r.Context(), r.PathValue("exchange"), q.Get("start"), q.Get("end"), openOnly)
	if err != nil {
		s.respondError(w, err)
		return
	}
	s.write(w, result)
}

// next 要求显式数量和排他日期锚点。
func (s *Server) next(w http.ResponseWriter, r *http.Request) {
	if !s.method(w, r, http.MethodGet) {
		return
	}
	q := r.URL.Query()
	count, err := strconv.Atoi(q.Get("count"))
	if err != nil {
		s.respondError(w, calendar.Invalid("count must be an integer"))
		return
	}
	result, err := s.service.Next(r.Context(), r.PathValue("exchange"), q.Get("after"), count)
	if err != nil {
		s.respondError(w, err)
		return
	}
	s.write(w, result)
}

// future 消费 KCQ V1 请求，限制请求体并拒绝多个拼接 JSON 对象。
func (s *Server) future(w http.ResponseWriter, r *http.Request) {
	if !s.method(w, r, http.MethodPost) {
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	var req calendar.TradingCalendarRequest
	if err := decoder.Decode(&req); err != nil {
		s.respondError(w, calendar.Invalid("invalid JSON request body"))
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		s.respondError(w, calendar.Invalid("request body must contain one JSON object"))
		return
	}
	result, err := s.service.Future(r.Context(), req)
	if err != nil {
		s.respondError(w, err)
		return
	}
	s.write(w, result)
}
