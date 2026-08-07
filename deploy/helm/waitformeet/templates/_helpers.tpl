{{/*
Expand the name of the chart.
*/}}
{{- define "waitformeet.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
A fully qualified app name, kept within the 63 character limit Kubernetes imposes
on names.
*/}}
{{- define "waitformeet.fullname" -}}
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

{{- define "waitformeet.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "waitformeet.labels" -}}
helm.sh/chart: {{ include "waitformeet.chart" . }}
{{ include "waitformeet.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "waitformeet.selectorLabels" -}}
app.kubernetes.io/name: {{ include "waitformeet.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "waitformeet.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "waitformeet.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
The name of the Secret holding the credentials, whether the chart creates it or the
deployment supplies its own.
*/}}
{{- define "waitformeet.secretName" -}}
{{- if .Values.existingSecret.name }}
{{- .Values.existingSecret.name }}
{{- else }}
{{- include "waitformeet.fullname" . }}
{{- end }}
{{- end }}

{{- define "waitformeet.pvcName" -}}
{{- if .Values.persistence.existingClaim }}
{{- .Values.persistence.existingClaim }}
{{- else }}
{{- include "waitformeet.fullname" . }}
{{- end }}
{{- end }}

{{/*
The session secret.

An explicit value wins. Otherwise the one already in the cluster is reused, so an
upgrade does not invalidate every form and in-flight login. Only a genuinely fresh
install generates a new one.

Note that `lookup` returns nothing during `helm template`, so a plain render shows a
different random value each time. That is expected: it is a preview, not an install.
*/}}
{{- define "waitformeet.sessionSecret" -}}
{{- if .Values.auth.sessionSecret -}}
{{- .Values.auth.sessionSecret -}}
{{- else -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace (include "waitformeet.fullname" .) -}}
{{- if and $existing $existing.data (index $existing.data "sessionSecret") -}}
{{- index $existing.data "sessionSecret" | b64dec -}}
{{- else -}}
{{- randAlphaNum 48 -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
The content seed, as JSON.

JSON rather than YAML because the application parses it with the standard library
and needs no YAML dependency. Empty values are pruned so that a partially filled
values file is a partial override rather than a set of blanks.
*/}}
{{- define "waitformeet.seedJSON" -}}
{{- $content := deepCopy .Values.content -}}
{{- if not $content.main -}}
{{- $_ := unset $content "main" -}}
{{- end -}}
{{- $content | toPrettyJson -}}
{{- end }}
