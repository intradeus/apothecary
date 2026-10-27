{{- define "apothecary.tag" -}}
{{- .Values.image.tag | default .Chart.AppVersion -}}
{{- end -}}

{{- define "apothecary.image" -}}
{{- $o := index .root.Values.image.overrides .key -}}
{{- if $o -}}{{ $o }}{{- else -}}{{ .root.Values.image.repository }}/{{ .name }}:{{ include "apothecary.tag" .root }}{{- end -}}
{{- end -}}

{{- define "apothecary.labels" -}}
app.kubernetes.io/part-of: apothecary
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{/* The engine's pods carry this label: sandbox, gateway and browser network
     policies select the engine by it. Do not change it. */}}
{{- define "apothecary.engineSelector" -}}
app.kubernetes.io/name: apothecaryd
{{- end -}}

{{- define "apothecary.secretName" -}}
{{- .Values.existingSecret | default "apothecaryd" -}}
{{- end -}}

{{- define "apothecary.engineURL" -}}
http://apothecaryd.{{ .Release.Namespace }}.svc.{{ .Values.workspaces.clusterDomain }}:8080
{{- end -}}
