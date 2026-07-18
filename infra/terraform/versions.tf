terraform {
  required_version = ">= 1.8.0"

  backend "s3" {
    bucket = "linka-plays-metric-tfstate-b1gn4stour811vgtjude"
    key    = "production/terraform.tfstate"
    region = "ru-central1"

    endpoints = {
      s3 = "https://storage.yandexcloud.net"
    }

    skip_region_validation      = true
    skip_credentials_validation = true
    skip_requesting_account_id  = true
    skip_s3_checksum            = true
    use_path_style              = true
    use_lockfile                = true
  }

  required_providers {
    yandex = {
      source  = "yandex-cloud/yandex"
      version = "~> 0.217.0"
    }
  }
}

provider "yandex" {
  cloud_id  = var.cloud_id
  folder_id = var.folder_id
  zone      = var.zone
}

provider "yandex" {
  alias     = "metric"
  cloud_id  = var.cloud_id
  folder_id = var.metric_folder_id
  zone      = var.zone
}
