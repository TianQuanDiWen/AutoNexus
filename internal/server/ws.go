package server

import (
	"encoding/json"
	"net/http"
	"time"

	"autonexus/internal/executor"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// 允许局域网内所有设备访问（手机、平板、其他 PC）
		return true
	},
}

// handleWSLogs 处理 WebSocket 日志订阅连接
func (s *Server) handleWSLogs(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ch, history := s.broadcaster.Subscribe(500)
	defer s.broadcaster.Unsubscribe(ch)

	// 1. 发送连接成功消息与历史日志批处理
	type InitMessage struct {
		Type    string              `json:"type"`
		History []executor.LogEntry `json:"history"`
	}

	initData, _ := json.Marshal(InitMessage{
		Type:    "history",
		History: history,
	})
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, initData); err != nil {
		return
	}

	// 2. 启动心跳 keep-alive 与接收客户端 pong
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// 启动协程持续读取客户端（用于处理 ping/pong 和断开检测）
	clientDisconnect := make(chan struct{})
	go func() {
		defer close(clientDisconnect)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// 3. 实时推流循环
	for {
		select {
		case <-clientDisconnect:
			return

		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second)); err != nil {
				return
			}

		case entry, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}
}
