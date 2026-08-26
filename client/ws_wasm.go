//go:build js

package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"syscall/js"
	"time"
)

// wasmWSConn shims the browser's native WebSocket onto the small wsConn
// surface used by the chat loop. The browser handles the handshake, framing
// and ping/pong automatically, so only message delivery and close are bridged.
type wasmWSConn struct {
	ws        js.Value
	recvCh    chan wsFrame
	callbacks []js.Func
	eofOnce   sync.Once
	relOnce   sync.Once
}

type wsFrame struct {
	msgType int
	data    []byte
	err     error
}

func dialWS(wsURL string) (wsConn, *http.Response, error) {
	wsObj := js.Global().Get("WebSocket")
	if !wsObj.Truthy() {
		return nil, nil, errors.New("WebSocket API không khả dụng trong trình duyệt này")
	}

	ws := wsObj.New(wsURL)
	conn := &wasmWSConn{ws: ws, recvCh: make(chan wsFrame, 64)}

	openCh := make(chan error, 1)

	onOpen := js.FuncOf(func(this js.Value, args []js.Value) any {
		openCh <- nil
		return nil
	})
	onError := js.FuncOf(func(this js.Value, args []js.Value) any {
		openCh <- errors.New("WebSocket lỗi khi kết nối")
		return nil
	})
	onMessage := js.FuncOf(func(this js.Value, args []js.Value) any {
		ev := args[0]
		data := ev.Get("data")
		if !data.Truthy() {
			return nil
		}
		conn.recvCh <- wsFrame{msgType: wsTextMessage, data: []byte(data.String())}
		return nil
	})
	onClose := js.FuncOf(func(this js.Value, args []js.Value) any {
		conn.eofOnce.Do(func() {
			conn.recvCh <- wsFrame{err: io.EOF}
		})
		conn.release()
		return nil
	})

	conn.callbacks = append(conn.callbacks, onOpen, onError, onMessage, onClose)
	ws.Set("onopen", onOpen)
	ws.Set("onerror", onError)
	ws.Set("onmessage", onMessage)
	ws.Set("onclose", onClose)

	select {
	case err := <-openCh:
		if err != nil {
			conn.close()
			return nil, nil, err
		}
	case <-time.After(20 * time.Second):
		conn.close()
		return nil, nil, errors.New("timeout khi kết nối WebSocket")
	}

	return conn, nil, nil
}

func (c *wasmWSConn) ReadMessage() (int, []byte, error) {
	f := <-c.recvCh
	return f.msgType, f.data, f.err
}

func (c *wasmWSConn) ReadJSON(v any) error {
	msgType, data, err := c.ReadMessage()
	if err != nil {
		return err
	}
	if msgType == wsCloseMessage {
		return io.EOF
	}
	return json.Unmarshal(data, v)
}

func (c *wasmWSConn) WriteMessage(messageType int, data []byte) error {
	switch messageType {
	case wsCloseMessage:
		c.ws.Call("close")
		return nil
	default:
		c.ws.Call("send", string(data))
		return nil
	}
}

func (c *wasmWSConn) WriteJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.WriteMessage(wsTextMessage, b)
}

func (c *wasmWSConn) Close() error {
	c.close()
	return nil
}

func (c *wasmWSConn) close() {
	c.ws.Call("close")
	c.release()
}

func (c *wasmWSConn) release() {
	c.relOnce.Do(func() {
		for _, f := range c.callbacks {
			f.Release()
		}
		c.callbacks = nil
	})
}
