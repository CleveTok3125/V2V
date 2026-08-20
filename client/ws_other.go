//go:build !js

package main

import (
	"net/http"

	"github.com/gorilla/websocket"
)

func dialWS(wsURL string) (wsConn, *http.Response, error) {
	headers := http.Header{}
	headers.Add("User-Agent", CLI.UserAgent)

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
	return conn, resp, err
}