using '../templates/ci-bot-rbac.bicep'

param botApplicationName = '{{ .ci.dev.bot.applicationName }}'

param e2eSubscriptionIds = [
{{ $sep := "" }}{{ range $subscription := .ci.dev.e2eSubscriptions }}{{ if not $subscription.unmanaged }}{{ $sep }}  '{{ $subscription.id }}'
{{ $sep = "" }}{{ end }}{{ end }}]

param infrastructureSubscriptions = [
{{ range .ci.dev.infrastructureSubscriptions }}  {
    id: '{{ .id }}'
    isGlobalSubscription: {{ .isGlobalSubscription }}
  }
{{ end }}]

param grantAksRbac = true
