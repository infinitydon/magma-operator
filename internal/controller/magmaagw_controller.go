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
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	magmav1alpha1 "github.com/infinitydon/magma-operator/api/v1alpha1"
)

const (
	ueransimStartPolicyAfterAGWReady = "AfterAGWReady"
	ueransimStartPolicyImmediate     = "Immediate"
	defaultDatapathReadyLabelKey     = "magma.io/agw-datapath-ready"
	defaultDatapathReadyLabelValue   = stringTrue
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
	chartPath := agw.Spec.ChartPath
	if chartPath == "" {
		chartPath = "magma-agw-upstream"
	}

	values := map[string]string{
		"namespace":                              req.Namespace,
		"agwAntiAffinity.enabled":                stringTrue,
		"simulator.enabled":                      stringFalse,
		"simulator.antiAffinity.separateFromAgw": stringTrue,
	}
	setValue(values, "nodePrep.interfaces.s1.parent", agw.Spec.S1Interface)
	setValue(values, "nodePrep.interfaces.nat.parent", agw.Spec.SGiInterface)

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
			return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "DatapathReadinessCheckFailed", err.Error())
		}
		datapathReady = ready
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
	values["simulator.enabled"] = stringFalse

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

	err := reconcileHelmRelease(ctx, helmRelease{
		ReleaseName: releaseName,
		Namespace:   req.Namespace,
		Repo:        agw.Spec.ChartRepository,
		Revision:    agw.Spec.ChartRevision,
		ChartPath:   chartPath,
		Values:      values,
		Wait:        !datapathEnabled || datapathReady,
	})
	if err != nil {
		log.Error(err, "failed to reconcile Magma AGW release")
		return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "HelmReconcileFailed", err.Error())
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
		message := fmt.Sprintf("Magma AGW Helm release is ready with UERANSIM disabled. After AGW is registered with Orc8r and the UE is provisioned in NMS, create or update ConfigMap %s/%s with data ready=true.", req.Namespace, ueransimGateName)
		return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "WaitingForUERANSIMGate", message)
	}

	return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionTrue, "HelmReleaseReady", "Magma AGW Helm release is ready")
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

func (r *MagmaAGWReconciler) reconcileAGWDatapathLabels(ctx context.Context, namespace string, selector map[string]string, datapath magmav1alpha1.MagmaAGWDatapathSpec) (bool, string, error) {
	nodes, err := r.selectedAGWNodes(ctx, selector)
	if err != nil {
		return false, "", err
	}
	if len(nodes.Items) == 0 {
		return false, "no nodes match the AGW datapath selector", nil
	}

	var daemonSet appsv1.DaemonSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "agw-node-prep"}, &daemonSet); err != nil {
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
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "agw-node-prep"}, &daemonSet); err != nil {
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
	apimeta.SetStatusCondition(&agw.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            conditionMessage(message),
		ObservedGeneration: agw.Generation,
	})
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
		Named("magmaagw").
		Complete(r)
}
