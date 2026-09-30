THIS SHOULD NOT BE UPDATED!!!

curl -H 'Content-Type: application/pdf' --data-binary @test1.pdf http://localhost:7071/api/extract

curl -X POST http://localhost:7072/api/text \
  -H 'Content-Type: application/json' -d '{"id": "<uuid>"}'