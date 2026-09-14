package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/psimaker/vaultsync/hub/pake"
)

func pakePasswordScalar(code []byte) []byte { return pake.PasswordScalar(code) }

// serveHTTP runs an http.Server until ctx is done, with the conservative
// timeouts a LAN-facing service should have.
func serveHTTP(ctx context.Context, addr string, h http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
