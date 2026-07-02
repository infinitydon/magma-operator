package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultBundledChartsDir = "/opt/magma-operator/charts"
	defaultRevision         = "main"
	stringTrue              = "true"
	stringFalse             = "false"
)

type helmRelease struct {
	ReleaseName string
	Namespace   string
	Repo        string
	Revision    string
	ChartPath   string
	Values      map[string]string
	Wait        bool
}

func reconcileHelmRelease(ctx context.Context, release helmRelease) error {
	if release.ReleaseName == "" {
		return fmt.Errorf("release name is required")
	}
	if release.Namespace == "" {
		return fmt.Errorf("release namespace is required")
	}
	if release.Revision == "" {
		release.Revision = defaultRevision
	}

	runCtx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()

	chartDir, err := resolveChartDir(runCtx, release)
	if err != nil {
		return err
	}
	args := []string{
		"upgrade", "--install", release.ReleaseName, chartDir,
		"--namespace", release.Namespace,
		"--create-namespace",
		"--timeout", "20m",
	}
	if release.Wait {
		args = append(args, "--wait")
	}
	for key, value := range release.Values {
		flag := "--set-string"
		if !strings.Contains(key, "nodeSelector") && (value == stringTrue || value == stringFalse || strings.HasSuffix(key, "nodePort")) {
			flag = "--set"
		}
		args = append(args, flag, fmt.Sprintf("%s=%s", key, value))
	}

	out, err := runCommand(runCtx, "", "helm", args...)
	if err != nil {
		return fmt.Errorf("helm upgrade failed: %w: %s", err, out)
	}
	return nil
}

func resolveChartDir(ctx context.Context, release helmRelease) (string, error) {
	if release.Repo != "" {
		repoDir := chartRepoDir(release)
		if err := syncChartRepo(ctx, release.Repo, release.Revision, repoDir); err != nil {
			return "", err
		}
		return filepath.Join(repoDir, release.ChartPath), nil
	}

	for _, root := range bundledChartRoots() {
		chartDir := filepath.Join(root, release.ChartPath)
		if _, err := os.Stat(filepath.Join(chartDir, "Chart.yaml")); err == nil {
			return chartDir, nil
		}
	}
	return "", fmt.Errorf("bundled chart %q was not found; set spec.chartRepository only when using an explicit external chart source", release.ChartPath)
}

func bundledChartRoots() []string {
	roots := []string{}
	if envRoot := os.Getenv("MAGMA_OPERATOR_CHARTS_DIR"); envRoot != "" {
		roots = append(roots, envRoot)
	}
	roots = append(roots, defaultBundledChartsDir, "charts")
	return roots
}

func uninstallHelmRelease(ctx context.Context, releaseName, namespace string) error {
	if releaseName == "" || namespace == "" {
		return nil
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	out, err := runCommand(runCtx, "", "helm", "uninstall", releaseName, "--namespace", namespace, "--wait", "--timeout", "10m")
	if err != nil {
		if strings.Contains(out, "not found") || strings.Contains(out, "release: not found") {
			return nil
		}
		return fmt.Errorf("helm uninstall failed: %w: %s", err, out)
	}
	return nil
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

func setListValues(values map[string]string, prefix string, entries []string) {
	for index, entry := range entries {
		if entry != "" {
			values[fmt.Sprintf("%s[%d]", prefix, index)] = entry
		}
	}
}

func mergeValues(values map[string]string, override map[string]string) {
	maps.Copy(values, override)
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
