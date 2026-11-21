{{- define "operator.config.data" -}}
config.yaml: |
  apiVersion: operator.config.grove.io/v1beta1
  kind: OperatorConfiguration
  runtimeClientConnection:
    qps: {{ .Values.config.runtimeClientConnection.qps }}
    burst: {{ .Values.config.runtimeClientConnection.burst }}
  leaderElection:
    enabled: {{ .Values.config.leaderElection.enabled }}
    leaseDuration: {{ .Values.config.leaderElection.leaseDuration }}
    renewDeadline: {{ .Values.config.leaderElection.renewDeadline }}
    retryPeriod: {{ .Values.config.leaderElection.retryPeriod }}
    resourceLock: {{ .Values.config.leaderElection.resourceLock }}
    resourceName: {{ .Values.config.leaderElection.resourceName }}
    resourceNamespace: {{ .Release.Namespace }}
  server:
    webhooks:
      port: {{ .Values.config.server.webhooks.port }}
      serverCertDir: {{ .Values.config.server.webhooks.serverCertDir }}
      secretName: {{ .Values.config.server.webhooks.secretName | default "grove-webhook-server-cert" }}
      certProvisionMode: {{ .Values.config.server.webhooks.certProvisionMode | default "auto" }}
    healthProbes:
      port: {{ .Values.config.server.healthProbes.port }}
    metrics:
      port: {{ .Values.config.server.metrics.port }}
  controllers:
    podCliqueSet:
      concurrentSyncs: {{ .Values.config.controllers.podCliqueSet.concurrentSyncs }}
    podClique:
      concurrentSyncs: {{ .Values.config.controllers.podClique.concurrentSyncs }}
    podCliqueScalingGroup:
      concurrentSyncs: {{ .Values.config.controllers.podCliqueScalingGroup.concurrentSyncs }}
  {{- if and .Values.config.scheduler .Values.config.scheduler.profiles }}
  scheduler:
    {{- if .Values.config.scheduler.defaultProfileName }}
    defaultProfileName: {{ .Values.config.scheduler.defaultProfileName }}
    {{- end }}
    profiles:
    {{- range .Values.config.scheduler.profiles }}
    - name: {{ .name }}
      {{- if hasKey . "config" }}
      config:
        {{- toYaml .config | nindent 8 }}
      {{- end }}
    {{- end }}
  {{- end }}
  {{- if .Values.config.debugging }}
  debugging:
    enableProfiling: {{ .Values.config.debugging.enableProfiling }}
    {{- if .Values.config.debugging.pprofBindHost }}
    pprofBindHost: {{ .Values.config.debugging.pprofBindHost | quote }}
    {{- end }}
    {{- if .Values.config.debugging.pprofBindPort }}
    pprofBindPort: {{ .Values.config.debugging.pprofBindPort }}
    {{- end }}
  {{- end }}
  logLevel: {{ .Values.config.logLevel | default "info" }}
  logFormat: {{ .Values.config.logFormat | default "json" }}
  {{- if .Values.config.topologyAwareScheduling }}
  topologyAwareScheduling:
    enabled: {{ .Values.config.topologyAwareScheduling.enabled }}
  {{- end }}
  {{- if .Values.config.authorizer.enabled }}
  authorizer:
    enabled: {{ .Values.config.authorizer.enabled }}
    reconcilerServiceAccountUserName: {{ printf "system:serviceaccount:%s:%s" .Release.Namespace .Values.serviceAccount.name }}
    exemptServiceAccountUserNames:
    {{- if .Values.config.authorizer.exemptServiceAccountUserNames }}
    {{- range $idx, $name := .Values.config.authorizer.exemptServiceAccountUserNames }}
      - {{ $name }}
    {{- end }}
    {{- else }}
      []
    {{- end }}
  {{- end }}
  {{- if .Values.config.network }}
  network:
    autoMNNVLEnabled: {{ .Values.config.network.autoMNNVLEnabled | default false }}
  {{- end }}

{{- end -}}

{{- define "operator.config.name" -}}
grove-operator-cm-{{ include "operator.config.data" . | sha256sum | trunc 8 }}
{{- end -}}

{{/*
Merge chart defaults (chart, release) with user-supplied labels.
User keys overwrite defaults so a values release label replaces the
Helm release name instead of duplicating the YAML key.
Args: list <root> <userLabelsDictOrNil>
*/}}
{{- define "common.labels" -}}
{{- $root := index . 0 -}}
{{- $labels := dict
  "chart" (printf "%s-%s" $root.Chart.Name $root.Chart.Version | replace "+" "_" | trunc 63 | trimAll "-_.")
  "release" $root.Release.Name
-}}
{{- toYaml (mergeOverwrite $labels (index . 1 | default dict)) }}
{{- end -}}

{{/* Returns "true" if the named scheduler profile is configured, empty string otherwise. */}}
{{- define "grove.scheduler.hasProfile" -}}
{{- $root := index . 0 -}}
{{- $profile := index . 1 -}}
{{- range $root.Values.config.scheduler.profiles -}}
{{- if eq .name $profile -}}true{{- end -}}
{{- end -}}
{{- end -}}

{{- define "operator.config.labels" -}}
{{- include "common.labels" (list . .Values.configMap.labels) }}
{{- end -}}

{{- define "operator.deployment.matchLabels" -}}
{{- range $key, $val := .Values.deployment.labels }}
{{ $key }}: {{ $val }}
{{- end }}
{{- end -}}

{{- define "operator.deployment.labels" -}}
{{- include "common.labels" (list . .Values.deployment.labels) }}
{{- end -}}

{{- define "operator.serviceaccount.labels" -}}
{{- include "common.labels" (list . .Values.serviceAccount.labels) }}
{{- end -}}

{{- define "operator.service.matchLabels" -}}
{{- range $key, $val := .Values.service.labels }}
{{ $key }}: {{ $val }}
{{- end }}
{{- end -}}

{{- define "operator.service.labels" -}}
{{- include "common.labels" (list . .Values.service.labels) }}
{{- end -}}

{{- define "operator.servicemonitor.labels" -}}
{{- include "common.labels" (list . .Values.metrics.serviceMonitor.labels) }}
{{- end -}}

{{- define "operator.clusterrole.labels" -}}
{{- include "common.labels" (list . .Values.clusterRole.labels) }}
{{- end -}}

{{- define "operator.clusterrolebinding.labels" -}}
{{- include "common.labels" (list . .Values.clusterRoleBinding.labels) }}
{{- end -}}

{{- define "image" -}}
{{- if hasPrefix "sha256:" (required "$.tag is required" $.tag) -}}
{{ required "$.repository is required" $.repository }}@{{ required "$.tag" $.tag }}
{{- else -}}
{{ required "$.repository is required" $.repository }}:{{ required "$.tag" $.tag }}
{{- end -}}
{{- end -}}

{{- define "operator.pcs.validating.webhook.labels" -}}
{{- include "common.labels" (list . .Values.webhooks.podCliqueSetValidationWebhook.labels) }}
{{- end -}}

{{- define "operator.pclq.validating.webhook.labels" -}}
{{- include "common.labels" (list . .Values.webhooks.podCliqueValidationWebhook.labels) }}
{{- end -}}

{{- define "operator.pcsg.validating.webhook.labels" -}}
{{- include "common.labels" (list . .Values.webhooks.podCliqueScalingGroupValidationWebhook.labels) }}
{{- end -}}

{{- define "operator.pcs.defaulting.webhook.labels" -}}
{{- include "common.labels" (list . .Values.webhooks.podCliqueSetDefaultingWebhook.labels) }}
{{- end -}}

{{- define "operator.clustertopology.validating.webhook.labels" -}}
{{- include "common.labels" (list . .Values.webhooks.clusterTopologyValidationWebhook.labels) }}
{{- end -}}

{{- define "operator.podclique.validating.webhook.labels" -}}
{{- include "common.labels" (list . .Values.webhooks.podCliqueValidationWebhook.labels) }}
{{- end -}}

{{- define "operator.authorizer.webhook.labels" -}}
{{- include "common.labels" (list . .Values.webhooks.authorizerWebhook.labels) }}
{{- end -}}

{{- define "operator.server.secret.labels" -}}
{{- include "common.labels" (list . .Values.webhookServerSecret.labels) }}
{{- end -}}

{{- define "operator.lease.role.labels" -}}
{{- include "common.labels" (list . .Values.leaseRole.labels) }}
{{- end -}}

{{- define "operator.lease.rolebinding.labels" -}}
{{- include "common.labels" (list . .Values.leaseRoleBinding.labels) }}
{{- end -}}
