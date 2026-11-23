{{- /*
An Issuer of the ACME CA, with its own ACME account, for one Certificate of
the same name, and the Secret <name>-acme-eab with its EAB key. Called with
(dict "name" <name> "namespace" <namespace> "acme" .Values.acme "eab"
<keyID and hmac>). cert-manager uses the EAB key only to register the
account and keeps the account key in <name>-acme-account.
*/}}
{{- define "cnpg-cluster.issuer" -}}
apiVersion: v1
kind: Secret
metadata:
  name: {{ .name }}-acme-eab
  namespace: {{ .namespace }}
stringData:
  secret: {{ required (printf "the hmac of the EAB key of %s is required" .name) .eab.hmac | quote }}
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: {{ .name }}
  namespace: {{ .namespace }}
spec:
  acme:
    server: {{ .acme.server }}
    {{- with .acme.ca }}
    caBundle: {{ . | b64enc }}
    {{- end }}
    privateKeySecretRef:
      name: {{ .name }}-acme-account
    externalAccountBinding:
      keyID: {{ required (printf "the keyID of the EAB key of %s is required" .name) .eab.keyID | quote }}
      keySecretRef:
        name: {{ .name }}-acme-eab
        key: secret
    solvers:
      - http01:
          ingress:
            ingressClassName: {{ .acme.ingressClassName }}
{{- end }}
