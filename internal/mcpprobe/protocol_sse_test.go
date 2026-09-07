package mcpprobe

import "testing"

func TestDecodeSSEIgnoresNotificationsAndMatchesInitialize(t *testing.T) {
	body := "event: progress\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n" +
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\ndata: \"id\":1,\"result\":{\"protocolVersion\":\"2025-06-18\"}}\n\n"
	raw, err := decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"protocolVersion":"2025-06-18"}` {
		t.Fatalf("payload = %s", raw)
	}
}
