{{- define "rewind.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "rewind.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s" (include "rewind.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "rewind.labels" -}}
app.kubernetes.io/name: {{ include "rewind.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end -}}

{{- define "rewind.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rewind.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "rewind.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "rewind.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "rewind.validateMode" -}}
{{- if not (has .Values.mode (list "argo" "flux" "crd")) -}}
{{- fail "values.mode must be one of: argo, flux, crd" -}}
{{- end -}}
{{- end -}}
