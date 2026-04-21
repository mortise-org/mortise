{{- define "mortise.depsNamespace" -}}
{{ .Values.buildInfra.namespace | default "mortise-deps" }}
{{- end -}}

{{- define "mortise.registryAddr" -}}
registry.{{ include "mortise.depsNamespace" . }}.svc:5000
{{- end -}}

{{- define "mortise.buildkitAddr" -}}
tcp://buildkitd.{{ include "mortise.depsNamespace" . }}.svc:1234
{{- end -}}

{{- define "mortise.labels" -}}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: mortise
{{- end -}}
