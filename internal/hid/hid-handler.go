package hid

/*
	WebSocket input handler: commands are routed through Controller.Handle and
	commands with a reply ID get an ACK.
*/

import (
	"encoding/json"
	"kvmostb/internal/logging"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type ackReply struct {
	Rid    string `json:"rid"`
	Status string `json:"status"` // "ok" or "error"
}

// WebSocketHandler serves /ws/hid for one controller. Each connection has a
// bounded queue of 10 commands and drops the oldest when full.
func WebSocketHandler(c *Controller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logging.Errorf("websocket upgrade: %v", err)
			return
		}
		defer conn.Close()

		var wsMu sync.Mutex

		const maxQueueSize = 10
		cmdQueue := make(chan *HIDCommand, maxQueueSize)
		done := make(chan struct{})

		go func() {
			defer close(done)
			for cmd := range cmdQueue {
				err := c.Handle(cmd)

				if cmd.Rid != "" {
					status := "ok"
					if err != nil {
						status = "error"
					}
					wsMu.Lock()
					if writeErr := conn.WriteJSON(ackReply{Rid: cmd.Rid, Status: status}); writeErr != nil {
						logging.Debugf("websocket ACK write: %v", writeErr)
					}
					wsMu.Unlock()
				} else if err != nil {
					logging.Errorf("HID command send: %v", err)
				}
			}
		}()

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				if !strings.Contains(err.Error(), "close") {
					logging.Debugf("websocket read: %v", err)
				}
				break
			}

			var cmd HIDCommand
			if err := json.Unmarshal(message, &cmd); err != nil {
				logging.Warnf("websocket message parse: %v", err)
				continue
			}

			// Record activity for mouse jiggler idle detection
			c.RecordActivity()

			// Commands with a reply ID are never dropped: the client waits for the ACK.
			if cmd.Rid != "" {
				cmdQueue <- &cmd
				continue
			}

			// Fire-and-forget: enqueue, or drop the oldest when full
			select {
			case cmdQueue <- &cmd:
			default:
				<-cmdQueue
				cmdQueue <- &cmd
			}
		}

		close(cmdQueue)
		<-done
	}
}
