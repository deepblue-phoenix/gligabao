package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ivan/gligabao/internal/db"
)

var (
	serveAddr   string
	serveDBPath string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP server",
	RunE: func(cmd *cobra.Command, args []string) error {
		database, err := db.Open(serveDBPath)
		if err != nil {
			return err
		}
		defer database.Close()

		mux := http.NewServeMux()
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "gligabao is running")
		})
		mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
			if err := database.PingContext(r.Context()); err != nil {
				http.Error(w, "db unavailable", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintln(w, "ok")
		})

		srv := &http.Server{Addr: serveAddr, Handler: mux}

		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		errCh := make(chan error, 1)
		go func() {
			log.Printf("listening on %s (db: %s)", serveAddr, serveDBPath)
			errCh <- srv.ListenAndServe()
		}()

		select {
		case err := <-errCh:
			if !errors.Is(err, http.ErrServerClosed) {
				return err
			}
		case <-ctx.Done():
			log.Println("shutting down")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return srv.Shutdown(shutdownCtx)
		}
		return nil
	},
}

func init() {
	serveCmd.Flags().StringVar(&serveAddr, "addr", ":8080", "HTTP listen address")
	serveCmd.Flags().StringVar(&serveDBPath, "db", "bin/gligabao.db", "SQLite database file path")
	rootCmd.AddCommand(serveCmd)
}
