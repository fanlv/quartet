package lark

import (
	"reflect"
	"sync"
	"testing"
	"time"

	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// TestLarkWSRuntimeSDKInternalsGuard makes the intentional dependency on
// larkws.Client's private internals fail fast during tests when the pinned SDK
// is upgraded. The runtime in ws_runtime.go uses go:linkname plus unsafe
// reflection so Manager.Restart can cancel the WebSocket cleanly; without this
// guard, SDK field drift could become a production panic instead of a CI error.
func TestLarkWSRuntimeSDKInternalsGuard(t *testing.T) {
	clientType := reflect.TypeOf(larkws.Client{})
	wantFields := map[string]reflect.Type{
		"conn":          reflect.TypeOf((*struct{})(nil)), // checked separately: only nil-ability matters here.
		"serviceID":     reflect.TypeOf(""),
		"pingInterval":  reflect.TypeOf(time.Duration(0)),
		"autoReconnect": reflect.TypeOf(false),
		"mu":            reflect.TypeOf(sync.Mutex{}),
	}

	for name, wantType := range wantFields {
		field, ok := clientType.FieldByName(name)
		if !ok {
			t.Fatalf("larkws.Client missing private field %q used by ws_runtime.go", name)
		}
		if name == "conn" {
			if field.Type.Kind() != reflect.Ptr {
				t.Fatalf("larkws.Client.conn should remain pointer-like for nil checks, got %s", field.Type)
			}
			continue
		}
		if field.Type != wantType {
			t.Fatalf("larkws.Client.%s type changed: got %s, want %s", name, field.Type, wantType)
		}
	}
}

func TestLarkWSRuntimeHelpersMatchPinnedSDK(t *testing.T) {
	client := larkws.NewClient("app-id", "app-secret", larkws.WithAutoReconnect(true))

	if larkWSHasConn(client) {
		t.Fatal("new larkws.Client should not have an active connection")
	}
	if got := larkWSServiceID(client); got != 0 {
		t.Fatalf("new larkws.Client should have empty serviceID mapped to 0, got %d", got)
	}
	if got := larkWSPingInterval(client); got != 2*time.Minute {
		t.Fatalf("unexpected default ping interval: got %s, want %s", got, 2*time.Minute)
	}

	setLarkWSAutoReconnect(client, false)
	field := reflect.ValueOf(client).Elem().FieldByName("autoReconnect")
	if forceExported(field).Bool() {
		t.Fatal("setLarkWSAutoReconnect(false) did not update SDK private field")
	}

	// These calls exercise the go:linkname bindings that do not perform network
	// I/O on a fresh client. If the SDK private method names/signatures drift,
	// this package should fail at build/link time or here in tests.
	larkWSDisconnect(client, nil)
	if err := larkWSWriteMessage(client, 2, []byte("ping")); err == nil {
		t.Fatal("expected writeMessage on a disconnected client to fail")
	}
}
