# smoke.sh and the workflows read these from `terraform output -json`.
output "producerName" {
  value = module.producer.name
}

output "producerHostName" {
  value = module.producer.host_name
}

output "consumerName" {
  value = module.consumer.name
}

output "consumerHostName" {
  value = module.consumer.host_name
}

output "ocrJobName" {
  value = module.ocr_job.job_name
}

output "dataStorageName" {
  value = module.data_storage.name
}

output "serviceBusName" {
  value = module.service_bus.name
}
