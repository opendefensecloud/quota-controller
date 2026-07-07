{{- define "quota-controller.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "quota-controller.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "quota-controller.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "quota-controller.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "quota-controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "quota-controller.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: controller
{{- end }}

{{- define "quota-controller.serviceAccountName" -}}
{{- if .Values.controller.serviceAccount.create }}
{{- default (include "quota-controller.fullname" .) .Values.controller.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.controller.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Webhook helpers
*/}}

{{- define "quota-controller.webhook.fullname" -}}
{{- printf "%s-webhook" (include "quota-controller.fullname" .) }}
{{- end }}

{{- define "quota-controller.webhook.selectorLabels" -}}
app.kubernetes.io/name: {{ include "quota-controller.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: webhook
{{- end }}

{{- define "quota-controller.webhook.serviceAccountName" -}}
{{- if .Values.webhook.serviceAccount.create }}
{{- default (include "quota-controller.webhook.fullname" .) .Values.webhook.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.webhook.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "quota-controller.webhook.tlsSecretName" -}}
{{- if .Values.webhook.tls.existingSecret }}
{{- .Values.webhook.tls.existingSecret }}
{{- else }}
{{- printf "%s-tls" (include "quota-controller.webhook.fullname" .) }}
{{- end }}
{{- end }}

{{- define "quota-controller.webhook.serviceFQDN" -}}
{{- printf "%s.%s.svc" (include "quota-controller.webhook.fullname" .) .Release.Namespace }}
{{- end }}

{{- define "quota-controller.webhook.baseURL" -}}
{{- printf "https://%s:%d" (include "quota-controller.webhook.serviceFQDN" .) (int .Values.webhook.service.port) }}
{{- end }}
