#!/usr/bin/sh

mkdir -p public
go build -tags netgo -ldflags '-s -w' -o ./public/server.bin ./server
sh ./build_web.sh
