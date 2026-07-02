/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	magmav1alpha1 "github.com/infinitydon/magma-operator/api/v1alpha1"
)

const (
	ueransimStartPolicyAfterAGWReady = "AfterAGWReady"
	ueransimStartPolicyImmediate     = "Immediate"
	defaultDatapathReadyLabelKey     = "magma.io/agw-datapath-ready"
	defaultDatapathReadyLabelValue   = stringTrue
	magmaAGWFinalizer                = "magma.infra.don/magmaagw-finalizer"
)

// MagmaAGWReconciler reconciles a MagmaAGW object
type MagmaAGWReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=magma.infra.don,resources=magmaagws,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=magma.infra.don,resources=magmaagws/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=magma.infra.don,resources=magmaagws/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups="",resources=namespaces;secrets;services;configmaps;serviceaccounts;persistentvolumeclaims;pods;pods/log,verbs=*
// +kubebuilder:rbac:groups="apps",resources=deployments;statefulsets;daemonsets;replicasets,verbs=*
// +kubebuilder:rbac:groups="batch",resources=jobs;cronjobs,verbs=*
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=*
// +kubebuilder:rbac:groups="apiextensions.k8s.io",resources=customresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups="k8s.cni.cncf.io",resources=network-attachment-definitions,verbs=*

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MagmaAGW object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/reconcile
//
//nolint:gocyclo // Reconcile coordinates AGW lifecycle phases and keeps their status transitions in one ordered flow.
func (r *MagmaAGWReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var agw magmav1alpha1.MagmaAGW
	if err := r.Get(ctx, req.NamespacedName, &agw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	releaseName := agw.Spec.ReleaseName
	if releaseName == "" {
		releaseName = agw.Name
	}

	if !agw.DeletionTimestamp.IsZero() {
		return r.reconcileAGWDeletion(ctx, &agw, releaseName)
	}
	if !controllerutil.ContainsFinalizer(&agw, magmaAGWFinalizer) {
		patch := client.MergeFrom(agw.DeepCopy())
		controllerutil.AddFinalizer(&agw, magmaAGWFinalizer)
		if err := r.Patch(ctx, &agw, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	identity, err := r.reconcileAGWIdentity(ctx, &agw)
	if err != nil {
		log.Error(err, "failed to reconcile AGW identity")
		setAGWCondition(&agw, "IdentityReady", metav1.ConditionFalse, "IdentityReconcileFailed", err.Error())
		return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "IdentityReconcileFailed", err.Error())
	}
	updateAGWStatusDetails(&agw, identity)
	setAGWCondition(&agw, "IdentityReady", metav1.ConditionTrue, "IdentityReady", "AGW bootstrap identity is ready")

	values := map[string]string{
		"namespace":                              req.Namespace,
		"agwAntiAffinity.enabled":                stringTrue,
		"simulator.enabled":                      stringFalse,
		"simulator.antiAffinity.separateFromAgw": stringTrue,
	}
	setValue(values, "nodePrep.interfaces.s1.parent", agw.Spec.S1Interface)
	setValue(values, "nodePrep.interfaces.nat.parent", agw.Spec.SGiInterface)
	if identity != nil {
		values["config.gwChallenge"] = identity.PrivateKeyB64
		values["gatewayIdentity.snowflake"] = identity.HardwareID
	}

	agwNodeSelector := mergedSelectors(agw.Spec.AGWNodeSelector, agw.Spec.AGWNodeLabelSelector)
	datapathEnabled := datapathGateEnabled(agw.Spec.Datapath)
	datapathReady := true
	if datapathEnabled {
		if len(agwNodeSelector) == 0 {
			return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "AGWNodeSelectorRequired", "datapath gating requires spec.agwNodeSelector or spec.agwNodeLabelSelector")
		}
		ready, err := r.agwDatapathNodesReady(ctx, req.Namespace, agwNodeSelector, agw.Spec.Datapath)
		if err != nil {
			log.Error(err, "failed to evaluate AGW datapath node readiness")
			setAGWCondition(&agw, "DatapathReady", metav1.ConditionFalse, "DatapathReadinessCheckFailed", err.Error())
			return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "DatapathReadinessCheckFailed", err.Error())
		}
		datapathReady = ready
		if datapathReady {
			setAGWCondition(&agw, "DatapathReady", metav1.ConditionTrue, "DatapathReady", "AGW datapath nodes are prepared")
		} else {
			setAGWCondition(&agw, "DatapathReady", metav1.ConditionFalse, "WaitingForDatapathReady", "waiting for AGW datapath node preparation")
		}
		setSelectorValues(values, "nodePrep.nodeSelector", agwNodeSelector)
		workloadSelector := maps.Clone(agwNodeSelector)
		workloadSelector[datapathReadyLabelKey(agw.Spec.Datapath)] = datapathReadyLabelValue(agw.Spec.Datapath)
		setSelectorValues(values, "nodeSelector", workloadSelector)
		values["nodePrep.requireMagmaOvsKmod"] = boolString(agw.Spec.Datapath.RequireMagmaOvsKmod)
		setValue(values, "nodePrep.magmaOvsKmodUpgradePath", agw.Spec.Datapath.OvsKmodUpgradePath)
	} else {
		setSelectorValues(values, "nodeSelector", agwNodeSelector)
	}
	setSelectorValues(values, "simulator.nodeSelector", agw.Spec.UERANSIMNodeSelector)
	mergeValues(values, agw.Spec.Values)
	if identity != nil {
		values["config.gwChallenge"] = identity.PrivateKeyB64
		values["gatewayIdentity.snowflake"] = identity.HardwareID
	}
	values["simulator.enabled"] = stringFalse

	lifecycle, lifecycleReady, lifecycleReason, lifecycleMessage, err := r.reconcileAGWLifecyclePrereqs(ctx, &agw, releaseName, values)
	updateAGWLifecycleStatus(&agw, lifecycle)
	if err != nil {
		log.Error(err, "failed to reconcile AGW lifecycle prerequisites")
		setAGWCondition(&agw, "TrustBundleSynced", metav1.ConditionFalse, lifecycleReason, lifecycleMessage)
		return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, lifecycleReason, lifecycleMessage)
	}
	if !lifecycleReady {
		setAGWCondition(&agw, "TrustBundleSynced", metav1.ConditionFalse, lifecycleReason, lifecycleMessage)
		return r.updateAGWStatusWithRequeue(ctx, &agw, releaseName, metav1.ConditionFalse, lifecycleReason, lifecycleMessage, 20*time.Second)
	}
	setAGWCondition(&agw, "TrustBundleSynced", metav1.ConditionTrue, "TrustBundleSynced", "AGW trust bundle is synced from Orc8r")

	gatewayRegistered, gatewayRegistrationReason, err := r.reconcileAGWGatewayRegistration(ctx, &agw, identity)
	if err != nil {
		log.Error(err, "failed to reconcile AGW gateway registration")
		setAGWCondition(&agw, "GatewayRegistered", metav1.ConditionFalse, gatewayRegistrationReason, err.Error())
		return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, gatewayRegistrationReason, err.Error())
	}
	if identity != nil {
		identity.GatewayRegistered = gatewayRegistered
		updateAGWStatusDetails(&agw, identity)
	}
	if !gatewayRegistered && gatewayRegistrationEnabled(agw.Spec.GatewayRegistration) {
		setAGWCondition(&agw, "GatewayRegistered", metav1.ConditionFalse, gatewayRegistrationReason, "waiting for AGW identity registration in Orc8r/NMS")
		return r.updateAGWStatusWithRequeue(ctx, &agw, releaseName, metav1.ConditionFalse, gatewayRegistrationReason, "waiting for AGW identity registration in Orc8r/NMS", 20*time.Second)
	}
	if gatewayRegistered {
		setAGWCondition(&agw, "GatewayRegistered", metav1.ConditionTrue, "GatewayRegistered", "AGW is registered in Orc8r/NMS")
	} else {
		setAGWCondition(&agw, "GatewayRegistered", metav1.ConditionFalse, "GatewayRegistrationDisabled", "AGW gateway registration is disabled")
	}

	ueransimGatePending := false
	ueransimGateName := ""
	if agw.Spec.EnableUERANSIM {
		startPolicy := agw.Spec.UERANSIMStartPolicy
		if startPolicy == "" {
			startPolicy = ueransimStartPolicyAfterAGWReady
		}
		switch startPolicy {
		case ueransimStartPolicyImmediate:
			values["simulator.enabled"] = stringTrue
		case ueransimStartPolicyAfterAGWReady:
			gateReady, gateName, err := r.ueransimStartGateReady(ctx, req.Namespace, &agw, releaseName)
			if err != nil {
				log.Error(err, "failed to read UERANSIM readiness gate")
				return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "UERANSIMGateReadFailed", err.Error())
			}
			ueransimGateName = gateName
			if gateReady {
				values["simulator.enabled"] = stringTrue
			} else {
				ueransimGatePending = true
			}
		default:
			message := fmt.Sprintf("unsupported ueransimStartPolicy %q; use %q or %q", startPolicy, ueransimStartPolicyAfterAGWReady, ueransimStartPolicyImmediate)
			return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "InvalidUERANSIMStartPolicy", message)
		}
	}
	removeAGWCondition(&agw, "UERANSIMReady")

	err = reconcileNativeRelease(ctx, r.Client, r.Scheme, &agw, nativeRelease{
		ReleaseName: releaseName,
		Namespace:   req.Namespace,
		Manifest:    "agw.yaml",
		Values:      values,
		ApplyFilter: nativeAGWApplyFilter(values["simulator.enabled"] == stringTrue),
	})
	if err != nil {
		log.Error(err, "failed to reconcile Magma AGW resources")
		return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "NativeReconcileFailed", err.Error())
	}
	if err := r.annotateAGWCoreDeploymentsForRootCA(ctx, req.Namespace, releaseName, agw.Status.TrustBundleHash); err != nil {
		log.Error(err, "failed to annotate AGW deployments for root CA rollout")
		return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "RootCARolloutAnnotationFailed", err.Error())
	}
	if err := r.cleanupStaleAGWPods(ctx, req.Namespace, releaseName); err != nil {
		log.Error(err, "failed to clean up stale AGW pods")
		return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "StalePodCleanupFailed", err.Error())
	}

	if datapathEnabled && !datapathReady {
		datapathReadyNow, message, err := r.reconcileAGWDatapathLabels(ctx, req.Namespace, agwNodeSelector, agw.Spec.Datapath)
		if err != nil {
			log.Error(err, "failed to reconcile AGW datapath labels")
			return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "DatapathLabelReconcileFailed", err.Error())
		}
		if datapathReadyNow {
			return r.updateAGWStatusWithRequeue(ctx, &agw, releaseName, metav1.ConditionFalse, "DatapathReady", "AGW datapath node prep is ready; reconciling AGW workloads", 5*time.Second)
		}
		return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "WaitingForDatapathReady", message)
	}

	if ueransimGatePending {
		message := fmt.Sprintf("Magma AGW resources are ready with UERANSIM disabled. After AGW is registered with Orc8r and the UE is provisioned in NMS, create or update ConfigMap %s/%s with data ready=true.", req.Namespace, ueransimGateName)
		return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "WaitingForUERANSIMGate", message)
	}

	validationReady, validationReason, validationMessage, err := r.reconcileUERANSIMValidation(ctx, &agw, releaseName)
	if err != nil {
		log.Error(err, "failed to reconcile one-shot UERANSIM validation")
		setAGWCondition(&agw, "UERANSIMValidated", metav1.ConditionFalse, validationReason, validationMessage)
	} else if validationReason == "UERANSIMValidated" {
		setAGWCondition(&agw, "UERANSIMValidated", metav1.ConditionTrue, validationReason, validationMessage)
	} else if validationReason == "UERANSIMValidationFailed" {
		setAGWCondition(&agw, "UERANSIMValidated", metav1.ConditionFalse, validationReason, validationMessage)
	} else if !validationReady {
		setAGWCondition(&agw, "UERANSIMValidated", metav1.ConditionUnknown, validationReason, validationMessage)
	}
	if !validationReady {
		return r.updateAGWReadyStatusWithRequeue(ctx, &agw, releaseName, "NativeResourcesReady", "Magma AGW resources are ready", 15*time.Second)
	}

	return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionTrue, "NativeResourcesReady", "Magma AGW resources are ready")
}

func (r *MagmaAGWReconciler) ueransimStartGateReady(ctx context.Context, namespace string, agw *magmav1alpha1.MagmaAGW, releaseName string) (bool, string, error) {
	gateName := agw.Spec.UERANSIMReadyConfigMap
	if gateName == "" {
		gateName = releaseName + "-ueransim-ready"
	}
	var gate corev1.ConfigMap
	err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: gateName}, &gate)
	if apierrors.IsNotFound(err) {
		return false, gateName, nil
	}
	if err != nil {
		return false, gateName, err
	}
	return strings.EqualFold(gate.Data["ready"], stringTrue), gateName, nil
}

func (r *MagmaAGWReconciler) mapUERANSIMGateConfigMap(ctx context.Context, object client.Object) []reconcile.Request {
	var agwList magmav1alpha1.MagmaAGWList
	if err := r.List(ctx, &agwList, client.InNamespace(object.GetNamespace())); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(agwList.Items))
	for _, agw := range agwList.Items {
		if !agw.Spec.EnableUERANSIM {
			continue
		}
		releaseName := agw.Spec.ReleaseName
		if releaseName == "" {
			releaseName = agw.Name
		}
		gateName := agw.Spec.UERANSIMReadyConfigMap
		if gateName == "" {
			gateName = releaseName + "-ueransim-ready"
		}
		if gateName != object.GetName() {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKey{
				Namespace: agw.Namespace,
				Name:      agw.Name,
			},
		})
	}
	return requests
}

func (r *MagmaAGWReconciler) reconcileAGWDeletion(ctx context.Context, agw *magmav1alpha1.MagmaAGW, releaseName string) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(agw, magmaAGWFinalizer) {
		return ctrl.Result{}, nil
	}

	deregistered, reason, message, err := r.reconcileAGWGatewayDeregistration(ctx, agw)
	if err != nil {
		_, statusErr := r.updateAGWStatus(ctx, agw, releaseName, metav1.ConditionFalse, reason, err.Error())
		if statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	if !deregistered {
		_, statusErr := r.updateAGWStatus(ctx, agw, releaseName, metav1.ConditionFalse, reason, message)
		if statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
	}

	if err := deleteNativeRelease(ctx, r.Client, nativeRelease{ReleaseName: releaseName, Namespace: agw.Namespace, Manifest: "agw.yaml"}); err != nil {
		_, statusErr := r.updateAGWStatus(ctx, agw, releaseName, metav1.ConditionFalse, "NativeDeleteFailed", err.Error())
		if statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	agwNodeSelector := mergedSelectors(agw.Spec.AGWNodeSelector, agw.Spec.AGWNodeLabelSelector)
	if len(agwNodeSelector) > 0 {
		nodes, err := r.selectedAGWNodes(ctx, agwNodeSelector)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.removeAGWDatapathLabels(ctx, nodes, agw.Spec.Datapath); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.cleanupAGWDurableState(ctx, agw, releaseName); err != nil {
		return ctrl.Result{}, err
	}

	patch := client.MergeFrom(agw.DeepCopy())
	controllerutil.RemoveFinalizer(agw, magmaAGWFinalizer)
	return ctrl.Result{}, r.Patch(ctx, agw, patch)
}

func (r *MagmaAGWReconciler) cleanupAGWDurableState(ctx context.Context, agw *magmav1alpha1.MagmaAGW, releaseName string) error {
	if agw.Spec.DeletionPolicy.DeletePVC {
		pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: releaseName + "-claim", Namespace: agw.Namespace}}
		if err := r.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if agw.Spec.DeletionPolicy.DeleteIdentitySecret {
		secretName := agw.Spec.Identity.SecretName
		if secretName == "" {
			secretName = defaultAGWIdentitySecretName
		}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: agw.Namespace}}
		if err := r.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *MagmaAGWReconciler) cleanupStaleAGWPods(ctx context.Context, namespace, releaseName string) error {
	var pods corev1.PodList
	err := r.List(ctx, &pods,
		client.InNamespace(namespace),
		client.MatchingLabels{
			labelAppInstance: releaseName,
			labelAppName:     magmaAGWAppName,
		},
	)
	if err != nil {
		return err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Reason != "NodeAffinity" {
			continue
		}
		if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *MagmaAGWReconciler) reconcileAGWDatapathLabels(ctx context.Context, namespace string, selector map[string]string, datapath magmav1alpha1.MagmaAGWDatapathSpec) (bool, string, error) {
	nodes, err := r.selectedAGWNodes(ctx, selector)
	if err != nil {
		return false, "", err
	}
	if len(nodes.Items) == 0 {
		return false, "no nodes match the AGW datapath selector", nil
	}

	var daemonSet appsv1.DaemonSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agwNodePrepName}, &daemonSet); err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.removeAGWDatapathLabels(ctx, nodes, datapath); err != nil {
				return false, "", err
			}
			return false, "waiting for agw-node-prep DaemonSet to be created", nil
		}
		return false, "", err
	}
	if daemonSet.Status.DesiredNumberScheduled == 0 {
		if err := r.removeAGWDatapathLabels(ctx, nodes, datapath); err != nil {
			return false, "", err
		}
		return false, "agw-node-prep DaemonSet has no scheduled pods", nil
	}
	if daemonSet.Status.NumberReady < daemonSet.Status.DesiredNumberScheduled {
		if err := r.removeAGWDatapathLabels(ctx, nodes, datapath); err != nil {
			return false, "", err
		}
		message := fmt.Sprintf("waiting for agw-node-prep DaemonSet readiness: %d/%d ready", daemonSet.Status.NumberReady, daemonSet.Status.DesiredNumberScheduled)
		return false, message, nil
	}

	if err := r.labelAGWDatapathNodes(ctx, nodes, datapath); err != nil {
		return false, "", err
	}
	return true, "", nil
}

func (r *MagmaAGWReconciler) labelAGWDatapathNodes(ctx context.Context, nodes *corev1.NodeList, datapath magmav1alpha1.MagmaAGWDatapathSpec) error {
	key := datapathReadyLabelKey(datapath)
	value := datapathReadyLabelValue(datapath)
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if node.Labels == nil {
			node.Labels = map[string]string{}
		}
		if node.Labels[key] == value {
			continue
		}
		patch := client.MergeFrom(node.DeepCopy())
		node.Labels[key] = value
		if err := r.Patch(ctx, node, patch); err != nil {
			return err
		}
	}
	return nil
}

func (r *MagmaAGWReconciler) removeAGWDatapathLabels(ctx context.Context, nodes *corev1.NodeList, datapath magmav1alpha1.MagmaAGWDatapathSpec) error {
	key := datapathReadyLabelKey(datapath)
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if node.Labels == nil {
			continue
		}
		if _, ok := node.Labels[key]; !ok {
			continue
		}
		patch := client.MergeFrom(node.DeepCopy())
		delete(node.Labels, key)
		if err := r.Patch(ctx, node, patch); err != nil {
			return err
		}
	}
	return nil
}

func (r *MagmaAGWReconciler) agwDatapathNodesReady(ctx context.Context, namespace string, selector map[string]string, datapath magmav1alpha1.MagmaAGWDatapathSpec) (bool, error) {
	nodes, err := r.selectedAGWNodes(ctx, selector)
	if err != nil {
		return false, err
	}
	if len(nodes.Items) == 0 {
		return false, nil
	}
	key := datapathReadyLabelKey(datapath)
	value := datapathReadyLabelValue(datapath)
	for _, node := range nodes.Items {
		if node.Labels[key] != value {
			return false, nil
		}
	}

	var daemonSet appsv1.DaemonSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agwNodePrepName}, &daemonSet); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if daemonSet.Status.DesiredNumberScheduled == 0 {
		return false, nil
	}
	if daemonSet.Status.NumberReady < daemonSet.Status.DesiredNumberScheduled {
		return false, nil
	}

	return true, nil
}

func (r *MagmaAGWReconciler) selectedAGWNodes(ctx context.Context, selector map[string]string) (*corev1.NodeList, error) {
	var nodes corev1.NodeList
	err := r.List(ctx, &nodes, &client.ListOptions{LabelSelector: labels.SelectorFromSet(selector)})
	if err != nil {
		return nil, err
	}
	return &nodes, nil
}

func (r *MagmaAGWReconciler) updateAGWStatus(ctx context.Context, agw *magmav1alpha1.MagmaAGW, releaseName string, status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {
	return r.updateAGWStatusWithRequeue(ctx, agw, releaseName, status, reason, message, 2*time.Minute)
}

func (r *MagmaAGWReconciler) updateAGWStatusWithRequeue(ctx context.Context, agw *magmav1alpha1.MagmaAGW, releaseName string, status metav1.ConditionStatus, reason, message string, requeueAfter time.Duration) (ctrl.Result, error) {
	agw.Status.ObservedGeneration = agw.Generation
	agw.Status.ReleaseName = releaseName
	agw.Status.ReleaseNamespace = agw.Namespace
	setAGWCondition(agw, "Ready", status, reason, message)
	if err := r.Status().Update(ctx, agw); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}
	if status == metav1.ConditionTrue {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func (r *MagmaAGWReconciler) updateAGWReadyStatusWithRequeue(ctx context.Context, agw *magmav1alpha1.MagmaAGW, releaseName, reason, message string, requeueAfter time.Duration) (ctrl.Result, error) {
	agw.Status.ObservedGeneration = agw.Generation
	agw.Status.ReleaseName = releaseName
	agw.Status.ReleaseNamespace = agw.Namespace
	setAGWCondition(agw, "Ready", metav1.ConditionTrue, reason, message)
	if err := r.Status().Update(ctx, agw); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func setAGWCondition(agw *magmav1alpha1.MagmaAGW, conditionType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&agw.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            conditionMessage(message),
		ObservedGeneration: agw.Generation,
	})
}

func removeAGWCondition(agw *magmav1alpha1.MagmaAGW, conditionType string) {
	apimeta.RemoveStatusCondition(&agw.Status.Conditions, conditionType)
}

func mergedSelectors(selectors ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, selector := range selectors {
		maps.Copy(merged, selector)
	}
	return merged
}

func datapathGateEnabled(datapath magmav1alpha1.MagmaAGWDatapathSpec) bool {
	if datapath.Enabled == nil {
		return true
	}
	return *datapath.Enabled
}

func datapathReadyLabelKey(datapath magmav1alpha1.MagmaAGWDatapathSpec) string {
	if datapath.ReadyLabelKey != "" {
		return datapath.ReadyLabelKey
	}
	return defaultDatapathReadyLabelKey
}

func datapathReadyLabelValue(datapath magmav1alpha1.MagmaAGWDatapathSpec) string {
	if datapath.ReadyLabelValue != "" {
		return datapath.ReadyLabelValue
	}
	return defaultDatapathReadyLabelValue
}

func boolString(value bool) string {
	if value {
		return stringTrue
	}
	return stringFalse
}

// SetupWithManager sets up the controller with the Manager.
func (r *MagmaAGWReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&magmav1alpha1.MagmaAGW{}).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.mapUERANSIMGateConfigMap)).
		Named("magmaagw").
		Complete(r)
}
