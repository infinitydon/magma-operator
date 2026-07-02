package controller

import (
	"context"
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
	stringTrue              = "true"
	stringFalse             = "false"
)

type helmRelease struct {
	ReleaseName string
	Namespace   string
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

	runCtx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()

	chartDir, err := resolveChartDir(release)
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

func resolveChartDir(release helmRelease) (string, error) {
	for _, root := range bundledChartRoots() {
		chartDir := filepath.Join(root, release.ChartPath)
		if _, err := os.Stat(filepath.Join(chartDir, "Chart.yaml")); err == nil {
			return chartDir, nil
		}
	}
	return "", fmt.Errorf("bundled chart %q was not found in %v", release.ChartPath, bundledChartRoots())
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
