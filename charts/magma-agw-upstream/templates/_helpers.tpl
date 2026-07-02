{{/*
Copyright 2020 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/}}
{{/* Generate basic labels */}}
{{- define "default-labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/component: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: helm
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
{{- end -}}

{{/* Generate selector labels */}}
{{- define "default-selector-labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* Generate selector labels */}}
{{- define "image-version-label" -}}
app.kubernetes.io/version: {{ .Values.image.tag | quote }}
{{- end -}}

{{- define "agw-instance-label" -}}
magma.infinitydon.com/agw-instance: "true"
{{- end -}}

{{- define "agw.snowflake" -}}
{{- .Values.gatewayIdentity.snowflake | trim | nospace -}}
{{- end -}}

{{- define "agw.podAntiAffinity" -}}
{{- if .Values.agwAntiAffinity.enabled }}
affinity:
  podAntiAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      - labelSelector:
          matchExpressions:
            - key: magma.infinitydon.com/agw-instance
              operator: In
              values:
                - "true"
            - key: app.kubernetes.io/instance
              operator: NotIn
              values:
                - {{ .Release.Name | quote }}
        topologyKey: {{ .Values.agwAntiAffinity.topologyKey | quote }}
{{- end }}
{{- end -}}

{{- define "simulator.agwAntiAffinity" -}}
{{- if and .Values.simulator.enabled .Values.simulator.antiAffinity.separateFromAgw }}
affinity:
  podAntiAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      - labelSelector:
          matchExpressions:
            - key: magma.infinitydon.com/agw-instance
              operator: In
              values:
                - "true"
        topologyKey: {{ .Values.simulator.antiAffinity.topologyKey | quote }}
{{- end }}
{{- end -}}

{{- define "magma-agw-upstream.assertNoLatest" -}}
{{- if regexMatch "(:latest$|^latest$)" .Values.image.tag -}}
{{- fail "image.tag must not use latest" -}}
{{- end -}}
{{- if regexMatch "(:latest$|^latest$)" .Values.image.gatewayGoTag -}}
{{- fail "image.gatewayGoTag must not use latest" -}}
{{- end -}}
{{- if regexMatch "(:latest$|^latest$)" .Values.image.test -}}
{{- fail "image.test must not use latest" -}}
{{- end -}}
{{- if and .Values.nodePrep.enabled (regexMatch "(:latest$|^latest$)" .Values.nodePrep.image) -}}
{{- fail "nodePrep.image must not use latest" -}}
{{- end -}}
{{- if and .Values.simulator.enabled (regexMatch "(:latest$|^latest$)" .Values.simulator.image.tag) -}}
{{- fail "simulator.image.tag must not use latest" -}}
{{- end -}}
{{- if and .Values.simulator.enabled (regexMatch "(:latest$|^latest$)" .Values.simulator.initImage.tag) -}}
{{- fail "simulator.initImage.tag must not use latest" -}}
{{- end -}}
{{- end -}}
