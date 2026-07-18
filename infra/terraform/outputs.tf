output "registry_id" {
  value = yandex_container_registry.metric.id
}

output "metric_folder_id" {
  value = var.metric_folder_id
}

output "runtime_service_account_id" {
  value = yandex_iam_service_account.runtime.id
}

output "ci_service_account_id" {
  value = yandex_iam_service_account.ci.id
}

output "lockbox_secret_id" {
  value = yandex_lockbox_secret.runtime.id
}

output "collector_url" {
  value = "https://${local.collector_domain}"
}
