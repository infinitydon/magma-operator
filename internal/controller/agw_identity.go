package controller

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
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
	defaultAGWIdentitySecretName    = "agw-bootstrap-challenge-key"
	defaultAGWChallengeKeySecretKey = "gw_challenge.key"
	defaultAGWRegistrationTier      = "default"
	defaultOrc8rNamespace           = "magma"
	defaultOrc8rReleaseName         = "magma-fullstack"
	defaultNMSAdminCertSecretName   = "orc8r-secrets-certs"
	agwIdentityManagedLabel         = "magma.infra.don/identity"
	agwIdentityManagedValue         = "agw"
)

type agwIdentityState struct {
	SecretName        string
	HardwareID        string
	PrivateKeyB64     string
	PublicKeyB64      string
	PublicKeyHash     string
	GatewayRegistered bool
}

func (r *MagmaAGWReconciler) reconcileAGWIdentity(ctx context.Context, agw *magmav1alpha1.MagmaAGW) (*agwIdentityState, error) {
	secretName := agw.Spec.Identity.SecretName
	if secretName == "" {
		secretName = defaultAGWIdentitySecretName
	}

	privateKeyPEM, err := r.desiredAGWPrivateKey(ctx, agw)
	if err != nil {
		return nil, err
	}

	var secret corev1.Secret
	key := types.NamespacedName{Namespace: agw.Namespace, Name: secretName}
	secret.Name = key.Name
	secret.Namespace = key.Namespace
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, &secret, func() error {
		secret.Type = corev1.SecretTypeOpaque
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		secret.Labels[labelAppManagedBy] = managedByMagmaOperator
		secret.Labels[agwIdentityManagedLabel] = agwIdentityManagedValue
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		if privateKeyPEM == nil || strings.EqualFold(agw.Spec.Identity.RotationPolicy, "Rotate") {
			privateKeyPEM = secret.Data[defaultAGWChallengeKeySecretKey]
		}
		if len(privateKeyPEM) == 0 {
			generated, err := generateECDSAPrivateKeyPEM()
			if err != nil {
				return err
			}
			privateKeyPEM = generated
		}
		hardwareID := agw.Spec.Identity.HardwareID
		if hardwareID == "" {
			hardwareID = agw.Spec.Values["gatewayIdentity.snowflake"]
		}
		if hardwareID == "" {
			hardwareID = string(secret.Data["hardware_id"])
		}
		if hardwareID == "" {
			var err error
			hardwareID, err = newHardwareID()
			if err != nil {
				return err
			}
		}
		publicKeyB64, err := publicKeyBase64DER(privateKeyPEM)
		if err != nil {
			return err
		}
		publicKeyHash := sha256Hex([]byte(publicKeyB64))
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[defaultAGWChallengeKeySecretKey] = privateKeyPEM
		secret.Data["gw_challenge.key.b64"] = []byte(base64.StdEncoding.EncodeToString(privateKeyPEM))
		secret.Data["challenge_public_key.b64"] = []byte(publicKeyB64)
		secret.Data["challenge_public_key.sha256"] = []byte(publicKeyHash)
		secret.Data["hardware_id"] = []byte(hardwareID)
		secret.Annotations["magma.infra.don/access-gateway-id"] = agw.Spec.AccessGatewayID
		secret.Annotations["magma.infra.don/hardware-id"] = hardwareID
		secret.Annotations["magma.infra.don/challenge-public-key-sha256"] = publicKeyHash
		return controllerutil.SetControllerReference(agw, &secret, r.Scheme)
	})
	if err != nil {
		return nil, err
	}
	if err := r.Get(ctx, key, &secret); err != nil {
		return nil, err
	}
	privateKeyPEM = secret.Data[defaultAGWChallengeKeySecretKey]
	publicKeyB64, err := publicKeyBase64DER(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	publicKeyHash := sha256Hex([]byte(publicKeyB64))
	return &agwIdentityState{
		SecretName:    secretName,
		HardwareID:    string(secret.Data["hardware_id"]),
		PrivateKeyB64: base64.StdEncoding.EncodeToString(privateKeyPEM),
		PublicKeyB64:  publicKeyB64,
		PublicKeyHash: publicKeyHash,
	}, nil
}

func (r *MagmaAGWReconciler) desiredAGWPrivateKey(ctx context.Context, agw *magmav1alpha1.MagmaAGW) ([]byte, error) {
	if refName := agw.Spec.Identity.ImportSecretName; refName != "" {
		refKey := agw.Spec.Identity.ImportSecretKey
		if refKey == "" {
			refKey = defaultAGWChallengeKeySecretKey
		}
		var source corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Namespace: agw.Namespace, Name: refName}, &source); err != nil {
			return nil, fmt.Errorf("read imported AGW challenge key secret %s/%s: %w", agw.Namespace, refName, err)
		}
		value := source.Data[refKey]
		if len(value) == 0 {
			return nil, fmt.Errorf("imported AGW challenge key secret %s/%s has no data[%q]", agw.Namespace, refName, refKey)
		}
		return normalizePrivateKeyPEM(value)
	}
	if encoded := agw.Spec.Values["config.gwChallenge"]; encoded != "" {
		value, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode legacy spec.values.config.gwChallenge: %w", err)
		}
		return normalizePrivateKeyPEM(value)
	}
	return nil, nil
}

func (r *MagmaAGWReconciler) reconcileAGWGatewayRegistration(ctx context.Context, agw *magmav1alpha1.MagmaAGW, identity *agwIdentityState) (bool, string, error) {
	if !gatewayRegistrationEnabled(agw.Spec.GatewayRegistration) {
		return false, "GatewayRegistrationDisabled", nil
	}
	if agw.Spec.NetworkID == "" {
		return false, "NetworkIDRequired", fmt.Errorf("gateway registration requires spec.networkID")
	}
	if agw.Spec.AccessGatewayID == "" {
		return false, "AccessGatewayIDRequired", fmt.Errorf("gateway registration requires spec.accessGatewayID")
	}

	namespace := agw.Spec.Orc8rNamespace
	if namespace == "" {
		namespace = defaultOrc8rNamespace
	}
	releaseName := agw.Spec.Orc8rReleaseName
	if releaseName == "" {
		releaseName = defaultOrc8rReleaseName
	}
	secretName := agw.Spec.NMSAdminCertSecretName
	if secretName == "" {
		secretName = defaultNMSAdminCertSecretName
	}
	apiHost := agw.Spec.NMSAPIHost
	if apiHost == "" {
		apiHost = releaseName + "-nginx-proxy"
	}

	var certSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: secretName}, &certSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "WaitingForNMSCertSecret", nil
		}
		return false, "NMSCertSecretReadFailed", err
	}
	var magmalte appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: magmalteDeploymentName}, &magmalte); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "WaitingForMagmalte", nil
		}
		return false, "MagmalteReadFailed", err
	}
	if len(magmalte.Spec.Template.Spec.Containers) == 0 {
		return false, "MagmalteImageUnavailable", nil
	}

	jobName := agw.Name + "-register-gateway"
	payload, err := agwGatewayPayload(agw, identity)
	if err != nil {
		return false, "GatewayPayloadInvalid", err
	}
	payloadHash := sha256Hex(payload)

	var existing batchv1.Job
	err = r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: jobName}, &existing)
	if err == nil {
		if existing.Annotations["magma.infra.don/payload-sha256"] != payloadHash {
			if err := r.Delete(ctx, &existing, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
				return false, "GatewayRegistrationJobDeleteFailed", err
			}
			return false, "GatewayRegistrationJobReplacing", nil
		}
		if existing.Status.Succeeded > 0 {
			return true, "GatewayRegistered", nil
		}
		if existing.Status.Failed > 0 {
			return false, "GatewayRegistrationJobFailed", fmt.Errorf("gateway registration job %s/%s failed", namespace, jobName)
		}
		return false, "GatewayRegistrationJobRunning", nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return false, "GatewayRegistrationJobReadFailed", err
	}

	job := registrationJob(namespace, jobName, magmalte.Spec.Template.Spec.Containers[0].Image, secretName, apiHost, agw.Spec.NetworkID, string(payload), payloadHash)
	if err := r.Create(ctx, job); err != nil {
		return false, "GatewayRegistrationJobCreateFailed", err
	}
	return false, "GatewayRegistrationJobCreated", nil
}

func registrationJob(namespace, name, image, certSecret, apiHost, networkID, payload, payloadHash string) *batchv1.Job {
	ttl := int32(3600)
	backoff := int32(6)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				labelAppComponent: "agw-registration",
				labelAppManagedBy: managedByMagmaOperator,
			},
			Annotations: map[string]string{
				"magma.infra.don/payload-sha256": payloadHash,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{{
						Name:            "register",
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"/bin/sh", shellExitOnErrorCommand},
						Args: []string{`until curl -sk --cert "$API_CERT_FILENAME" --key "$API_PRIVATE_KEY_FILENAME" "https://$API_HOST/magma/v1/networks" >/dev/null; do sleep 5; done
until [ "$(curl -sk -o /tmp/network-get.out -w '%{http_code}' --cert "$API_CERT_FILENAME" --key "$API_PRIVATE_KEY_FILENAME" "https://$API_HOST/magma/v1/networks/$NETWORK_ID")" = "200" ]; do sleep 5; done
cat > /tmp/tier.json <<EOF
{"id":"$GATEWAY_TIER","version":"1.0.0","images":[],"gateways":[]}
EOF
tier_code="$(curl -sk -o /tmp/tier-get.out -w '%{http_code}' --cert "$API_CERT_FILENAME" --key "$API_PRIVATE_KEY_FILENAME" "https://$API_HOST/magma/v1/networks/$NETWORK_ID/tiers/$GATEWAY_TIER")"
if [ "$tier_code" = "200" ]; then
  curl -skf -X PUT --cert "$API_CERT_FILENAME" --key "$API_PRIVATE_KEY_FILENAME" -H "Content-Type: application/json" --data @/tmp/tier.json "https://$API_HOST/magma/v1/networks/$NETWORK_ID/tiers/$GATEWAY_TIER" >/dev/null
else
  curl -skf -X POST --cert "$API_CERT_FILENAME" --key "$API_PRIVATE_KEY_FILENAME" -H "Content-Type: application/json" --data @/tmp/tier.json "https://$API_HOST/magma/v1/networks/$NETWORK_ID/tiers" >/dev/null
fi
cat > /tmp/gateway.json <<EOF
$GATEWAY_PAYLOAD
EOF
code="$(curl -sk -o /tmp/gateway-get.out -w '%{http_code}' --cert "$API_CERT_FILENAME" --key "$API_PRIVATE_KEY_FILENAME" "https://$API_HOST/magma/v1/lte/$NETWORK_ID/gateways/$GATEWAY_ID")"
if [ "$code" = "200" ]; then
  exit 0
fi
generic_code="$(curl -sk -o /tmp/generic-gateway-get.out -w '%{http_code}' --cert "$API_CERT_FILENAME" --key "$API_PRIVATE_KEY_FILENAME" "https://$API_HOST/magma/v1/networks/$NETWORK_ID/gateways/$GATEWAY_ID")"
if [ "$generic_code" = "200" ]; then
  curl -skf -X DELETE --cert "$API_CERT_FILENAME" --key "$API_PRIVATE_KEY_FILENAME" "https://$API_HOST/magma/v1/networks/$NETWORK_ID/gateways/$GATEWAY_ID" >/dev/null
fi
curl -skf -X POST --cert "$API_CERT_FILENAME" --key "$API_PRIVATE_KEY_FILENAME" -H "Content-Type: application/json" --data @/tmp/gateway.json "https://$API_HOST/magma/v1/lte/$NETWORK_ID/gateways" >/dev/null
curl -skf --cert "$API_CERT_FILENAME" --key "$API_PRIVATE_KEY_FILENAME" "https://$API_HOST/magma/v1/lte/$NETWORK_ID/gateways/$GATEWAY_ID" >/dev/null`},
						Env: []corev1.EnvVar{
							{Name: "API_HOST", Value: apiHost},
							{Name: "NETWORK_ID", Value: networkID},
							{Name: "GATEWAY_ID", Value: gatewayIDFromPayload(payload)},
							{Name: "GATEWAY_TIER", Value: gatewayTierFromPayload(payload)},
							{Name: "GATEWAY_PAYLOAD", Value: payload},
							{Name: "API_CERT_FILENAME", Value: adminOperatorCertPath},
							{Name: "API_PRIVATE_KEY_FILENAME", Value: adminOperatorKeyPath},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: defaultNMSAdminCertSecretName, MountPath: adminOperatorCertPath, SubPath: adminOperatorCertKey, ReadOnly: true},
							{Name: defaultNMSAdminCertSecretName, MountPath: adminOperatorKeyPath, SubPath: adminOperatorKeyKey, ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: defaultNMSAdminCertSecretName,
						VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
							SecretName:  certSecret,
							DefaultMode: ptr.To[int32](0444),
						}},
					}},
				},
			},
		},
	}
}

func (r *MagmaAGWReconciler) reconcileAGWGatewayDeregistration(ctx context.Context, agw *magmav1alpha1.MagmaAGW) (bool, string, string, error) {
	if !agw.Spec.GatewayRegistration.DeleteOnRemoval {
		return true, "GatewayDeregistrationSkipped", "gatewayRegistration.deleteOnRemoval is false", nil
	}
	if !gatewayRegistrationEnabled(agw.Spec.GatewayRegistration) {
		return true, "GatewayDeregistrationSkipped", "gateway registration is disabled", nil
	}
	if agw.Spec.NetworkID == "" || agw.Spec.AccessGatewayID == "" {
		return false, "GatewayDeregistrationInvalid", "gateway deregistration requires spec.networkID and spec.accessGatewayID", fmt.Errorf("gateway deregistration requires spec.networkID and spec.accessGatewayID")
	}

	namespace := agw.Spec.Orc8rNamespace
	if namespace == "" {
		namespace = defaultOrc8rNamespace
	}
	releaseName := agw.Spec.Orc8rReleaseName
	if releaseName == "" {
		releaseName = defaultOrc8rReleaseName
	}
	secretName := agw.Spec.NMSAdminCertSecretName
	if secretName == "" {
		secretName = defaultNMSAdminCertSecretName
	}
	apiHost := agw.Spec.NMSAPIHost
	if apiHost == "" {
		apiHost = releaseName + "-nginx-proxy"
	}

	var certSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: secretName}, &certSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "WaitingForNMSCertSecret", fmt.Sprintf("waiting for NMS cert Secret %s/%s before gateway deregistration", namespace, secretName), nil
		}
		return false, "NMSCertSecretReadFailed", err.Error(), err
	}
	var magmalte appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: magmalteDeploymentName}, &magmalte); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "WaitingForMagmalte", "waiting for magmalte before gateway deregistration", nil
		}
		return false, "MagmalteReadFailed", err.Error(), err
	}
	if len(magmalte.Spec.Template.Spec.Containers) == 0 {
		return false, "MagmalteImageUnavailable", "magmalte deployment has no containers", nil
	}

	jobName := agw.Name + "-delete-gateway"
	var existing batchv1.Job
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: jobName}, &existing)
	if err == nil {
		if existing.Status.Succeeded > 0 {
			return true, "GatewayDeregistered", "AGW gateway record was removed from Orc8r/NMS", nil
		}
		if existing.Status.Failed > 0 {
			return false, "GatewayDeregistrationJobFailed", fmt.Sprintf("gateway deregistration job %s/%s failed", namespace, jobName), fmt.Errorf("gateway deregistration job %s/%s failed", namespace, jobName)
		}
		return false, "GatewayDeregistrationJobRunning", fmt.Sprintf("waiting for gateway deregistration job %s/%s", namespace, jobName), nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return false, "GatewayDeregistrationJobReadFailed", err.Error(), err
	}

	job := deregistrationJob(namespace, jobName, magmalte.Spec.Template.Spec.Containers[0].Image, secretName, apiHost, agw.Spec.NetworkID, agw.Spec.AccessGatewayID)
	if err := r.Create(ctx, job); err != nil {
		return false, "GatewayDeregistrationJobCreateFailed", err.Error(), err
	}
	return false, "GatewayDeregistrationJobCreated", fmt.Sprintf("created gateway deregistration job %s/%s", namespace, jobName), nil
}

func deregistrationJob(namespace, name, image, certSecret, apiHost, networkID, gatewayID string) *batchv1.Job {
	ttl := int32(3600)
	backoff := int32(6)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				labelAppComponent: "agw-deregistration",
				labelAppManagedBy: managedByMagmaOperator,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{{
						Name:            "deregister",
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"/bin/sh", shellExitOnErrorCommand},
						Args: []string{`until curl -sk --cert "$API_CERT_FILENAME" --key "$API_PRIVATE_KEY_FILENAME" "https://$API_HOST/magma/v1/networks" >/dev/null; do sleep 5; done
curl -skf -X DELETE --cert "$API_CERT_FILENAME" --key "$API_PRIVATE_KEY_FILENAME" "https://$API_HOST/magma/v1/lte/$NETWORK_ID/gateways/$GATEWAY_ID" >/dev/null || true
curl -skf -X DELETE --cert "$API_CERT_FILENAME" --key "$API_PRIVATE_KEY_FILENAME" "https://$API_HOST/magma/v1/networks/$NETWORK_ID/gateways/$GATEWAY_ID" >/dev/null || true`},
						Env: []corev1.EnvVar{
							{Name: "API_HOST", Value: apiHost},
							{Name: "NETWORK_ID", Value: networkID},
							{Name: "GATEWAY_ID", Value: gatewayID},
							{Name: "API_CERT_FILENAME", Value: adminOperatorCertPath},
							{Name: "API_PRIVATE_KEY_FILENAME", Value: adminOperatorKeyPath},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: defaultNMSAdminCertSecretName, MountPath: adminOperatorCertPath, SubPath: adminOperatorCertKey, ReadOnly: true},
							{Name: defaultNMSAdminCertSecretName, MountPath: adminOperatorKeyPath, SubPath: adminOperatorKeyKey, ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: defaultNMSAdminCertSecretName,
						VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
							SecretName:  certSecret,
							DefaultMode: ptr.To[int32](0444),
						}},
					}},
				},
			},
		},
	}
}

func agwGatewayPayload(agw *magmav1alpha1.MagmaAGW, identity *agwIdentityState) ([]byte, error) {
	name := agw.Spec.GatewayRegistration.Name
	if name == "" {
		name = agw.Spec.AccessGatewayID
	}
	description := agw.Spec.GatewayRegistration.Description
	if description == "" {
		description = "Managed by magma-operator"
	}
	tier := agw.Spec.GatewayRegistration.Tier
	if tier == "" {
		tier = defaultAGWRegistrationTier
	}
	payload := map[string]any{
		"id":          agw.Spec.AccessGatewayID,
		"name":        name,
		"description": description,
		"tier":        tier,
		"device": map[string]any{
			"hardware_id": identity.HardwareID,
			"key": map[string]any{
				"key":      identity.PublicKeyB64,
				"key_type": "SOFTWARE_ECDSA_SHA256",
			},
		},
		"magmad": map[string]any{
			"autoupgrade_enabled":       true,
			"autoupgrade_poll_interval": 60,
			"checkin_interval":          60,
			"checkin_timeout":           30,
			"dynamic_services":          []string{},
		},
		"cellular": map[string]any{
			"epc": map[string]any{
				"ip_block":      "192.168.128.0/24",
				"nat_enabled":   true,
				"dns_primary":   "8.8.8.8",
				"dns_secondary": "8.8.4.4",
			},
			"ran": map[string]any{
				"pci":              260,
				"transmit_enabled": true,
			},
		},
		"connected_enodeb_serials": []string{},
	}
	return json.Marshal(payload)
}

func gatewayRegistrationEnabled(spec magmav1alpha1.MagmaAGWGatewayRegistrationSpec) bool {
	return spec.Enabled == nil || *spec.Enabled
}

func gatewayIDFromPayload(payload string) string {
	var data struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(payload), &data)
	return data.ID
}

func gatewayTierFromPayload(payload string) string {
	var data struct {
		Tier string `json:"tier"`
	}
	_ = json.Unmarshal([]byte(payload), &data)
	if data.Tier == "" {
		return defaultAGWRegistrationTier
	}
	return data.Tier
}

func normalizePrivateKeyPEM(value []byte) ([]byte, error) {
	block, _ := pem.Decode(bytes.TrimSpace(value))
	if block == nil {
		return nil, fmt.Errorf("AGW challenge key must be PEM encoded")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		parsed, pkcs8Err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if pkcs8Err != nil {
			return nil, fmt.Errorf("parse AGW challenge key: %w", err)
		}
		var ok bool
		key, ok = parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("AGW challenge key must be an ECDSA private key")
		}
	}
	return encodeECPrivateKeyPEM(key)
}

func generateECDSAPrivateKeyPEM() ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return encodeECPrivateKeyPEM(key)
}

func encodeECPrivateKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func publicKeyBase64DER(privateKeyPEM []byte) (string, error) {
	block, _ := pem.Decode(bytes.TrimSpace(privateKeyPEM))
	if block == nil {
		return "", fmt.Errorf("AGW challenge key must be PEM encoded")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		parsed, pkcs8Err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if pkcs8Err != nil {
			return "", fmt.Errorf("parse AGW challenge key: %w", err)
		}
		var ok bool
		key, ok = parsed.(*ecdsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("AGW challenge key must be an ECDSA private key")
		}
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

func newHardwareID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:]), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func updateAGWStatusDetails(agw *magmav1alpha1.MagmaAGW, identity *agwIdentityState) {
	if identity == nil {
		return
	}
	agw.Status.IdentitySecretName = identity.SecretName
	agw.Status.HardwareID = identity.HardwareID
	agw.Status.ChallengePublicKeyHash = identity.PublicKeyHash
	agw.Status.GatewayRegistered = identity.GatewayRegistered
}
