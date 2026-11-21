package gligabao

import (
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/runmanager"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	"github.com/deepblue-phoenix/gligabao/internal/sys/server"
)

// TestEvalSettingsFromConfig checks that the evals_* settings of the config reach the server as they are, and that the defaults of the
// config are the ones of the run manager's limits.
func TestEvalSettingsFromConfig(t *testing.T) {
	cfg := config.Default()
	cfg.EvalsS3Bucket = "meta"
	cfg.EvalsDatasetRetentionDays = 3
	cfg.EvalsHeartbeatStale = config.Duration(5 * time.Minute)
	s := evalSettings(cfg)
	if s.S3.Bucket != "meta" || s.S3.Prefix != "evals" || s.MaxCases != 100 || s.MaxFileBytes != 1<<20 || s.MaxTextBytes != 256<<10 {
		t.Fatalf("file settings: %+v", s)
	}
	if s.DatasetRetention != 3*24*time.Hour || s.HeartbeatStale != 5*time.Minute || s.HeartbeatInterval != 15*time.Second {
		t.Fatalf("durations: %+v", s)
	}
	if s.MaxRunsPerUser != 2 || s.MaxRuns != 8 || s.MaxRunCostUSD != 5 || s.MaxUserCostUSDPerDay != 20 {
		t.Fatalf("run limits: %+v", s)
	}
	// The defaults of the config are the run manager's.
	d := runmanager.DefaultLimits()
	got := runLimits(s)
	if got.MaxRunsPerUser != d.MaxRunsPerUser || got.MaxRuns != d.MaxRuns || got.MaxRunCostUSD != d.MaxRunCostUSD ||
		got.MaxUserCostUSDPerDay != d.MaxUserCostUSDPerDay || got.HeartbeatInterval != d.HeartbeatInterval {
		t.Fatalf("runLimits(config defaults) = %+v, want the run manager's defaults %+v", got, d)
	}
}

// TestFileLimits checks that the evals_max_* settings replace the limits of an eval file, and that zero ones keep the defaults.
func TestFileLimits(t *testing.T) {
	def := fileLimits(nil, server.EvalSettings{})
	if def.MaxCases != 100 || def.MaxFileBytes != 1<<20 || def.MaxTextBytes != 256<<10 || def.MaxRemember != 20 {
		t.Fatalf("defaults: %+v", def)
	}
	got := fileLimits(nil, server.EvalSettings{MaxCases: 7, MaxFileBytes: 900, MaxTextBytes: 10})
	if got.MaxCases != 7 || got.MaxFileBytes != 900 || got.MaxTextBytes != 10 || got.MaxRemember != 20 {
		t.Fatalf("overrides: %+v", got)
	}
}

// TestTrajectoryDirSetting checks the directory of the trajectories: ServeOptions first, then the config, then the server's default (empty).
func TestTrajectoryDirSetting(t *testing.T) {
	cfg := config.Default()
	if d := trajectoryDir(ServeOptions{}, cfg); d != "" {
		t.Fatalf("no setting: %q", d)
	}
	cfg.EvalsTrajectoryDir = "/from/config"
	if d := trajectoryDir(ServeOptions{}, cfg); d != "/from/config" {
		t.Fatalf("config: %q", d)
	}
	if d := trajectoryDir(ServeOptions{TrajectoryDir: "/from/opts"}, cfg); d != "/from/opts" {
		t.Fatalf("options win: %q", d)
	}
}
