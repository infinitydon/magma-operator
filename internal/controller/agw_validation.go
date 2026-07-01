package controller

import (
	"context"
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	magmav1alpha1 "github.com/infinitydon/magma-operator/api/v1alpha1"
)

const (
	ueransimValidationComponent   = "ueransim-validation"
	ueransimValidationHash        = "magma.infra.don/ueransim-validation-hash"
	ueransimValidationRolloutHash = "magma.infra.don/ueransim-validation-rollout-hash"
	ueransimValidationServiceAcct = "magma-operator-ueransim-validation"
)

func (r *MagmaAGWReconciler) reconcileUERANSIMValidation(ctx context.Context, agw *magmav1alpha1.MagmaAGW, releaseName string) (bool, string, string, error) {
	if !ueransimValidationEnabled(agw.Spec.UERANSIMValidation) {
		removeAGWCondition(agw, "UERANSIMValidated")
		return true, "UERANSIMValidationDisabled", "UERANSIM validation is disabled", nil
	}
	if !agw.Spec.EnableUERANSIM {
		return true, "UERANSIMValidationSkipped", "UERANSIM is disabled", nil
	}

	if err := r.reconcileUERANSIMValidationRBAC(ctx, agw.Namespace); err != nil {
		return false, "UERANSIMValidationRBACFailed", err.Error(), err
	}

	jobName := releaseName + "-ueransim-validation"
	desiredHash := ueransimValidationSpecHash(agw, releaseName)
	var existing batchv1.Job
	err := r.Get(ctx, types.NamespacedName{Namespace: agw.Namespace, Name: jobName}, &existing)
	if err == nil {
		if existing.Annotations[ueransimValidationHash] != desiredHash {
			if err := r.Delete(ctx, &existing, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
				return false, "UERANSIMValidationJobDeleteFailed", err.Error(), err
			}
			return false, "UERANSIMValidationJobReplacing", "UERANSIM validation trigger changed; replacing validation job", nil
		}
		if existing.Status.Succeeded > 0 {
			return true, "UERANSIMValidated", "one-shot UERANSIM validation passed", nil
		}
		if existing.Status.Failed > 0 {
			return true, "UERANSIMValidationFailed", fmt.Sprintf("one-shot UERANSIM validation job %s/%s failed", agw.Namespace, jobName), nil
		}
		return false, "UERANSIMValidationRunning", fmt.Sprintf("waiting for one-shot UERANSIM validation job %s/%s", agw.Namespace, jobName), nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return false, "UERANSIMValidationJobReadFailed", err.Error(), err
	}

	rolloutReady, reason, message, err := r.reconcileUERANSIMValidationRollout(ctx, agw.Namespace, releaseName, desiredHash)
	if err != nil || !rolloutReady {
		return false, reason, message, err
	}
	uePod, err := r.ueransimUEPod(ctx, agw.Namespace, releaseName)
	if err != nil {
		return false, "UERANSIMValidationPodReadFailed", err.Error(), err
	}
	if uePod == "" {
		return false, "WaitingForUERANSIMUEPod", "waiting for a running UERANSIM UE pod before one-shot validation", nil
	}

	job := ueransimValidationJob(agw, releaseName, jobName, uePod, desiredHash)
	if err := controllerutil.SetControllerReference(agw, job, r.Scheme); err != nil {
		return false, "UERANSIMValidationOwnerRefFailed", err.Error(), err
	}
	if err := r.Create(ctx, job); err != nil {
		return false, "UERANSIMValidationJobCreateFailed", err.Error(), err
	}
	return false, "UERANSIMValidationJobCreated", fmt.Sprintf("created one-shot UERANSIM validation job %s/%s", agw.Namespace, jobName), nil
}

func (r *MagmaAGWReconciler) reconcileUERANSIMValidationRollout(ctx context.Context, namespace, releaseName, validationHash string) (bool, string, string, error) {
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments,
		client.InNamespace(namespace),
		client.MatchingLabels{
			"app.kubernetes.io/instance": releaseName,
			"app.kubernetes.io/name":     "magma-agw-upstream",
		},
	); err != nil {
		return false, "UERANSIMValidationRolloutReadFailed", err.Error(), err
	}

	required := map[string]bool{
		releaseName + "-ueransim-gnb": false,
		releaseName + "-ueransim-ue":  false,
	}
	patched := false
	for i := range deployments.Items {
		deployment := &deployments.Items[i]
		if _, ok := required[deployment.Name]; !ok {
			continue
		}
		required[deployment.Name] = true
		if deployment.Spec.Template.Annotations[ueransimValidationRolloutHash] == validationHash {
			continue
		}
		patch := client.MergeFrom(deployment.DeepCopy())
		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = map[string]string{}
		}
		deployment.Spec.Template.Annotations[ueransimValidationRolloutHash] = validationHash
		if err := r.Patch(ctx, deployment, patch); err != nil {
			return false, "UERANSIMValidationRolloutPatchFailed", err.Error(), err
		}
		patched = true
	}
	for name, found := range required {
		if !found {
			return false, "WaitingForUERANSIMDeployment", fmt.Sprintf("waiting for UERANSIM deployment %s/%s before one-shot validation", namespace, name), nil
		}
	}
	if patched {
		return false, "UERANSIMValidationRolloutStarted", "recreated UERANSIM gNB and UE deployments for one-shot validation", nil
	}

	ready, message, err := r.deploymentsReady(ctx, namespace, releaseName, "UERANSIM validation", func(deployment appsv1.Deployment) bool {
		return deployment.Name == releaseName+"-ueransim-gnb" || deployment.Name == releaseName+"-ueransim-ue"
	}, "")
	if err != nil {
		return false, "UERANSIMValidationRolloutReadinessFailed", err.Error(), err
	}
	if !ready {
		return false, "WaitingForUERANSIMValidationRollout", message, nil
	}
	return true, "UERANSIMValidationRolloutReady", "UERANSIM gNB and UE deployments were recreated and are ready", nil
}

func (r *MagmaAGWReconciler) ueransimUEPod(ctx context.Context, namespace, releaseName string) (string, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(namespace),
		client.MatchingLabels{
			"app.kubernetes.io/instance":  releaseName,
			"app.kubernetes.io/component": "ueransim-ue",
		},
	); err != nil {
		return "", err
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			return pod.Name, nil
		}
	}
	return "", nil
}

func (r *MagmaAGWReconciler) reconcileUERANSIMValidationRBAC(ctx context.Context, namespace string) error {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: ueransimValidationServiceAcct, Namespace: namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		if sa.Labels == nil {
			sa.Labels = map[string]string{}
		}
		sa.Labels["app.kubernetes.io/managed-by"] = "magma-operator"
		sa.Labels["app.kubernetes.io/component"] = ueransimValidationComponent
		return nil
	}); err != nil {
		return err
	}

	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: ueransimValidationServiceAcct, Namespace: namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		role.Rules = []rbacv1.PolicyRule{{
			APIGroups: []string{""},
			Resources: []string{"pods", "pods/log"},
			Verbs:     []string{"get", "list"},
		}, {
			APIGroups: []string{""},
			Resources: []string{"pods/exec"},
			Verbs:     []string{"create"},
		}}
		return nil
	}); err != nil {
		return err
	}

	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: ueransimValidationServiceAcct, Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, binding, func() error {
		binding.Subjects = []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      ueransimValidationServiceAcct,
			Namespace: namespace,
		}}
		binding.RoleRef = rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     ueransimValidationServiceAcct,
		}
		return nil
	})
	return err
}

func ueransimValidationJob(agw *magmav1alpha1.MagmaAGW, releaseName, jobName, uePod, specHash string) *batchv1.Job {
	timeout := agw.Spec.UERANSIMValidation.TimeoutSeconds
	if timeout <= 0 {
		timeout = 180
	}
	pingHost := agw.Spec.UERANSIMValidation.PingHost
	if pingHost == "" {
		pingHost = "4.2.2.2"
	}
	iperfPort := agw.Spec.UERANSIMValidation.IPerfPort
	if iperfPort <= 0 {
		iperfPort = 5201
	}

	backoff := int32(0)
	deadline := timeout + 60
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: agw.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/component":  ueransimValidationComponent,
				"app.kubernetes.io/instance":   releaseName,
				"app.kubernetes.io/managed-by": "magma-operator",
			},
			Annotations: map[string]string{
				ueransimValidationHash: specHash,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoff,
			ActiveDeadlineSeconds: ptr.To[int64](int64(deadline)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: ueransimValidationServiceAcct,
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:            "validate",
						Image:           "registry.k8s.io/kubectl:v1.36.2",
						ImagePullPolicy: corev1.PullIfNotPresent,
						Args: []string{"exec", "-n", agw.Namespace, uePod, "--", "env",
							"TIMEOUT_SECONDS=" + strconv.Itoa(int(timeout)),
							"PING_HOST=" + pingHost,
							"IPERF_SERVER=" + agw.Spec.UERANSIMValidation.IPerfServer,
							"IPERF_PORT=" + strconv.Itoa(int(iperfPort)),
							"sh", "-ceu", `deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
echo "Validating UE pod"
while [ "$(date +%s)" -lt "$deadline" ]; do
  ue_ip="$(ip -4 -o addr show uesimtun0 2>/dev/null | awk '{print $4}' | cut -d/ -f1 || true)"
  if [ -n "$ue_ip" ]; then
    echo "uesimtun0 is up with $ue_ip"
    ping -I uesimtun0 -c 3 -W 3 "$PING_HOST"
    if [ -n "$IPERF_SERVER" ]; then
      iperf3 -c "$IPERF_SERVER" -p "$IPERF_PORT" -B "$ue_ip" -t 5
    fi
    exit 0
  fi
  sleep 5
done
echo "Timed out waiting for uesimtun0"
exit 1`},
					}},
				},
			},
		},
	}
}

func ueransimValidationEnabled(spec magmav1alpha1.MagmaAGWUERANSIMValidationSpec) bool {
	return spec.Enabled != nil && *spec.Enabled
}

func ueransimValidationSpecHash(agw *magmav1alpha1.MagmaAGW, releaseName string) string {
	spec := agw.Spec.UERANSIMValidation
	return sha256Hex([]byte(fmt.Sprintf("%s|%s|%s|%s|%d|%d|%s", releaseName, spec.Trigger, spec.UEDeploymentName, spec.PingHost, spec.IPerfPort, spec.TimeoutSeconds, spec.IPerfServer)))
}
