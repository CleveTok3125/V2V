//go:build !js

package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/gorilla/websocket"
)

// resolvedProxy caches the session proxy: the wizard must ask once
// even though dialWS runs again on ws->wss retry.
var resolvedProxy *proxyConfig
var proxyResolved bool
var proxyResolveErr error

func dialWS(wsURL string) (wsConn, *http.Response, error) {
	headers := http.Header{}
	headers.Add("User-Agent", CLI.UserAgent)

	if !proxyResolved {
		proxyResolved = true
		resolvedProxy, proxyResolveErr = resolveProxy(os.Stdin)
	}
	if proxyResolveErr != nil {
		return nil, nil, proxyResolveErr
	}
	if resolvedProxy == nil {
		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
		return conn, resp, err
	}
	fmt.Printf("🔀 Proxy: %s\n", resolvedProxy.logString())
	if resolvedProxy.Scheme == "socks5" {
		return dialSocks5WS(wsURL, headers, resolvedProxy)
	}
	d := *websocket.DefaultDialer
	d.Proxy = http.ProxyURL(resolvedProxy.dialURL())
	conn, resp, err := d.Dial(wsURL, headers)
	return conn, resp, err
}
