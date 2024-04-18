package goproxy

import (
	"bufio"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func headerContains(header http.Header, name string, value string) bool {
	for _, v := range header[name] {
		for _, s := range strings.Split(v, ",") {
			if strings.EqualFold(value, strings.TrimSpace(s)) {
				return true
			}
		}
	}
	return false
}

func isWebSocketRequest(r *http.Request) bool {
	return headerContains(r.Header, "Connection", "upgrade") &&
		headerContains(r.Header, "Upgrade", "websocket")
}

func (proxy *ProxyHttpServer) serveWebsocketTLS(ctx *ProxyCtx, w http.ResponseWriter, req *http.Request, tlsConfig *tls.Config, clientConn *tls.Conn) {
	targetURL := url.URL{Scheme: "wss", Host: req.URL.Host, Path: req.URL.Path}

	// Connect to upstream
	targetConn, err := tls.Dial("tcp", targetURL.Host, tlsConfig)
	if err != nil {
		ctx.Warnf("Error dialing target site: %v", err)
		return
	}
	defer targetConn.Close()

	// Perform handshake
	if err := proxy.websocketHandshake(ctx, req, targetConn, clientConn); err != nil {
		ctx.Warnf("Websocket handshake error: %v", err)
		return
	}

	// Proxy wss connection
	proxy.proxyWebsocket(ctx, targetConn, clientConn)
}

func (proxy *ProxyHttpServer) serveWebsocket(ctx *ProxyCtx, w http.ResponseWriter, req *http.Request) {
	targetURL := url.URL{Scheme: "ws", Host: req.URL.Host, Path: req.URL.Path}

	targetConn, err := proxy.connectDial(ctx, "tcp", targetURL.Host)
	if err != nil {
		ctx.Warnf("Error dialing target site: %v", err)
		return
	}
	defer targetConn.Close()

	// Connect to Client
	hj, ok := w.(http.Hijacker)
	if !ok {
		panic("httpserver does not support hijacking")
	}
	clientConn, _, err := hj.Hijack()
	if err != nil {
		ctx.Warnf("Hijack error: %v", err)
		return
	}

	// Perform handshake
	if err := proxy.websocketHandshake(ctx, req, targetConn, clientConn); err != nil {
		ctx.Warnf("Websocket handshake error: %v", err)
		return
	}

	// Proxy ws connection
	proxy.proxyWebsocket(ctx, targetConn, clientConn)
}

func (proxy *ProxyHttpServer) websocketHandshake(ctx *ProxyCtx, req *http.Request, targetSiteConn io.ReadWriter, clientConn io.ReadWriter) error {
	// write handshake request to target
	err := req.Write(targetSiteConn)
	if err != nil {
		ctx.Warnf("Error writing upgrade request: %v", err)
		return err
	}

	targetTLSReader := bufio.NewReader(targetSiteConn)

	// Read handshake response from target
	resp, err := http.ReadResponse(targetTLSReader, req)
	if err != nil {
		ctx.Warnf("Error reading handhsake response  %v", err)
		return err
	}

	// Run response through handlers
	resp = proxy.filterResponse(resp, ctx)

	// Proxy handshake back to client
	err = resp.Write(clientConn)
	if err != nil {
		ctx.Warnf("Error writing handshake response: %v", err)
		return err
	}
	return nil
}

type WebSocketFrame struct {
	Fin     bool   // 是否是结束帧
	Opcode  int    // 操作码
	Payload []byte // 实际有效载荷
}

func ParseWebSocketFrame(data []byte) (*WebSocketFrame, error) {
	if len(data) < 2 {
		return nil, errors.New("frame is too short")
	}

	frame := &WebSocketFrame{}

	// 解析FIN位和操作码
	frame.Fin = (data[0] & 0x80) != 0
	frame.Opcode = int(data[0] & 0x0F)

	// 解析掩码和负载长度
	mask := (data[1] & 0x80) != 0
	payloadLength := int(data[1] & 0x7F)

	offset := 2
	if payloadLength == 126 {
		if len(data) < 4 {
			return nil, errors.New("frame is too short for 126")
		}
		payloadLength = int(data[2])<<8 | int(data[3])
		offset += 2
	} else if payloadLength == 127 {
		if len(data) < 10 {
			return nil, errors.New("frame is too short for 127")
		}
		payloadLength = int(data[2])<<56 | int(data[3])<<48 | int(data[4])<<40 | int(data[5])<<32 | int(data[6])<<24 | int(data[7])<<16 | int(data[8])<<8 | int(data[9])
		offset += 8
	}

	// 解析掩码键和数据
	if mask {
		if len(data) < offset+4+payloadLength {
			return nil, errors.New("frame is too short for masking")
		}
		maskKey := data[offset : offset+4]
		offset += 4
		frame.Payload = make([]byte, payloadLength)
		for i := 0; i < payloadLength; i++ {
			frame.Payload[i] = data[offset+i] ^ maskKey[i%4]
		}
	} else {
		if len(data) < offset+payloadLength {
			return nil, errors.New("frame is too short for payload")
		}
		frame.Payload = data[offset : offset+payloadLength]
	}

	return frame, nil
}

func (proxy *ProxyHttpServer) proxyWebsocket(ctx *ProxyCtx, dest io.ReadWriter, source io.ReadWriter) {
	errChan := make(chan error, 2)
	cp := proxy.WssHandler

	// Start proxying websocket data
	go cp(dest, source)
	go cp(source, dest)
	<-errChan
}
