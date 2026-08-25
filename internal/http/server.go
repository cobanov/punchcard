// Package http is the transport layer: it wires the chi mux, the huma API
// (which generates the OpenAPI 3.1 contract), operational endpoints, and
// process middleware. It performs auth, validation, and serialization only —
// business logic and authorization live in the service layer.
package http

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Server owns the HTTP listener and its graceful lifecycle.
type Server struct {
	httpServer      *http.Server
	deps            Deps
	shutdownTimeout time.Duration
}

// New builds a Server from its dependencies.
func New(d Deps) *Server {
	router, _ := BuildRouter(d)
	return &Server{
		httpServer: &http.Server{
			Addr:              d.Config.HTTPAddr,
			Handler:           router,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
		deps:            d,
		shutdownTimeout: d.Config.ShutdownTimeout,
	}
}

// Run listens and serves until ctx is cancelled, then drains connections within
// the configured shutdown timeout. It returns nil on a clean shutdown.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.deps.Logger.Info("http server listening", "addr", s.httpServer.Addr)
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.deps.Logger.Info("http server shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
		if err := s.httpServer.Shutdown(shutCtx); err != nil {
			return err
		}
		s.deps.Logger.Info("http server stopped cleanly")
		return nil
	}
}
