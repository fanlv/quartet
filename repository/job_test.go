package repository

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/fanlv/quartet/types/model"
)

func TestPersistedJobRoundTripKeepsPrivateMessageReceipts(t *testing.T) {
	original := &model.Job{
		ID:                    "job-1",
		ActiveClientMessageID: "client-1",
		ClientMessageReceipts: map[string]model.ClientMessageReceipt{
			"client-1": {
				State:       model.ClientMessageStateProcessing,
				PayloadHash: "hash-1",
				AcceptedAt:  123,
			},
		},
		CommandReceipts: map[string]model.CommandReceipt{
			"command-1": {
				PayloadHash: "command-hash-1",
				Event: &model.CommandSystemMessageEvent{
					Command: "/new",
					Text:    "created",
					Action:  &model.CommandAction{Type: "new_job", WorkspaceID: "ws-1"},
				},
			},
		},
		CreationClientMessageID: "creation-1",
		CreationPayloadHash:     "creation-hash-1",
	}

	data, err := marshalPersistedJob(original)
	if err != nil {
		t.Fatalf("marshalPersistedJob: %v", err)
	}
	if !bytes.Contains(data, []byte(`"clientMessageReceipts"`)) {
		t.Fatalf("persisted JSON omitted receipts: %s", data)
	}
	loaded, err := unmarshalPersistedJob(data)
	if err != nil {
		t.Fatalf("unmarshalPersistedJob: %v", err)
	}
	if loaded.ActiveClientMessageID != original.ActiveClientMessageID {
		t.Fatalf("active clientMessageId = %q, want %q", loaded.ActiveClientMessageID, original.ActiveClientMessageID)
	}
	if loaded.CreationClientMessageID != original.CreationClientMessageID || loaded.CreationPayloadHash != original.CreationPayloadHash {
		t.Fatalf("creation receipt = (%q,%q), want (%q,%q)", loaded.CreationClientMessageID, loaded.CreationPayloadHash, original.CreationClientMessageID, original.CreationPayloadHash)
	}
	if got := loaded.ClientMessageReceipts["client-1"]; got != original.ClientMessageReceipts["client-1"] {
		t.Fatalf("receipt = %#v, want %#v", got, original.ClientMessageReceipts["client-1"])
	}
	if got := loaded.CommandReceipts["command-1"]; got.Event == nil || got.Event.Command != "/new" || got.Event.Action == nil || got.Event.Action.WorkspaceID != "ws-1" {
		t.Fatalf("command receipt did not round-trip: %#v", got)
	}
	if loaded.CreationClientMessageID != "creation-1" || loaded.CreationPayloadHash != "creation-hash-1" {
		t.Fatalf("creation receipt did not round-trip: id=%q hash=%q", loaded.CreationClientMessageID, loaded.CreationPayloadHash)
	}
}

func TestJobPublicJSONOmitsMessageReceipts(t *testing.T) {
	job := &model.Job{
		ID:                      "job-1",
		ActiveClientMessageID:   "client-secret",
		CreationClientMessageID: "creation-secret",
		CreationPayloadHash:     "creation-hash-secret",
		ClientMessageReceipts: map[string]model.ClientMessageReceipt{
			"client-secret": {PayloadHash: "hash-secret"},
		},
		CommandReceipts: map[string]model.CommandReceipt{
			"command-secret": {PayloadHash: "command-hash-secret"},
		},
	}
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	for _, privateValue := range [][]byte{[]byte("clientMessageReceipts"), []byte("commandReceipts"), []byte("activeClientMessageId"), []byte("creationClientMessageId"), []byte("creationPayloadHash"), []byte("client-secret"), []byte("hash-secret"), []byte("command-secret"), []byte("creation-secret")} {
		if bytes.Contains(data, privateValue) {
			t.Fatalf("public Job JSON leaked private receipt data %q: %s", privateValue, data)
		}
	}
}
