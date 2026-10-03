THIS SHOULD NOT BE UPDATED!!!

curl -H 'Content-Type: application/pdf' --data-binary @test1.pdf http://localhost:7071/api/extract

curl -X POST http://localhost:7072/api/text \
  -H 'Content-Type: application/json' -d '{"id": "<uuid>"}'

curl -sS -H "x-functions-key: $KEY" -H 'Content-Type: application/pdf' \
  --data-binary @test1.pdf \
  https://func-textextract-producer-staging-i354px.azurewebsites.net/api/extract

curl -sS -X POST -H 'Content-Type: application/json' \
  -d '{"id": "<uuid>"}' \
  https://func-textextract-consumer-staging-i354px.azurewebsites.net/api/text
