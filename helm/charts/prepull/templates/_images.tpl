{{- /*
The images of a part of the versions file, as YAML list items, each by the
string the chart that runs it uses: registry/repository:tag@digest. Called
with the part; it calls itself on each value, except a block's tag.
*/}}
{{- define "prepull.images" -}}
{{- if kindIs "map" . }}
{{- with .repository }}
{{- $ref := . }}
{{- with $.registry }}{{ $ref = printf "%s/%s" . $ref }}{{ end }}
{{- $tag := toString ($.tag | default "") }}
{{- $digest := toString ($.digest | default "") }}
{{- if contains "@" $tag }}{{ $digest = splitList "@" $tag | last }}{{ $tag = splitList "@" $tag | first }}{{ end }}
{{- if $tag }}{{ $ref = printf "%s:%s" $ref $tag }}{{ end }}
- {{ printf "%s@%s" $ref (required (printf "%s has no digest" $ref) $digest) | quote }}
{{- end }}
{{- range $key, $value := . }}
{{- if ne $key "tag" }}{{ include "prepull.images" $value }}{{ end }}
{{- end }}
{{- else if kindIs "slice" . }}
{{- range . }}{{ include "prepull.images" . }}{{ end }}
{{- else if and (kindIs "string" .) (contains "@sha256:" .) }}
- {{ . | quote }}
{{- end }}
{{- end -}}
