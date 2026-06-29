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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	magmav1alpha1 "github.com/infinitydon/magma-operator/api/v1alpha1"
)

const (
	ueransimStartPolicyAfterAGWReady = "AfterAGWReady"
	ueransimStartPolicyImmediate     = "Immediate"
)

// MagmaAGWReconciler reconciles a MagmaAGW object
type MagmaAGWReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=magma.infra.don,resources=magmaagws,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=magma.infra.don,resources=magmaagws/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=magma.infra.don,resources=magmaagws/finalizers,verbs=update
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
	setValue(values, "config.gwChallenge", agw.Spec.AccessGatewayID)
	setValue(values, "nodePrep.interfaces.s1.parent", agw.Spec.S1Interface)
	setValue(values, "nodePrep.interfaces.nat.parent", agw.Spec.SGiInterface)
	setSelectorValues(values, "nodeSelector", agw.Spec.AGWNodeSelector)
	setSelectorValues(values, "nodeSelector", agw.Spec.AGWNodeLabelSelector)
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
	})
	if err != nil {
		log.Error(err, "failed to reconcile Magma AGW release")
		return r.updateAGWStatus(ctx, &agw, releaseName, metav1.ConditionFalse, "HelmReconcileFailed", err.Error())
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

func (r *MagmaAGWReconciler) updateAGWStatus(ctx context.Context, agw *magmav1alpha1.MagmaAGW, releaseName string, status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {
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
	return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MagmaAGWReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&magmav1alpha1.MagmaAGW{}).
		Named("magmaagw").
		Complete(r)
}
