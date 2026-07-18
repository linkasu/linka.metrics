# Terraform bootstrap YC

Конфигурация разделяет инфраструктуру между shared folder и выделенным metrics folder. Shared folder содержит DNS, API Gateway, сертификат и bucket состояния; metrics folder содержит Container Registry, service accounts runtime/CI, пустой Lockbox secret и Serverless Container. CI получает `editor` только в metrics folder. Payload Lockbox намеренно не описан Terraform и не попадает в state.

## Порядок bootstrap

1. Создайте закрытый versioned bucket `linka-plays-metric-tfstate-b1gn4stour811vgtjude`, выдайте отдельному Terraform state service account роль `storage.editor` и передайте его static access key через `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`.
2. Передайте `cloud_id`, shared `folder_id`, выделенный `metric_folder_id`, `zone`, `dns_zone_id`, immutable `collector_image_url`.
3. Создайте только контейнер Lockbox: `terraform apply -target=yandex_lockbox_secret.runtime`.
4. В UI или YC CLI создайте версию secret с независимыми ключами `installation_hmac_active_secret`, `installation_hmac_previous_secret`, `writer_hmac_secret` и `service_hmac_active_secret`, каждый минимум 32 случайных байта. Previous installation key сохраняется минимум на максимальный lifetime token; renewal всегда подписывается active key. Product JWT и pairwise identifiers выпускает LINKa Identity.
5. Передайте только ID версии как `lockbox_secret_version_id` и выполните обычные `terraform plan`/`terraform apply` после проверки плана.
6. Отдельно примените certificate challenge record: `terraform apply -target=yandex_dns_recordset.certificate_validation`, дождитесь статуса сертификата `ISSUED`, затем выполните полный apply. Это исключает гонку между выдачей сертификата и созданием custom domain API Gateway.

Репозиторий не выполняет `terraform apply`. Не передавайте payload через `.tfvars`: даже sensitive variables сохраняются в state. `terraform.tfvars.example` содержит только фиктивные идентификаторы.

Serverless Containers обычно получает образы из Yandex Container Registry. CI публикует GHCR-образ; перед deploy его следует зеркалировать в созданный registry отдельным доверенным CI job без сборки либо указать уже доступный `cr.yandex/...` URL.

Deploy CI service account получает `editor` только в выделенном metrics folder и direct `iam.serviceAccounts.user` на runtime account. Отдельный service account в shared folder сохраняет только доступ к bucket состояния. Создание ключей и их помещение в GitHub secrets выполняются вне Terraform, чтобы закрытые ключи не оказались в state.

API Gateway применяет бесплатный глобальный лимит 100 requests/second из OpenAPI specification. Deploy workflow читает production gateway по `YC_API_GATEWAY_ID` и останавливается, если лимит отсутствует. Collector и writer независимо ограничивают тело batch до 512 KiB. Alert при 80% folder quota настраивается отдельно; YC quota ceilings не описываются текущим Terraform provider.
