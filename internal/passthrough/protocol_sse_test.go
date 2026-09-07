package passthrough

import (
	"encoding/json"
	"testing"
)

func TestDecodeSSESelectsMatchingReplyAndJoinsData(t *testing.T) {
	body := "event: progress\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n" +
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\ndata: \"id\":7,\"result\":{\"ok\":true}}\n\n"
	raw, err := decode(body, 7)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || payload["id"] != float64(7) {
		t.Fatalf("payload = %s, %v", raw, err)
	}
}

func TestDecodeSSERejectsWrongIDOnly(t *testing.T) {
	if _, err := decode("data: {\"jsonrpc\":\"2.0\",\"id\":8,\"result\":{}}\n\n", 7); err == nil {
		t.Fatal("wrong id was accepted")
	}
}
