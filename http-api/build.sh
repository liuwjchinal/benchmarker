#!/bin/bash

set -e

# FIXME: get credentials `benchmarkerPostgres` and `httpAPITokens`

env GOOS=linux GOARCH=amd64 go build -o http-api *.go
docker build -t http-api .
rm http-api

docker tag http-api 633007691302.dkr.ecr.us-east-1.amazonaws.com/http-api:latest
docker push 633007691302.dkr.ecr.us-east-1.amazonaws.com/http-api:latest
