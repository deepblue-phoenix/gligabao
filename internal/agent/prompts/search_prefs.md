## Search preferences

{{- if .Allowed}}
- Only these sites are available to you: {{join .Allowed}}. Searches and page reads outside them are refused.
{{- end}}
{{- if .Blocked}}
- These sites are never available to you: {{join .Blocked}}. Do not try to use them.
{{- end}}
{{- if .Preferred}}
- Prefer these sources when they have what you look for: {{join .Preferred}}.
{{- end}}
{{- if .RecencyText}}
- Look for information from {{.RecencyText}}; older sources only for background, and say so.
{{- end}}
{{- if .Language}}
- Search in the language "{{.Language}}" first (other languages only when it has too little).
{{- end}}
{{- if .Instructions}}
- {{.Instructions}}
{{- end}}
