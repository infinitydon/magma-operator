{{- define "magma-fullstack-upstream.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: Helm
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "magma-fullstack-upstream.assertNoLatest" -}}
{{- if regexMatch "(:latest$|^latest$)" .Values.image.postgres -}}
{{- fail "image.postgres must not use latest" -}}
{{- end -}}
{{- if regexMatch "(:latest$|^latest$)" .Values.image.test -}}
{{- fail "image.test must not use latest" -}}
{{- end -}}
{{- if regexMatch "(:latest$|^latest$)" .Values.orc8r.controller.image.tag -}}
{{- fail "orc8r.controller.image.tag must not use latest" -}}
{{- end -}}
{{- if regexMatch "(:latest$|^latest$)" .Values.orc8r.nginx.image.tag -}}
{{- fail "orc8r.nginx.image.tag must not use latest" -}}
{{- end -}}
{{- if regexMatch "(:latest$|^latest$)" .Values.orc8r.nms.magmalte.image.tag -}}
{{- fail "orc8r.nms.magmalte.image.tag must not use latest" -}}
{{- end -}}
{{- if regexMatch "(:latest$|^latest$)" .Values.orc8r.nms.nginx.image.tag -}}
{{- fail "orc8r.nms.nginx.image.tag must not use latest" -}}
{{- end -}}
{{- if regexMatch "(:latest$|^latest$)" (index .Values "lte-orc8r").controller.image.tag -}}
{{- fail "lte-orc8r.controller.image.tag must not use latest" -}}
{{- end -}}
{{- end -}}
