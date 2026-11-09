{{- define "burnrate.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "burnrate.fullname" -}}
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

{{- define "burnrate.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "burnrate.selectorLabels" -}}
app.kubernetes.io/name: {{ include "burnrate.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "burnrate.labels" -}}
helm.sh/chart: {{ include "burnrate.chart" . }}
{{ include "burnrate.selectorLabels" . }}
app.kubernetes.io/version: {{ include "burnrate.imageTag" . | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: burnrate
{{- end }}

{{- define "burnrate.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "burnrate.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "burnrate.imageTag" -}}
{{- default .Chart.AppVersion .Values.image.tag }}
{{- end }}

{{/* Pod template shared by the Deployment and the Rollout. */}}
{{- define "burnrate.podTemplate" -}}
metadata:
  labels:
    {{- include "burnrate.selectorLabels" . | nindent 4 }}
    app.kubernetes.io/version: {{ include "burnrate.imageTag" . | quote }}
    {{- with .Values.podLabels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  {{- with .Values.podAnnotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  serviceAccountName: {{ include "burnrate.serviceAccountName" . }}
  automountServiceAccountToken: false
  terminationGracePeriodSeconds: {{ .Values.terminationGracePeriodSeconds }}
  securityContext:
    {{- toYaml .Values.podSecurityContext | nindent 4 }}
  containers:
    - name: burnrate
      image: "{{ .Values.image.repository }}:{{ include "burnrate.imageTag" . }}"
      imagePullPolicy: {{ .Values.image.pullPolicy }}
      ports:
        - name: http
          containerPort: 8080
          protocol: TCP
      env:
        - name: PORT
          value: "8080"
        - name: LOG_LEVEL
          value: {{ .Values.config.logLevel | quote }}
        - name: DEFAULT_CAPACITY
          value: {{ .Values.config.defaultCapacity | quote }}
        - name: DEFAULT_REFILL_PER_SECOND
          value: {{ .Values.config.defaultRefillPerSecond | quote }}
        {{- with .Values.config.tenantPolicies }}
        - name: TENANT_POLICIES
          value: {{ toJson . | quote }}
        {{- end }}
        - name: BUCKET_IDLE_TTL
          value: {{ .Values.config.bucketIdleTTL | quote }}
        - name: SHUTDOWN_DRAIN_DELAY
          value: {{ .Values.config.shutdownDrainDelay | quote }}
        - name: SHUTDOWN_TIMEOUT
          value: {{ .Values.config.shutdownTimeout | quote }}
        - name: FAULT_ERROR_RATE
          value: {{ .Values.config.faultErrorRate | quote }}
        - name: FAULT_LATENCY
          value: {{ .Values.config.faultLatency | quote }}
        - name: GOMAXPROCS
          valueFrom:
            resourceFieldRef:
              resource: limits.cpu
              divisor: "1"
        - name: GOMEMLIMIT
          valueFrom:
            resourceFieldRef:
              resource: limits.memory
              divisor: "1"
        {{- with .Values.otel.endpoint }}
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: {{ . | quote }}
        - name: OTEL_RESOURCE_ATTRIBUTES
          value: "k8s.namespace.name=$(POD_NAMESPACE),k8s.pod.name=$(POD_NAME)"
        {{- end }}
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        {{- with .Values.extraEnv }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      securityContext:
        {{- toYaml .Values.securityContext | nindent 8 }}
      {{- with .Values.startupProbe }}
      startupProbe:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.livenessProbe }}
      livenessProbe:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.readinessProbe }}
      readinessProbe:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      resources:
        {{- toYaml .Values.resources | nindent 8 }}
  {{- if .Values.topologySpreadConstraints.enabled }}
  topologySpreadConstraints:
    - maxSkew: {{ .Values.topologySpreadConstraints.maxSkew }}
      topologyKey: kubernetes.io/hostname
      whenUnsatisfiable: ScheduleAnyway
      labelSelector:
        matchLabels:
          {{- include "burnrate.selectorLabels" . | nindent 10 }}
  {{- end }}
  {{- with .Values.nodeSelector }}
  nodeSelector:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with .Values.affinity }}
  affinity:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with .Values.tolerations }}
  tolerations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}
