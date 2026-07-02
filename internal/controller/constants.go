package controller

const (
	labelAppComponent = "app.kubernetes.io/component"
	labelAppInstance  = "app.kubernetes.io/instance"
	labelAppManagedBy = "app.kubernetes.io/managed-by"
	labelAppName      = "app.kubernetes.io/name"

	managedByMagmaOperator = "magma-operator"
	magmaAGWAppName        = "magma-agw-upstream"
	magmaOrc8rAppName      = "magma-fullstack-upstream"
	agwNodePrepName        = "agw-node-prep"
	magmalteDeploymentName = "nms-magmalte"

	shellExitOnErrorCommand = "-ceu"

	adminOperatorCertPath = "/run/secrets/admin_operator.pem"
	adminOperatorKeyPath  = "/run/secrets/admin_operator.key.pem"
	adminOperatorCertKey  = "admin_operator.pem"
	adminOperatorKeyKey   = "admin_operator.key.pem"
)
