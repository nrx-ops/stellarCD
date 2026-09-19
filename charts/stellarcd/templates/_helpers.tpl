{{/*
Chart name, overridable with nameOverride.
*/}}
{{- define "stellarcd.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified resource name prefix. Kubernetes names cap at 63 characters and
the longest suffix this chart appends is "-controller-manager-metrics" (27), so
the prefix is truncated to leave room for it.
*/}}
{{- define "stellarcd.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 36 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 36 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 36 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "stellarcd.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Labels shared by every object in the chart.
*/}}
{{- define "stellarcd.labels" -}}
helm.sh/chart: {{ include "stellarcd.chart" . }}
app.kubernetes.io/name: {{ include "stellarcd.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/part-of: {{ include "stellarcd.name" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end }}

{{/*
Annotations shared by every object in the chart.
*/}}
{{- define "stellarcd.annotations" -}}
{{- with .Values.commonAnnotations }}
{{- toYaml . }}
{{- end }}
{{- end }}

{{/*
Controller selector labels. These land in Deployment.spec.selector, which is
immutable after creation, so they deliberately exclude version and chart labels.
*/}}
{{- define "stellarcd.controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "stellarcd.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: controller-manager
control-plane: controller-manager
{{- end }}

{{- define "stellarcd.controller.labels" -}}
{{ include "stellarcd.labels" . }}
app.kubernetes.io/component: controller-manager
control-plane: controller-manager
{{- end }}

{{- define "stellarcd.ui.selectorLabels" -}}
app.kubernetes.io/name: {{ include "stellarcd.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: dashboard
{{- end }}

{{- define "stellarcd.ui.labels" -}}
{{ include "stellarcd.labels" . }}
app.kubernetes.io/component: dashboard
{{- end }}

{{- define "stellarcd.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (printf "%s-controller-manager" (include "stellarcd.fullname" .)) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Image references. An empty tag falls back to the chart's appVersion so that
`helm upgrade` to a new chart version also moves the image.
*/}}
{{- define "stellarcd.controller.image" -}}
{{- printf "%s:%s" .Values.controller.image.repository (default .Chart.AppVersion .Values.controller.image.tag) }}
{{- end }}

{{- define "stellarcd.ui.image" -}}
{{- printf "%s:%s" .Values.ui.image.repository (default .Chart.AppVersion .Values.ui.image.tag) }}
{{- end }}

{{- define "stellarcd.ui.serviceName" -}}
{{- printf "%s-frontend" (include "stellarcd.fullname" .) }}
{{- end }}

{{/*
Cluster-internal FQDN of the dashboard Service. Istio resolves a short name
against the VirtualService's own namespace, so spelling it out keeps the route
correct if the VirtualService is ever moved.
*/}}
{{- define "stellarcd.ui.serviceFQDN" -}}
{{- printf "%s.%s.svc.cluster.local" (include "stellarcd.ui.serviceName" .) .Release.Namespace }}
{{- end }}

{{- define "stellarcd.adminApiServiceName" -}}
{{- printf "%s-admin-api" (include "stellarcd.fullname" .) }}
{{- end }}

{{/*
URL the dashboard's nginx proxies /api to. Fully qualified so the proxy keeps
resolving if the dashboard is ever moved to another namespace.
*/}}
{{- define "stellarcd.adminApiUrl" -}}
{{- if .Values.ui.adminApiUrl }}
{{- .Values.ui.adminApiUrl }}
{{- else }}
{{- printf "http://%s.%s.svc:%d" (include "stellarcd.adminApiServiceName" .) .Release.Namespace (int .Values.controller.adminApi.service.port) }}
{{- end }}
{{- end }}
