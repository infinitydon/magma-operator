package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	magmav1alpha1 "github.com/infinitydon/magma-operator/api/v1alpha1"
)

const (
	agwRootCASyncComponent = "agw-rootca-sync"
	rootCASecretKey        = "rootCA.pem"
	rootCAHashAnnotation   = "magma.infra.don/rootca-sha256"
)

type agwLifecycleState struct {
	Orc8rServiceIP  string
	TrustBundleHash string
}

func (r *MagmaAGWReconciler) reconcileAGWLifecyclePrereqs(ctx context.Context, agw *magmav1alpha1.MagmaAGW, releaseName string, values map[string]string) (*agwLifecycleState, bool, string, string, error) {
	state := &agwLifecycleState{}

	serviceIP, err := r.reconcileOrc8rHostAlias(ctx, agw, values)
	if err != nil {
		return state, false, "Orc8rServiceDiscoveryFailed", err.Error(), err
	}
	state.Orc8rServiceIP = serviceIP

	hash, ready, reason, message, err := r.reconcileAGWTrustBundle(ctx, agw, releaseName, values)
	if err != nil || !ready {
		state.TrustBundleHash = hash
		return state, ready, reason, message, err
	}
	state.TrustBundleHash = hash

	return state, true, "", "", nil
}

func (r *MagmaAGWReconciler) reconcileOrc8rHostAlias(ctx context.Context, agw *magmav1alpha1.MagmaAGW, values map[string]string) (string, error) {
	if values["orc8r.hostAliases.enabled"] == stringFalse {
		return "", nil
	}

	namespace := orc8rNamespace(agw)
	serviceName := agw.Spec.NMSAPIHost
	if serviceName == "" {
		serviceName = orc8rReleaseName(agw) + "-nginx-proxy"
	}

	var svc corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: serviceName}, &svc); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == corev1.ClusterIPNone {
		return "", nil
	}

	values["orc8r.hostAliases.enabled"] = stringTrue
	values["orc8r.hostAliases.ip"] = svc.Spec.ClusterIP
	return svc.Spec.ClusterIP, nil
}

func (r *MagmaAGWReconciler) reconcileAGWTrustBundle(ctx context.Context, agw *magmav1alpha1.MagmaAGW, releaseName string, values map[string]string) (string, bool, string, string, error) {
	namespace := orc8rNamespace(agw)
	orc8rSecretName := agw.Spec.NMSAdminCertSecretName
	if orc8rSecretName == "" {
		orc8rSecretName = defaultNMSAdminCertSecretName
	}

	var orc8rSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: orc8rSecretName}, &orc8rSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", false, "WaitingForOrc8rCertSecret", fmt.Sprintf("waiting for Orc8r cert Secret %s/%s", namespace, orc8rSecretName), nil
		}
		return "", false, "Orc8rCertSecretReadFailed", err.Error(), err
	}
	rootCA := orc8rSecret.Data[rootCASecretKey]
	if len(rootCA) == 0 {
		return "", false, "Orc8rRootCAMissing", fmt.Sprintf("Orc8r cert Secret %s/%s is missing %s", namespace, orc8rSecretName, rootCASecretKey), nil
	}
	hash := sha256Hex(rootCA)

	agwCertSecretName := agwCertSecretName(releaseName, values)
	var agwSecret corev1.Secret
	agwSecret.Name = agwCertSecretName
	agwSecret.Namespace = agw.Namespace
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, &agwSecret, func() error {
		agwSecret.Type = corev1.SecretTypeOpaque
		if agwSecret.Labels == nil {
			agwSecret.Labels = map[string]string{}
		}
		agwSecret.Labels[labelAppManagedBy] = managedByMagmaOperator
		if agwSecret.Annotations == nil {
			agwSecret.Annotations = map[string]string{}
		}
		agwSecret.Annotations[rootCAHashAnnotation] = hash
		if agwSecret.Data == nil {
			agwSecret.Data = map[string][]byte{}
		}
		agwSecret.Data[rootCASecretKey] = rootCA
		return nil
	}); err != nil {
		return hash, false, "AGWCertSecretSyncFailed", err.Error(), err
	}

	var pvc corev1.PersistentVolumeClaim
	pvcName := releaseName + "-claim"
	if err := r.Get(ctx, types.NamespacedName{Namespace: agw.Namespace, Name: pvcName}, &pvc); err != nil {
		if apierrors.IsNotFound(err) {
			return hash, true, "", "", nil
		}
		return hash, false, "AGWPVCReadFailed", err.Error(), err
	}

	ready, reason, message, err := r.reconcileRootCASyncJob(ctx, agw.Namespace, releaseName, agwCertSecretName, pvcName, hash)
	return hash, ready, reason, message, err
}

func (r *MagmaAGWReconciler) reconcileRootCASyncJob(ctx context.Context, namespace, releaseName, secretName, pvcName, hash string) (bool, string, string, error) {
	jobName := releaseName + "-rootca-sync"
	var existing batchv1.Job
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: jobName}, &existing)
	if err == nil {
		if existing.Annotations[rootCAHashAnnotation] != hash {
			if err := r.Delete(ctx, &existing, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
				return false, "RootCASyncJobDeleteFailed", err.Error(), err
			}
			return false, "RootCASyncJobReplacing", "root CA changed; replacing AGW PVC sync job", nil
		}
		if existing.Status.Succeeded > 0 {
			return true, "", "", nil
		}
		if existing.Status.Failed > 0 {
			return false, "RootCASyncJobFailed", fmt.Sprintf("root CA sync job %s/%s failed", namespace, jobName), nil
		}
		return false, "RootCASyncJobRunning", fmt.Sprintf("waiting for root CA sync job %s/%s", namespace, jobName), nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return false, "RootCASyncJobReadFailed", err.Error(), err
	}

	job := rootCASyncJob(namespace, jobName, releaseName, secretName, pvcName, hash)
	if err := r.Create(ctx, job); err != nil {
		return false, "RootCASyncJobCreateFailed", err.Error(), err
	}
	return false, "RootCASyncJobCreated", fmt.Sprintf("created root CA sync job %s/%s", namespace, jobName), nil
}

func rootCASyncJob(namespace, name, releaseName, secretName, pvcName, hash string) *batchv1.Job {
	ttl := int32(3600)
	backoff := int32(1)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				labelAppComponent: agwRootCASyncComponent,
				labelAppInstance:  releaseName,
				labelAppManagedBy: managedByMagmaOperator,
			},
			Annotations: map[string]string{
				rootCAHashAnnotation: hash,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:    "sync-rootca",
						Image:   "busybox:1.36",
						Command: []string{"sh", shellExitOnErrorCommand},
						Args: []string{`mkdir -p /var/opt/magma/certs
cp -f /certs/rootCA.pem /var/opt/magma/certs/rootCA.pem
chmod 0644 /var/opt/magma/certs/rootCA.pem`},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "agwc-claim", MountPath: "/var/opt/magma"},
							{Name: "agwc-secret", MountPath: "/certs", ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{
							Name: "agwc-claim",
							VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: pvcName,
							}},
						},
						{
							Name: "agwc-secret",
							VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
								SecretName:  secretName,
								DefaultMode: ptr.To[int32](0444),
							}},
						},
					},
				},
			},
		},
	}
}

func (r *MagmaAGWReconciler) annotateAGWCoreDeploymentsForRootCA(ctx context.Context, namespace, releaseName, hash string) error {
	return r.annotateDeploymentsForRootCA(ctx, namespace, releaseName, hash, func(deployment appsv1.Deployment) bool {
		return !isUERANSIMDeployment(deployment.Name)
	})
}

func (r *MagmaAGWReconciler) annotateDeploymentsForRootCA(ctx context.Context, namespace, releaseName, hash string, include func(appsv1.Deployment) bool) error {
	if hash == "" {
		return nil
	}
	var deployments appsv1.DeploymentList
	err := r.List(ctx, &deployments,
		client.InNamespace(namespace),
		client.MatchingLabels{
			labelAppInstance: releaseName,
			labelAppName:     magmaAGWAppName,
		},
	)
	if err != nil {
		return err
	}
	for i := range deployments.Items {
		deployment := &deployments.Items[i]
		if !include(*deployment) {
			continue
		}
		if deployment.Spec.Template.Annotations[rootCAHashAnnotation] == hash {
			continue
		}
		patch := client.MergeFrom(deployment.DeepCopy())
		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = map[string]string{}
		}
		deployment.Spec.Template.Annotations[rootCAHashAnnotation] = hash
		if err := r.Patch(ctx, deployment, patch); err != nil {
			return err
		}
	}
	return nil
}

func (r *MagmaAGWReconciler) deploymentsReady(ctx context.Context, namespace, releaseName, label string, include func(appsv1.Deployment) bool, requiredRootCAHash string) (bool, string, error) {
	var deployments appsv1.DeploymentList
	err := r.List(ctx, &deployments,
		client.InNamespace(namespace),
		client.MatchingLabels{
			labelAppInstance: releaseName,
			labelAppName:     magmaAGWAppName,
		},
	)
	if err != nil {
		return false, "", err
	}

	matched := 0
	for _, deployment := range deployments.Items {
		if !include(deployment) {
			continue
		}
		matched++
		if requiredRootCAHash != "" && deployment.Spec.Template.Annotations[rootCAHashAnnotation] != requiredRootCAHash {
			return false, fmt.Sprintf("waiting for %s deployment %s/%s root CA rollout annotation", label, namespace, deployment.Name), nil
		}
		desired := int32(1)
		if deployment.Spec.Replicas != nil {
			desired = *deployment.Spec.Replicas
		}
		if deployment.Status.ObservedGeneration < deployment.Generation {
			return false, fmt.Sprintf("waiting for %s deployment %s/%s controller observation", label, namespace, deployment.Name), nil
		}
		if deployment.Status.ReadyReplicas < desired || deployment.Status.AvailableReplicas < desired {
			return false, fmt.Sprintf("waiting for %s deployment %s/%s readiness: %d/%d ready", label, namespace, deployment.Name, deployment.Status.ReadyReplicas, desired), nil
		}
	}
	if matched == 0 {
		return false, fmt.Sprintf("waiting for %s deployments for release %s/%s", label, namespace, releaseName), nil
	}
	return true, fmt.Sprintf("%s deployments are ready", label), nil
}

func isUERANSIMDeployment(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "ueransim")
}

func updateAGWLifecycleStatus(agw *magmav1alpha1.MagmaAGW, state *agwLifecycleState) {
	if state == nil {
		return
	}
	agw.Status.Orc8rServiceIP = state.Orc8rServiceIP
	agw.Status.TrustBundleHash = state.TrustBundleHash
}

func agwCertSecretName(releaseName string, values map[string]string) string {
	if values["secret.certs"] != "" {
		return values["secret.certs"]
	}
	return releaseName + "-secret-certs"
}

func orc8rNamespace(agw *magmav1alpha1.MagmaAGW) string {
	if agw.Spec.Orc8rNamespace != "" {
		return agw.Spec.Orc8rNamespace
	}
	return defaultOrc8rNamespace
}

func orc8rReleaseName(agw *magmav1alpha1.MagmaAGW) string {
	if agw.Spec.Orc8rReleaseName != "" {
		return agw.Spec.Orc8rReleaseName
	}
	return defaultOrc8rReleaseName
}
