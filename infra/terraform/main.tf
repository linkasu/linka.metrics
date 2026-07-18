locals {
  collector_domain = "plays-metric.nkolinka.ru"
  writer_domain    = "plays-metric-writer.nkolinka.ru"
  db_domain        = "plays-metric-db.nkolinka.ru"
  vps_ipv4         = "37.230.192.57"
}

resource "yandex_container_registry" "metric" {
  provider = yandex.metric
  name     = "linka-plays-metric-dedicated"
}

resource "yandex_iam_service_account" "runtime" {
  provider = yandex.metric
  name     = "linka-plays-metric-runtime-dedicated"
}

resource "yandex_iam_service_account" "ci" {
  provider = yandex.metric
  name     = "linka-plays-metric-ci-dedicated"
}

resource "yandex_iam_service_account" "terraform_state" {
  name        = "linka-plays-metric-terraform-state"
  description = "Retained in the shared folder for access to the Terraform state bucket"
}

resource "yandex_resourcemanager_folder_iam_member" "runtime_lockbox" {
  provider  = yandex.metric
  folder_id = var.metric_folder_id
  role      = "lockbox.payloadViewer"
  member    = "serviceAccount:${yandex_iam_service_account.runtime.id}"
}

resource "yandex_resourcemanager_folder_iam_member" "runtime_invoker" {
  provider  = yandex.metric
  folder_id = var.metric_folder_id
  role      = "serverless-containers.containerInvoker"
  member    = "serviceAccount:${yandex_iam_service_account.runtime.id}"
}

resource "yandex_resourcemanager_folder_iam_member" "runtime_registry" {
  provider  = yandex.metric
  folder_id = var.metric_folder_id
  role      = "container-registry.images.puller"
  member    = "serviceAccount:${yandex_iam_service_account.runtime.id}"
}

resource "yandex_resourcemanager_folder_iam_member" "ci_editor" {
  provider  = yandex.metric
  folder_id = var.metric_folder_id
  role      = "editor"
  member    = "serviceAccount:${yandex_iam_service_account.ci.id}"
}

resource "yandex_iam_service_account_iam_member" "ci_use_runtime_service_account" {
  provider           = yandex.metric
  service_account_id = yandex_iam_service_account.runtime.id
  role               = "iam.serviceAccounts.user"
  member             = "serviceAccount:${yandex_iam_service_account.ci.id}"
}

resource "yandex_resourcemanager_folder_iam_member" "ci_terraform_state" {
  folder_id = var.folder_id
  role      = "storage.editor"
  member    = "serviceAccount:${yandex_iam_service_account.terraform_state.id}"
}

resource "yandex_lockbox_secret" "runtime" {
  provider            = yandex.metric
  name                = "linka-plays-metric-runtime"
  deletion_protection = true
  description         = "Payload is created and rotated outside Terraform"
}

resource "yandex_serverless_container" "collector" {
  provider           = yandex.metric
  name               = "linka-plays-metric-collector"
  description        = "LINKa Plays telemetry collector in dedicated metrics folder"
  memory             = 256
  cores              = 1
  core_fraction      = 100
  execution_timeout  = "15s"
  concurrency        = 16
  service_account_id = yandex_iam_service_account.runtime.id

  runtime {
    type = "http"
  }

  image {
    url = var.collector_image_url
    environment = {
      LISTEN_ADDR = ":8080"
      WRITER_URL  = var.writer_url
    }
  }

  secrets {
    id                   = yandex_lockbox_secret.runtime.id
    version_id           = var.lockbox_secret_version_id
    key                  = "installation_hmac_secret"
    environment_variable = "INSTALLATION_HMAC_SECRET"
  }

  secrets {
    id                   = yandex_lockbox_secret.runtime.id
    version_id           = var.lockbox_secret_version_id
    key                  = "writer_hmac_secret"
    environment_variable = "WRITER_HMAC_SECRET"
  }

  depends_on = [
    yandex_resourcemanager_folder_iam_member.runtime_lockbox,
    yandex_resourcemanager_folder_iam_member.runtime_registry,
  ]

  lifecycle {
    ignore_changes = [image[0].url]
  }
}

resource "yandex_cm_certificate" "collector" {
  name    = "linka-plays-metric"
  domains = [local.collector_domain]

  managed {
    challenge_type  = "DNS_CNAME"
    challenge_count = 1
  }
}

resource "yandex_dns_recordset" "certificate_validation" {
  count   = yandex_cm_certificate.collector.managed[0].challenge_count
  zone_id = var.dns_zone_id
  name    = yandex_cm_certificate.collector.challenges[count.index].dns_name
  type    = yandex_cm_certificate.collector.challenges[count.index].dns_type
  ttl     = 60
  data    = [yandex_cm_certificate.collector.challenges[count.index].dns_value]
}

resource "yandex_api_gateway" "metric" {
  name = "linka-plays-metric"

  custom_domains {
    fqdn           = local.collector_domain
    certificate_id = yandex_cm_certificate.collector.id
  }

  spec = <<-YAML
    openapi: 3.0.0
    info:
      title: LINKa Plays Metric
      version: 1.0.0
    paths:
      /:
        x-yc-apigateway-any-method:
          x-yc-apigateway-integration:
            type: serverless_containers
            container_id: ${yandex_serverless_container.collector.id}
            service_account_id: ${yandex_iam_service_account.runtime.id}
      /{proxy+}:
        x-yc-apigateway-any-method:
          parameters:
            - name: proxy
              in: path
              required: false
              explode: false
              style: simple
              schema:
                type: string
                default: '-'
          x-yc-apigateway-integration:
            type: serverless_containers
            container_id: ${yandex_serverless_container.collector.id}
            service_account_id: ${yandex_iam_service_account.runtime.id}
  YAML

  depends_on = [
    yandex_dns_recordset.certificate_validation,
    yandex_resourcemanager_folder_iam_member.runtime_invoker,
  ]
}

resource "yandex_dns_recordset" "collector" {
  zone_id = var.dns_zone_id
  name    = "${local.collector_domain}."
  type    = "CNAME"
  ttl     = 300
  data    = ["${trimsuffix(yandex_api_gateway.metric.domain, ".")}."]
}

resource "yandex_dns_recordset" "writer" {
  zone_id = var.dns_zone_id
  name    = "${local.writer_domain}."
  type    = "A"
  ttl     = 300
  data    = [local.vps_ipv4]
}

resource "yandex_dns_recordset" "database" {
  zone_id = var.dns_zone_id
  name    = "${local.db_domain}."
  type    = "A"
  ttl     = 300
  data    = [local.vps_ipv4]
}
