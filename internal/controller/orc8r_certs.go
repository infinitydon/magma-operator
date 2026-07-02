package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	magmav1alpha1 "github.com/infinitydon/magma-operator/api/v1alpha1"
)

const (
	orc8rCertDomainAnnotation = "magma.infra.don/orc8r-cert-domain"
	orc8rCertHashAnnotation   = "magma.infra.don/orc8r-cert-hash"
)

func (r *MagmaOrc8rReconciler) reconcileOrc8rCertSecret(ctx context.Context, orc8r *magmav1alpha1.MagmaOrc8r) (string, error) {
	domainName := orc8r.Spec.DomainName
	if domainName == "" {
		domainName = "magma.local"
	}
	controllerHostname := orc8r.Spec.ControllerHostname
	if controllerHostname == "" {
		controllerHostname = "controller." + domainName
	}
	domainKey := domainName + "|" + controllerHostname

	var secret corev1.Secret
	err := r.Get(ctx, client.ObjectKey{Namespace: orc8r.Namespace, Name: defaultNMSAdminCertSecretName}, &secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return "", err
	}
	if apierrors.IsNotFound(err) {
		secret = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: defaultNMSAdminCertSecretName, Namespace: orc8r.Namespace}}
	}

	if secret.Annotations != nil && secret.Annotations[orc8rCertDomainAnnotation] == domainKey && len(secret.Data[rootCASecretKey]) > 0 {
		return secret.Annotations[orc8rCertHashAnnotation], nil
	}

	data, hash, err := generateOrc8rCertSecretData(domainName, controllerHostname)
	if err != nil {
		return "", err
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, &secret, func() error {
		secret.Type = corev1.SecretTypeOpaque
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		secret.Labels[labelAppManagedBy] = managedByMagmaOperator
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations[orc8rCertDomainAnnotation] = domainKey
		secret.Annotations[orc8rCertHashAnnotation] = hash
		secret.Data = data
		return controllerutil.SetControllerReference(orc8r, &secret, r.Scheme)
	}); err != nil {
		return "", err
	}
	return hash, nil
}

func generateOrc8rCertSecretData(domainName, controllerHostname string) (map[string][]byte, string, error) {
	rootKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", err
	}
	certifierKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", err
	}
	vpnKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", err
	}

	rootCert, err := createCertificate("rootca."+domainName, nil, nil, &rootKey.PublicKey, rootKey, true)
	if err != nil {
		return nil, "", err
	}
	certifierCert, err := createCertificate("certifier."+domainName, nil, nil, &certifierKey.PublicKey, certifierKey, true)
	if err != nil {
		return nil, "", err
	}
	vpnCert, err := createCertificate("vpnca."+domainName, nil, nil, &vpnKey.PublicKey, vpnKey, true)
	if err != nil {
		return nil, "", err
	}

	controllerKey, controllerCert, err := signedCert("controller."+domainName, controllerSANs(domainName, controllerHostname), rootCert, rootKey)
	if err != nil {
		return nil, "", err
	}
	adminKey, adminCert, err := signedCert("admin_operator", nil, certifierCert, certifierKey)
	if err != nil {
		return nil, "", err
	}
	fluentdKey, fluentdCert, err := signedCert("fluentd."+domainName, []string{"fluentd", "fluentd." + domainName}, certifierCert, certifierKey)
	if err != nil {
		return nil, "", err
	}

	data := map[string][]byte{
		"rootCA.pem":         pemCert(rootCert.Raw),
		"controller.crt":     pemCert(controllerCert.Raw),
		"controller.key":     pemKey(controllerKey),
		adminOperatorCertKey: pemCert(adminCert.Raw),
		adminOperatorKeyKey:  pemKey(adminKey),
		"certifier.pem":      pemCert(certifierCert.Raw),
		"certifier.key":      pemKey(certifierKey),
		"vpn_ca.crt":         pemCert(vpnCert.Raw),
		"vpn_ca.key":         pemKey(vpnKey),
		"fluentd.pem":        pemCert(fluentdCert.Raw),
		"fluentd.key":        pemKey(fluentdKey),
		"bootstrapper.key":   pemKey(rootKey),
	}
	hashInput := []byte(domainName + "|" + controllerHostname)
	hashInput = append(hashInput, data["rootCA.pem"]...)
	sum := sha256.Sum256(hashInput)
	return data, hex.EncodeToString(sum[:]), nil
}

func controllerSANs(domainName, controllerHostname string) []string {
	return []string{
		controllerHostname,
		"controller." + domainName,
		"api." + domainName,
		"*.nms." + domainName,
		"bootstrapper-controller." + domainName,
	}
}

func signedCert(commonName string, sans []string, ca *x509.Certificate, caKey *rsa.PrivateKey) (*rsa.PrivateKey, *x509.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	cert, err := createCertificate(commonName, sans, ca, &key.PublicKey, caKey, false)
	if err != nil {
		return nil, nil, err
	}
	return key, cert, nil
}

func createCertificate(commonName string, sans []string, ca *x509.Certificate, publicKey *rsa.PublicKey, signer *rsa.PrivateKey, isCA bool) (*x509.Certificate, error) {
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Add(-1 * time.Hour)
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now,
		NotAfter:     now.AddDate(10, 0, 0),
		DNSNames:     sans,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	if isCA {
		template.IsCA = true
		template.BasicConstraintsValid = true
		template.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign
	}
	parent := ca
	if parent == nil {
		parent = template
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, parent, publicKey, signer)
	if err != nil {
		return nil, fmt.Errorf("create certificate %s: %w", commonName, err)
	}
	return x509.ParseCertificate(raw)
}

func pemCert(raw []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
}

func pemKey(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}
