package webhook

import (
	"fmt"
	"net/http"
	"runtime/debug"
	"time"

	"go.uber.org/zap"
)

// ResponseWriterWrapper is a minimal wrapper for http.ResponseWriter that allows the
// written HTTP status code to be captured for logging.
type ResponseWriterWrapper struct {
	http.ResponseWriter
	status          int
	headerIsWritten bool
}

// WriteHeader updates the wrapper's status code if header is not written.
func (rw *ResponseWriterWrapper) WriteHeader(code int) {
	if rw.headerIsWritten {
		return
	}

	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
	rw.headerIsWritten = true
}

// loggingMiddleware returns a function to log the incoming HTTP request & its duration.
func loggingMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			// Capture the panic and log it
			defer func() {
				if err := recover(); err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					zap.L().Error("Captured a panic from webhook service handler",
						zap.String("error", fmt.Sprintf("%v", err)),
						zap.ByteString("trace", debug.Stack()),
					)
				}
			}()

			start := time.Now()
			wrapped := &ResponseWriterWrapper{ResponseWriter: w}
			next.ServeHTTP(wrapped, r)
			zap.L().Info("Server responded to an incoming HTTP request",
				zap.Int("status", wrapped.status),
				zap.String("method", r.Method),
				zap.String("path", r.URL.EscapedPath()),
				zap.String("duration", time.Since(start).String()),
			)
		}

		return http.HandlerFunc(fn)
	}
}
