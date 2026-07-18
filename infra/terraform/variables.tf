variable "cloud_id" {
  description = "Yandex Cloud ID"
  type        = string
}

variable "folder_id" {
  description = "Shared Yandex Cloud folder ID containing DNS, API Gateway, certificate, and Terraform state"
  type        = string
}

variable "metric_folder_id" {
  description = "Dedicated Yandex Cloud folder ID containing telemetry runtime and deployment resources"
  type        = string
}

variable "zone" {
  description = "Default availability zone"
  type        = string
  default     = "ru-central1-a"
}

variable "dns_zone_id" {
  description = "Existing public Cloud DNS zone ID for nkolinka.ru"
  type        = string
}

variable "collector_image_url" {
  description = "Immutable collector image URL available to Serverless Containers"
  type        = string
}

variable "writer_url" {
  description = "Public HTTPS base URL of the writer"
  type        = string
  default     = "https://plays-metric-writer.nkolinka.ru"
}

variable "lockbox_secret_version_id" {
  description = "ID of a manually created Lockbox version; payload is never managed by Terraform"
  type        = string
}
