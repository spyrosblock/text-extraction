### create key
curl -X POST -d '{"name": "sdf", "credits": 5}' -H "x-functions-key: <ADMIN_FUNCTION_KEY>" https://func-textextract-producer-staging-540b2e.azurewebsites.net/api/manage/keys
### list keys
curl -H "x-functions-key: <ADMIN_FUNCTION_KEY>" https://func-textextract-producer-staging-540b2e.azurewebsites.net/api/manage/keys
### delete keys
curl -X DELETE -H "x-functions-key: <ADMIN_FUNCTION_KEY>" https://func-textextract-producer-staging-540b2e.azurewebsites.net/api/manage/keys?key_id=9af7acbd134f1f27b413f7bdfd042ad416b75a77eda3950eaff50649222c3df0 -w "%{http_code}\n"

### set balance
curl -X PUT -d '{"credits": 10}' -H "x-functions-key: <ADMIN_FUNCTION_KEY>" https://func-textextract-producer-staging-540b2e.azurewebsites.net/api/manage/credits?key_id=d1b2e33a1b4d2c1a7400e509b31f17b0b703c534b6d209bd2ea3358229b2a132 -w "%{http_code}\n"
### add to balance
curl -X POST -d '{"delta": 10}' -H "x-functions-key: <ADMIN_FUNCTION_KEY>" https://func-textextract-producer-staging-540b2e.azurewebsites.net/api/manage/credits?key_id=d1b2e33a1b4d2c1a7400e509b31f17b0b703c534b6d209bd2ea3358229b2a132 -w "%{http_code}\n"

### submit for extraction
curl -H "X-API-Key: <USER_API_KEY>" -H "Content-Type: application/pdf" --data-binary @test1.pdf https://func-textextract-producer-staging-540b2e.azurewebsites.net/api/extract -w "%{http_code}\n"

### get text
curl -X POST -d '{"request_id": "08eebb10-4bfb-4bfe-aaf7-8e5fbfe5efbb"}' -H "X-API-Key: <USER_API_KEY>" https://func-textextract-consumer-staging-540b2e.azurewebsites.net/api/text -w "%{http_code}\n"

