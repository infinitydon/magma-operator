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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	magmav1alpha1 "github.com/infinitydon/magma-operator/api/v1alpha1"
)

const magmaOrc8rFinalizer = "magma.infra.don/magmaorc8r-finalizer"

// MagmaOrc8rReconciler reconciles a MagmaOrc8r object
type MagmaOrc8rReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=magma.infra.don,resources=magmaorc8rs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=magma.infra.don,resources=magmaorc8rs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=magma.infra.don,resources=magmaorc8rs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces;secrets;services;configmaps;serviceaccounts;persistentvolumeclaims;pods;pods/log,verbs=*
// +kubebuilder:rbac:groups="apps",resources=deployments;statefulsets;daemonsets;replicasets,verbs=*
// +kubebuilder:rbac:groups="batch",resources=jobs;cronjobs,verbs=*
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=*
// +kubebuilder:rbac:groups="apiextensions.k8s.io",resources=customresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups="k8s.cni.cncf.io",resources=network-attachment-definitions,verbs=*

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MagmaOrc8r object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/reconcile
func (r *MagmaOrc8rReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var orc8r magmav1alpha1.MagmaOrc8r
	if err := r.Get(ctx, req.NamespacedName, &orc8r); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	releaseName := orc8r.Spec.ReleaseName
	if releaseName == "" {
		releaseName = orc8r.Name
	}
	chartPath := orc8r.Spec.ChartPath
	if chartPath == "" {
		chartPath = magmaOrc8rChartName
	}
	if !orc8r.DeletionTimestamp.IsZero() {
		return r.reconcileOrc8rDeletion(ctx, &orc8r, releaseName)
	}
	if !controllerutil.ContainsFinalizer(&orc8r, magmaOrc8rFinalizer) {
		patch := client.MergeFrom(orc8r.DeepCopy())
		controllerutil.AddFinalizer(&orc8r, magmaOrc8rFinalizer)
		if err := r.Patch(ctx, &orc8r, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	values := map[string]string{
		"orc8r.nms.magmalte.service.type": "NodePort",
		"orc8r.nms.nginx.create":          stringFalse,
	}
	setValue(values, "global.domainName", orc8r.Spec.DomainName)
	setValue(values, "orc8r.controller.image.env.orc8r_domain_name", orc8r.Spec.DomainName)
	setValue(values, "lte-orc8r.controller.image.env.orc8r_domain_name", orc8r.Spec.DomainName)
	setValue(values, "orc8r.nms.controllerHostname", orc8r.Spec.ControllerHostname)
	setValue(values, "orc8r.nginx.spec.hostname", orc8r.Spec.ControllerHostname)
	setValue(values, "nmsAdmin.organization", orc8r.Spec.NMSOrganization)
	setValue(values, "nmsAdmin.email", orc8r.Spec.NMSAdminEmail)
	setValue(values, "nmsAdmin.password", orc8r.Spec.NMSAdminPassword)
	setListValues(values, "nmsAdmin.customDomains", orc8r.Spec.NMSCustomDomains)
	setValue(values, "provisioning.network.id", orc8r.Spec.NetworkID)
	setValue(values, "provisioning.network.name", orc8r.Spec.NetworkName)
	setValue(values, "provisioning.subscriber.imsi", orc8r.Spec.SubscriberIMSI)
	if orc8r.Spec.NMSNodePort != nil {
		values["orc8r.nms.magmalte.service.http.nodePort"] = fmt.Sprint(*orc8r.Spec.NMSNodePort)
	}
	mergeValues(values, orc8r.Spec.Values)

	err := reconcileHelmRelease(ctx, helmRelease{
		ReleaseName: releaseName,
		Namespace:   req.Namespace,
		Repo:        orc8r.Spec.ChartRepository,
		Revision:    orc8r.Spec.ChartRevision,
		ChartPath:   chartPath,
		Values:      values,
		Wait:        true,
	})
	if err != nil {
		log.Error(err, "failed to reconcile Magma Orc8r release")
		return r.updateOrc8rStatus(ctx, &orc8r, releaseName, metav1.ConditionFalse, "HelmReconcileFailed", err.Error())
	}
	if orc8r.Spec.NMSNodePort != nil {
		if err := r.patchMagmalteForHTTPNodePort(ctx, req.Namespace); err != nil {
			log.Error(err, "failed to patch MagmaLTE HTTP NodePort mode")
			return r.updateOrc8rStatus(ctx, &orc8r, releaseName, metav1.ConditionFalse, "MagmaltePatchFailed", err.Error())
		}
	}

	return r.updateOrc8rStatus(ctx, &orc8r, releaseName, metav1.ConditionTrue, "HelmReleaseReady", "Magma Orc8r Helm release is ready")
}

func (r *MagmaOrc8rReconciler) reconcileOrc8rDeletion(ctx context.Context, orc8r *magmav1alpha1.MagmaOrc8r, releaseName string) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(orc8r, magmaOrc8rFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := uninstallHelmRelease(ctx, releaseName, orc8r.Namespace); err != nil {
		_, statusErr := r.updateOrc8rStatus(ctx, orc8r, releaseName, metav1.ConditionFalse, "HelmUninstallFailed", err.Error())
		if statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	patch := client.MergeFrom(orc8r.DeepCopy())
	controllerutil.RemoveFinalizer(orc8r, magmaOrc8rFinalizer)
	return ctrl.Result{}, r.Patch(ctx, orc8r, patch)
}

func (r *MagmaOrc8rReconciler) patchMagmalteForHTTPNodePort(ctx context.Context, namespace string) error {
	var deploy appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: magmalteDeploymentName}, &deploy); err != nil {
		return err
	}
	if len(deploy.Spec.Template.Spec.Containers) == 0 {
		return nil
	}
	container := &deploy.Spec.Template.Spec.Containers[0]
	container.Command = []string{"node"}
	container.Args = []string{"-r", "./babelRegister.js", "scripts/server"}
	setEnv(container, "NODE_ENV", "development")
	return r.Update(ctx, &deploy)
}

func setEnv(container *corev1.Container, name, value string) {
	for i := range container.Env {
		if container.Env[i].Name == name {
			container.Env[i].Value = value
			return
		}
	}
	container.Env = append(container.Env, corev1.EnvVar{Name: name, Value: value})
}

func (r *MagmaOrc8rReconciler) updateOrc8rStatus(ctx context.Context, orc8r *magmav1alpha1.MagmaOrc8r, releaseName string, status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {
	orc8r.Status.ObservedGeneration = orc8r.Generation
	orc8r.Status.ReleaseName = releaseName
	orc8r.Status.ReleaseNamespace = orc8r.Namespace
	orc8r.Status.NMSURL = "http://<node-ip>:<magmalte-nodeport>/user/login"
	if orc8r.Spec.NMSNodePort != nil {
		orc8r.Status.NMSURL = fmt.Sprintf("http://<node-ip>:%d/user/login", *orc8r.Spec.NMSNodePort)
	}
	apimeta.SetStatusCondition(&orc8r.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            conditionMessage(message),
		ObservedGeneration: orc8r.Generation,
	})
	if err := r.Status().Update(ctx, orc8r); err != nil {
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
func (r *MagmaOrc8rReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&magmav1alpha1.MagmaOrc8r{}).
		Named("magmaorc8r").
		Complete(r)
}
