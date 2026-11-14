for i in $(seq 101); do
    curl https://func-textextract-account-staging-540b2e.azurewebsites.net/api/me -w '%{http_code}\n'
done