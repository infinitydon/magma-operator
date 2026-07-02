package controller

const (
	labelAppComponent = "app.kubernetes.io/component"
	labelAppInstance  = "app.kubernetes.io/instance"
	labelAppManagedBy = "app.kubernetes.io/managed-by"
	labelAppName      = "app.kubernetes.io/name"

	managedByMagmaOperator = "magma-operator"
	magmaAGWChartName      = "magma-agw-upstream"
	magmaOrc8rChartName    = "magma-fullstack-upstream"
	magmalteDeploymentName = "nms-magmalte"

	shellExitOnErrorCommand = "-ceu"

	adminOperatorCertPath = "/run/secrets/admin_operator.pem"
	adminOperatorKeyPath  = "/run/secrets/admin_operator.key.pem"
)
