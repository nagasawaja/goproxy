package main

import (
	"crypto/tls"
	"fmt"
	"github.com/elazarl/goproxy"
	"github.com/gorilla/websocket"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"time"
)

var upgrader = websocket.Upgrader{} // use default options

func echo(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Print("upgrade:", err)
		return
	}
	defer c.Close()
	for {
		mt, message, err := c.ReadMessage()
		if err != nil {
			log.Println("read:", err)
			break
		}
		log.Printf("recv: %s", message)
		err = c.WriteMessage(mt, message)
		if err != nil {
			log.Println("write:", err)
			break
		}
	}
}

func StartEchoServer(wg *sync.WaitGroup) {
	log.Println("Starting echo server")
	wg.Add(1)
	go func() {
		http.HandleFunc("/", echo)
		err := http.ListenAndServeTLS(":12345", "localhost.pem", "localhost-key.pem", nil)
		if err != nil {
			panic("ListenAndServe: " + err.Error())
		}
		wg.Done()
	}()
}

func StartProxy(wg *sync.WaitGroup) {
	log.Println("Starting proxy server")
	wg.Add(1)
	go func() {
		proxy := goproxy.NewProxyHttpServer()
		proxy.WssHandler = func(dst io.Writer, src io.Reader) error {
			buf := make([]byte, 32*1024) // 创建一个32KB的缓冲区
			for {
				n, err := src.Read(buf)
				if err != nil && err != io.EOF {
					return nil
				}
				tt, _ := goproxy.ParseWebSocketFrame(buf)
				_ = tt
				if n > 0 {
					data := buf[:n]
					// 打印从WebSocket连接读取的数据
					fmt.Printf("cp data; data:%+v\n", string(tt.Payload))
					// 此处也可以将数据转换为字符串打印，如果知道它是文本
					// fmt.Printf("Data: %s\n", string(data))

					_, err = dst.Write(data)
					if err != nil {
						return nil
					}
				}
				if err == io.EOF {
					break
				}
			}
			return nil
		}
		proxy.OnRequest().HandleConnect(goproxy.AlwaysMitm)
		proxy.Verbose = true

		err := http.ListenAndServe(":54321", proxy)
		if err != nil {
			log.Fatal(err.Error())
		}
		wg.Done()
	}()
}

func main() {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	wg := &sync.WaitGroup{}
	StartEchoServer(wg)
	StartProxy(wg)

	endpointUrl := "wss://localhost:12345"
	proxyUrl := "http://localhost:54321"

	surl, _ := url.Parse(proxyUrl)
	dialer := websocket.Dialer{
		Subprotocols:    []string{"p1"},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		Proxy:           http.ProxyURL(surl),
	}

	c, _, err := dialer.Dial(endpointUrl, nil)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer c.Close()

	done := make(chan struct{})

	go func() {
		defer c.Close()
		defer close(done)
		for {
			_, message, err := c.ReadMessage()
			if err != nil {
				log.Println("read:", err)
				return
			}
			log.Printf("recv: %s", message)
		}
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case t := <-ticker.C:
			err := c.WriteMessage(websocket.TextMessage, []byte(t.String()))
			if err != nil {
				log.Println("write:", err)
				return
			}
		case <-interrupt:
			log.Println("interrupt")
			// To cleanly close a connection, a client should send a close
			// frame and wait for the server to close the connection.
			err := c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				log.Println("write close:", err)
				return
			}
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			c.Close()
			return
		}
	}
}
