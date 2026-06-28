package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultChartRepo = "https://github.com/infinitydon/telco-helm-charts.git"
	defaultRevision  = "main"
)

type helmRelease struct {
	ReleaseName string
	Namespace   string
	Repo        string
	Revision    string
	ChartPath   string
	Values      map[string]string
}

type helmResult struct {
	ChartDir string
	Output   string
}

func reconcileHelmRelease(ctx context.Context, release helmRelease) (helmResult, error) {
	if release.ReleaseName == "" {
		return helmResult{}, fmt.Errorf("release name is required")
	}
	if release.Namespace == "" {
		return helmResult{}, fmt.Errorf("release namespace is required")
	}
	if release.Repo == "" {
		release.Repo = defaultChartRepo
	}
	if release.Revision == "" {
		release.Revision = defaultRevision
	}

	runCtx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()

	repoDir := chartRepoDir(release)
	if err := syncChartRepo(runCtx, release.Repo, release.Revision, repoDir); err != nil {
		return helmResult{}, err
	}

	chartDir := filepath.Join(repoDir, release.ChartPath)
	args := []string{
		"upgrade", "--install", release.ReleaseName, chartDir,
		"--namespace", release.Namespace,
		"--create-namespace",
		"--wait",
		"--timeout", "20m",
	}
	for key, value := range release.Values {
		flag := "--set-string"
		if !strings.Contains(key, "nodeSelector") && (value == "true" || value == "false" || strings.HasSuffix(key, "nodePort")) {
			flag = "--set"
		}
		args = append(args, flag, fmt.Sprintf("%s=%s", key, value))
	}

	out, err := runCommand(runCtx, "", "helm", args...)
	if err != nil {
		return helmResult{ChartDir: chartDir, Output: out}, fmt.Errorf("helm upgrade failed: %w: %s", err, out)
	}
	return helmResult{ChartDir: chartDir, Output: out}, nil
}

func syncChartRepo(ctx context.Context, repo, revision, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if out, err := runCommand(ctx, dir, "git", "fetch", "--all", "--tags", "--prune"); err != nil {
			return fmt.Errorf("git fetch failed: %w: %s", err, out)
		}
		if out, err := runCommand(ctx, dir, "git", "checkout", revision); err != nil {
			return fmt.Errorf("git checkout failed: %w: %s", err, out)
		}
		if out, err := runCommand(ctx, dir, "git", "pull", "--ff-only"); err != nil {
			return fmt.Errorf("git pull failed: %w: %s", err, out)
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if out, err := runCommand(ctx, "", "git", "clone", "--branch", revision, "--depth", "1", repo, dir); err != nil {
		return fmt.Errorf("git clone failed: %w: %s", err, out)
	}
	return nil
}

func chartRepoDir(release helmRelease) string {
	sum := sha256.Sum256([]byte(release.Repo + "@" + release.Revision + ":" + release.ChartPath + ":" + release.Namespace + ":" + release.ReleaseName))
	return filepath.Join(os.TempDir(), "magma-operator-charts", hex.EncodeToString(sum[:])[:16])
}

func runCommand(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func setValue(values map[string]string, key, value string) {
	if value != "" {
		values[key] = value
	}
}

func setSelectorValues(values map[string]string, prefix string, selector map[string]string) {
	for key, value := range selector {
		values[prefix+"."+escapeHelmKey(key)] = value
	}
}

func escapeHelmKey(key string) string {
	return strings.NewReplacer(`\`, `\\`, `.`, `\.`).Replace(key)
}

func conditionMessage(message string) string {
	const maxConditionMessage = 32000
	if len(message) <= maxConditionMessage {
		return message
	}
	return message[:maxConditionMessage] + "... truncated"
}
