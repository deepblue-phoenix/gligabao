package server

import (
	"context"
	"errors"
	"io"

	log "github.com/sirupsen/logrus"

	"github.com/deepblue-phoenix/gligabao/internal/sys/ml_sidecar"
)

// startMLSidecar makes sure a local ML sidecar is running before the server connects to it, for training mode: if one already answers
// at the URL nothing is done; otherwise the ML sidecar is launched as a child process, stopped with ctx. It never fails the startup:
// whatever goes wrong is logged, and the connection check that follows (and the training preflight) report the ML sidecar as not reachable.
func startMLSidecar(ctx context.Context,
	opts Options) {
	fields := log.Fields{"url": opts.MLSidecarURL}
	pingCtx, cancel := context.WithTimeout(ctx, mlSidecarPingTimeout)
	defer cancel()
	if _, err := ml_sidecar.New(opts.MLSidecarURL).Health(pingCtx); err == nil {
		log.WithFields(fields).Info("the ML sidecar is already running; not starting one")
		return
	}
	// Forwarding (the default): the output of the ML sidecar goes to the server's stdout/stderr (nil writers). Off: it is dropped.
	var stdout, stderr io.Writer
	if !opts.MLSidecarForwardLogs {
		stdout, stderr = io.Discard, io.Discard
	}
	p, err := ml_sidecar.Launch(ctx,
		ml_sidecar.LaunchOptions{
			URL:    opts.MLSidecarURL,
			Dir:    opts.MLSidecarDir,
			Stdout: stdout,
			Stderr: stderr,
		})
	switch {
	case errors.Is(err, ml_sidecar.ErrNotLocal):
		log.WithFields(fields).Warn("the ML sidecar URL is not local and nothing answers there; not starting one")
	case err != nil:
		log.WithError(err).WithFields(fields).Warn("could not start the ML sidecar")
	default:
		log.WithFields(log.Fields{
			"url":          opts.MLSidecarURL,
			"dir":          p.Dir,
			"python":       p.Python,
			"forward_logs": opts.MLSidecarForwardLogs,
		}).Info("the ML sidecar was started by the server (training mode); it stops with the server")
	}
}
