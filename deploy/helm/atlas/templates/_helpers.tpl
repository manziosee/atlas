{{- define "atlas.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "atlas.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else if contains (include "atlas.name" .) .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "atlas.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "atlas.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/part-of: atlas
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end -}}

{{- define "atlas.selector" -}}
app.kubernetes.io/name: {{ include "atlas.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "atlas.syncSelector" -}}
app.kubernetes.io/name: {{ include "atlas.name" . }}-k8s
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "atlas.image" -}}
{{ .Values.image.repository }}:{{ default .Chart.AppVersion .Values.image.tag }}
{{- end -}}

{{- define "atlas.secretName" -}}
{{- default (printf "%s-secrets" (include "atlas.fullname" .)) .Values.secrets.existingSecret -}}
{{- end -}}

{{- define "atlas.scheme" -}}
{{- if .Values.tls.enabled }}https{{ else }}http{{ end -}}
{{- end -}}
