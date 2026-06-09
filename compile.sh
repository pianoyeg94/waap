#!/usr/bin/env bash
GOPATH="${1:-"${HOME}/go"}"
export GOPATH
export CGO_ENABLED=1

go build
sudo setcap "cap_dac_override+ep" ./waap
