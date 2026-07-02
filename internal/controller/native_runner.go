package controller

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io"
	"maps"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	stringTrue  = "true"
	stringFalse = "false"
)

//go:embed native_manifests/*.yaml
var nativeManifestFS embed.FS

type nativeRelease struct {
	ReleaseName string
	Namespace   string
	Manifest    string
	Values      map[string]string
	ApplyFilter func(*unstructured.Unstructured) bool
}

func reconcileNativeRelease(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner client.Object, release nativeRelease) error {
	objects, err := nativeReleaseObjects(release)
	if err != nil {
		return err
	}
	for i := range objects {
		object := objects[i]
		if release.ApplyFilter != nil && !release.ApplyFilter(object) {
			if err := deleteNativeObject(ctx, c, object); err != nil {
				return err
			}
			continue
		}
		if object.GetKind() == "Secret" {
			exists, err := preserveExistingNativeSecret(ctx, c, scheme, owner, object)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
		}
		if err := controllerutil.SetControllerReference(owner, object, scheme); err != nil {
			return err
		}
		//nolint:staticcheck // Unstructured server-side apply still uses Patch with client.Apply.
		if err := c.Patch(ctx, object, client.Apply, client.FieldOwner("magma-operator"), client.ForceOwnership); err != nil {
			if errors.IsInvalid(err) && object.GetKind() == "Job" {
				if deleteErr := deleteNativeObject(ctx, c, object); deleteErr != nil {
					return deleteErr
				}
				continue
			}
			return err
		}
		if err := cleanupNativeObjectMetadata(ctx, c, object); err != nil {
			return err
		}
	}
	return nil
}

func preserveExistingNativeSecret(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner client.Object, object *unstructured.Unstructured) (bool, error) {
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(object.GroupVersionKind())
	if err := c.Get(ctx, client.ObjectKeyFromObject(object), current); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if err := controllerutil.SetControllerReference(owner, current, scheme); err != nil {
		return false, err
	}
	labels, annotations, _ := nativeObjectMetadata(current.GetLabels(), current.GetAnnotations())
	current.SetLabels(labels)
	current.SetAnnotations(annotations)
	return true, c.Update(ctx, current)
}

func deleteNativeRelease(ctx context.Context, c client.Client, release nativeRelease) error {
	objects, err := nativeReleaseObjects(release)
	if err != nil {
		return err
	}
	for i := range objects {
		if err := deleteNativeObject(ctx, c, objects[i]); err != nil {
			return err
		}
	}
	return nil
}

func deleteNativeObject(ctx context.Context, c client.Client, object *unstructured.Unstructured) error {
	if err := c.Delete(ctx, object, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

func nativeReleaseObjects(release nativeRelease) ([]*unstructured.Unstructured, error) {
	raw, err := nativeManifestFS.ReadFile("native_manifests/" + release.Manifest)
	if err != nil {
		return nil, err
	}
	rendered := renderNativeManifest(string(raw), release)
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(rendered)), 4096)
	objects := []*unstructured.Unstructured{}
	for {
		object := &unstructured.Unstructured{}
		if err := decoder.Decode(object); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if object.GetKind() == "" || object.GetName() == "" {
			continue
		}
		if object.GetNamespace() == "" && object.GetKind() != "Namespace" {
			object.SetNamespace(release.Namespace)
		}
		normalizeNativeObjectMetadata(object)
		applyNodeSelectors(object, release.Values)
		objects = append(objects, object)
	}
	return objects, nil
}

func normalizeNativeObjectMetadata(object *unstructured.Unstructured) {
	labels, annotations, _ := nativeObjectMetadata(object.GetLabels(), object.GetAnnotations())
	object.SetLabels(labels)
	object.SetAnnotations(annotations)
}

func cleanupNativeObjectMetadata(ctx context.Context, c client.Client, object *unstructured.Unstructured) error {
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(object.GroupVersionKind())
	if err := c.Get(ctx, client.ObjectKeyFromObject(object), current); err != nil {
		return err
	}
	labels, annotations, changed := nativeObjectMetadata(current.GetLabels(), current.GetAnnotations())
	if !changed {
		return nil
	}
	current.SetLabels(labels)
	current.SetAnnotations(annotations)
	return c.Update(ctx, current)
}

func nativeObjectMetadata(existingLabels, existingAnnotations map[string]string) (map[string]string, map[string]string, bool) {
	changed := false
	labels := existingLabels
	if labels == nil {
		labels = map[string]string{}
	}
	if _, ok := labels["helm.sh/chart"]; ok {
		delete(labels, "helm.sh/chart")
		changed = true
	}
	if labels[labelAppManagedBy] != managedByMagmaOperator {
		labels[labelAppManagedBy] = managedByMagmaOperator
		changed = true
	}

	annotations := existingAnnotations
	for key := range annotations {
		if strings.HasPrefix(key, "helm.sh/") {
			delete(annotations, key)
			changed = true
		}
	}
	return labels, annotations, changed
}

func renderNativeManifest(manifest string, release nativeRelease) string {
	domainName := valueOrDefault(release.Values, "global.domainName", valueOrDefault(release.Values, "config.domainName", "magma.local"))
	controllerHostname := valueOrDefault(release.Values, "orc8r.nms.controllerHostname", "controller."+domainName)
	replacements := map[string]string{
		"agwrel":                              release.ReleaseName,
		"agwns":                               release.Namespace,
		"orc8rrel":                            release.ReleaseName,
		"orc8rns":                             release.Namespace,
		"domain-placeholder.local":            domainName,
		"controller-placeholder.local":        controllerHostname,
		"controller.magma.local":              "controller." + domainName,
		"bootstrapper-controller.magma.local": "bootstrapper-controller." + domainName,
		"accessd-controller.magma.local":      "accessd-controller." + domainName,
		"certifier-controller.magma.local":    "certifier-controller." + domainName,
		"configurator-controller.magma.local": "configurator-controller." + domainName,
		"device-controller.magma.local":       "device-controller." + domainName,
		"directoryd-controller.magma.local":   "directoryd-controller." + domainName,
		"dispatcher-controller.magma.local":   "dispatcher-controller." + domainName,
		"eventd-controller.magma.local":       "eventd-controller." + domainName,
		"metricsd-controller.magma.local":     "metricsd-controller." + domainName,
		"policydb-controller.magma.local":     "policydb-controller." + domainName,
		"state-controller.magma.local":        "state-controller." + domainName,
		"streamer-controller.magma.local":     "streamer-controller." + domainName,
		"subscriberdb-controller.magma.local": "subscriberdb-controller." + domainName,
		"10.0.0.1":                            valueOrDefault(release.Values, "orc8r.hostAliases.ip", "10.152.183.140"),
		"gwchallengeplaceholder":              release.Values["config.gwChallenge"],
		"hardwareplaceholder":                 release.Values["gatewayIdentity.snowflake"],
		"s1placeholder":                       release.Values["nodePrep.interfaces.s1.parent"],
		"sgiplaceholder":                      release.Values["nodePrep.interfaces.nat.parent"],
		"orgplaceholder":                      valueOrDefault(release.Values, "nmsAdmin.organization", "magma-test"),
		"emailplaceholder":                    valueOrDefault(release.Values, "nmsAdmin.email", "admin"),
		"passwordplaceholder":                 valueOrDefault(release.Values, "nmsAdmin.password", "admin"),
		"networkidplaceholder":                valueOrDefault(release.Values, "provisioning.network.id", "mpk_test"),
		"networknameplaceholder":              valueOrDefault(release.Values, "provisioning.network.name", "mpk_test"),
		"imsiplaceholder":                     valueOrDefault(release.Values, "provisioning.subscriber.imsi", "IMSI001010000000001"),
	}
	keys := make([]string, 0, len(replacements)*2)
	for old, newValue := range replacements {
		keys = append(keys, old, newValue)
	}
	return strings.NewReplacer(keys...).Replace(manifest)
}

func applyNodeSelectors(object *unstructured.Unstructured, values map[string]string) {
	selector := selectorForObject(object.GetName(), values)
	if len(selector) == 0 {
		return
	}

	switch object.GetKind() {
	case "Deployment", "DaemonSet", "StatefulSet":
		setPodSpecNodeSelector(object, selector, "spec", "template", "spec")
	case "Job":
		setPodSpecNodeSelector(object, selector, "spec", "template", "spec")
	case "CronJob":
		setPodSpecNodeSelector(object, selector, "spec", "jobTemplate", "spec", "template", "spec")
	case "Pod":
		setPodSpecNodeSelector(object, selector, "spec")
	}
}

func setPodSpecNodeSelector(object *unstructured.Unstructured, selector map[string]string, fields ...string) {
	unstructured.RemoveNestedField(object.Object, append(fields, "nodeSelector")...)
	_ = unstructured.SetNestedStringMap(object.Object, selector, append(fields, "nodeSelector")...)
}

func selectorForObject(name string, values map[string]string) map[string]string {
	switch {
	case strings.Contains(name, "ueransim") || strings.Contains(name, "iperf3"):
		return selectorValues(values, "simulator.nodeSelector.")
	case name == agwNodePrepName:
		return selectorValues(values, "nodePrep.nodeSelector.")
	default:
		return selectorValues(values, "nodeSelector.")
	}
}

func selectorValues(values map[string]string, prefix string) map[string]string {
	selector := map[string]string{}
	for key, value := range values {
		if selectorKey, ok := strings.CutPrefix(key, prefix); ok {
			selectorKey = strings.ReplaceAll(selectorKey, `\\.`, `.`)
			selectorKey = strings.ReplaceAll(selectorKey, `\.`, `.`)
			selector[selectorKey] = value
		}
	}
	return selector
}

func valueOrDefault(values map[string]string, key, fallback string) string {
	if values[key] != "" {
		return values[key]
	}
	return fallback
}

func nativeAGWApplyFilter(enableUERANSIM bool) func(*unstructured.Unstructured) bool {
	return func(object *unstructured.Unstructured) bool {
		if enableUERANSIM {
			return true
		}
		name := object.GetName()
		return !strings.Contains(name, "ueransim") && !strings.Contains(name, "simulator") && !strings.Contains(name, "iperf3")
	}
}

func setValue(values map[string]string, key, value string) {
	if value != "" {
		values[key] = value
	}
}

func setSelectorValues(values map[string]string, prefix string, selector map[string]string) {
	for key, value := range selector {
		values[prefix+"."+escapeValueKey(key)] = value
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

func escapeValueKey(key string) string {
	return strings.NewReplacer(`\`, `\\`, `.`, `\.`).Replace(key)
}

func conditionMessage(message string) string {
	const maxConditionMessage = 32000
	if len(message) <= maxConditionMessage {
		return message
	}
	return message[:maxConditionMessage] + "... truncated"
}
