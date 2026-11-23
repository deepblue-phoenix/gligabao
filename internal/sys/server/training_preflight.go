package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/deepblue-phoenix/gligabao/internal/evals/userfiles"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
)

// trainingPreflightTimeout bounds each remote check of requireTrainingCredentials.
const trainingPreflightTimeout = 15 * time.Second

// trainingCheck is one thing training needs: what it is and the problem found with it ("" if it is fine).
type trainingCheck struct {
	name    string
	problem string
}

// requireTrainingCredentials makes sure everything training needs is accessible; it is called only in training mode, and its error
// halts the startup (training is a critical part of the system). It checks:
//   - the Anthropic API key (the LLM that judges the training evals and audits),
//   - the sidecar answers, and each of its training backends is configured and healthy (that is where the credentials of the
//     training provider, e.g. MODAL_TOKEN_ID/MODAL_TOKEN_SECRET, live: the backend is only healthy if the provider accepts them),
//   - every S3 bucket of the training files (datasets, evals) that is configured can be listed with the AWS credentials.
//
// All the problems are collected and reported together.
func requireTrainingCredentials(ctx context.Context,
	opts Options,
	sidecarOK bool) error {
	checks := append([]trainingCheck{
		{
			name:    "Anthropic API key (judge of the training evals and audits)",
			problem: missingKeyProblem(opts.AnthropicKey),
		},
		sidecarCheck(ctx,
			sidecarOK),
	},
		s3Checks(ctx,
			map[string]userfiles.S3Config{
				"training datasets S3 bucket": opts.TrainingDirs.DatasetsS3,
				"evals S3 bucket":             opts.Evals.S3,
			})...)
	return trainingChecksError(checks)
}

// missingKeyProblem is the problem of an empty key, "" if there is one.
func missingKeyProblem(key string) string {
	if strings.TrimSpace(key) == "" {
		return "not set"
	}
	return ""
}

// sidecarCheck checks that the sidecar answered its health check and that all its backends are usable.
func sidecarCheck(ctx context.Context,
	sidecarOK bool) trainingCheck {
	name := "training sidecar and its backend credentials"
	c := sidecarClient.Load()
	if !sidecarOK || c == nil {
		return trainingCheck{
			name:    name,
			problem: "the sidecar is not reachable (check sidecar_url / SIDECAR_URL and that the sidecar runs)",
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, trainingPreflightTimeout)
	defer cancel()
	backends, err := c.Backends(callCtx)
	if err != nil {
		return trainingCheck{
			name:    name,
			problem: "cannot list the sidecar backends: " + err.Error(),
		}
	}
	return trainingCheck{
		name:    name,
		problem: backendsProblem(backends),
	}
}

// backendsProblem describes the backends that are missing, not configured or not healthy ("" if all are usable).
func backendsProblem(backends []sidecar.BackendStatus) string {
	if len(backends) == 0 {
		return "the sidecar has no training backend"
	}
	problems := []string{}
	for _, b := range backends {
		configured, _ := b["configured"].(bool)
		healthy, _ := b["healthy"].(bool)
		name := fmt.Sprint(b["name"])
		switch {
		case !configured:
			problems = append(problems, fmt.Sprintf("backend %q is not configured", name))
		case !healthy:
			problems = append(problems, fmt.Sprintf("backend %q is not healthy (%v)", name, b["error"]))
		}
	}
	return strings.Join(problems, "; ")
}

// s3Checks checks every configured (non-empty bucket) S3 location. Names are in a map, so the order of the checks is not meaningful.
func s3Checks(ctx context.Context,
	locations map[string]userfiles.S3Config) []trainingCheck {
	checks := []trainingCheck{}
	for name, cfg := range locations {
		if cfg.Bucket == "" {
			continue
		}
		problem := ""
		if err := s3Accessible(ctx, cfg); err != nil {
			problem = err.Error()
		}
		checks = append(checks, trainingCheck{
			name:    fmt.Sprintf("%s (%s)", name, cfg.Bucket),
			problem: problem,
		})
	}
	return checks
}

// s3Accessible lists one object of the bucket: it fails if the AWS credentials are missing or have no access to it.
func s3Accessible(ctx context.Context,
	cfg userfiles.S3Config) error {
	client, err := userfiles.NewS3Client(ctx, cfg)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, trainingPreflightTimeout)
	defer cancel()
	_, err = client.ListObjectsV2(callCtx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(cfg.Bucket),
		MaxKeys: aws.Int32(1),
	})
	if err != nil {
		return fmt.Errorf("cannot access the bucket with the AWS credentials: %w", err)
	}
	return nil
}

// trainingChecksError is nil if no check has a problem, else one error that lists them all.
func trainingChecksError(checks []trainingCheck) error {
	lines := []string{}
	for _, c := range checks {
		if c.problem != "" {
			lines = append(lines, "  - "+c.name+": "+c.problem)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return errors.New("training is enabled but its credentials are not accessible:\n" + strings.Join(lines, "\n"))
}
