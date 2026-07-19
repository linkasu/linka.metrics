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

variable "service_hmac_active_key_id" {
  description = "Non-secret active collector-to-writer V2 HMAC key ID"
  type        = string
  default     = "collector-2026-07"
}

variable "installation_hmac_active_key_id" {
  description = "Active installation-token HMAC key ID"
  type        = string
  default     = "installation-2026-08"
}

variable "installation_hmac_previous_key_id" {
  description = "Previous installation-token HMAC key ID retained for verification"
  type        = string
  default     = "installation-2026-07"
}

variable "identity_jwks_url" {
  description = "HTTPS URL of the LINKa Identity JWKS endpoint"
  type        = string

  validation {
    condition     = startswith(var.identity_jwks_url, "https://")
    error_message = "identity_jwks_url must use HTTPS."
  }
}

variable "identity_token_issuer" {
  description = "Exact issuer expected in LINKa Identity JWTs"
  type        = string

  validation {
    condition     = length(trimspace(var.identity_token_issuer)) > 0
    error_message = "identity_token_issuer must not be empty."
  }
}

variable "identity_telemetry_audiences" {
  description = "Exact product-to-audience map expected in LINKa Identity JWTs"
  type        = map(string)
  default = {
    linka-plays      = "linka-plays-metric"
    linka-looks      = "linka-looks-metric"
    linka-pictures   = "linka-pictures-metric"
    linka-type       = "linka-type-metric"
    linka-paperboard = "linka-paperboard-metric"
    linka-site       = "linka-site-metric"
    linka-tts        = "linka-tts-metric"
  }
}

variable "cors_allowed_origins" {
  description = "Exact semicolon-separated HTTPS origins allowed to call the public collector from browsers"
  type        = string
  default     = "https://linka.su;https://linkatype.web.app"
}

variable "lockbox_secret_version_id" {
  description = "ID of a manually created Lockbox version; payload is never managed by Terraform"
  type        = string
}
