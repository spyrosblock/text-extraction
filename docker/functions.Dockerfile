# Local dev image for the Function apps (producer, consumer, account), used by
# docker-compose.yml. Not used in Azure: there the apps are zip-deployed to
# Flex Consumption. Build context: the repo root (the apps import libs/).
#   docker build -f docker/functions.Dockerfile --build-arg APP=producer .
FROM golang:1.26-trixie AS build
ARG APP
WORKDIR /src
COPY libs/ libs/
COPY services/${APP}/go.mod services/${APP}/go.sum services/${APP}/
RUN cd services/${APP} && go mod download
COPY services/${APP}/ services/${APP}/
RUN cd services/${APP} && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/app .

# The image Flex Consumption runs native (Go) apps on. Its entrypoint is the
# platform's launcher, which needs secrets only Azure provides, so this starts
# the Functions host directly. Same layout as the deployed zip: app + host.json.
FROM mcr.microsoft.com/azure-functions/noble/flexconsumption:4.1055.300-1-native1.0
ARG APP
COPY --from=build /out/app /home/site/wwwroot/app
COPY services/${APP}/host.json /home/site/wwwroot/host.json
ENV ASPNETCORE_URLS=http://0.0.0.0:8080
EXPOSE 8080
ENTRYPOINT ["/azure-functions-host/Microsoft.Azure.WebJobs.Script.WebHost"]
