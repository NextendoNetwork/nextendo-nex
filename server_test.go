package nex

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// TestServerWebSocketSmoke validates the whole transport over a real WebSocket:
// a client dials, sends a SYN, and must receive a SYN+ACK.
func TestServerWebSocketSmoke(t *testing.T) {
	ep := NewEndpoint(testSettings())
	srv := NewServer(ep)

	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if err := client.WriteMessage(websocket.BinaryMessage, EncodePacket(synPacket())); err != nil {
		t.Fatalf("write SYN: %v", err)
	}

	_, data, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("read SYN+ACK: %v", err)
	}
	p := decodeOne(t, data)
	if p.Type != PacketSYN || !p.HasFlag(FlagACK) || len(p.ConnectionSig) != 16 {
		t.Fatalf("expected SYN+ACK, got %+v", p)
	}
}
