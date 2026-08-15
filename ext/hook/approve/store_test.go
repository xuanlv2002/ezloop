package approve

import (
	"encoding/json"
	"testing"
)

func TestStoreFingerprintGranularity(t *testing.T) {
	s := NewStore(false)
	s.Approve("rm", json.RawMessage(`{"path":"a.txt"}`))

	if !s.IsApproved("rm", json.RawMessage(`{"path":"a.txt"}`)) {
		t.Fatal("same tool+args should hit")
	}
	if s.IsApproved("rm", json.RawMessage(`{"path":"b.txt"}`)) {
		t.Fatal("changed args must not hit")
	}
	if s.IsApproved("ls", json.RawMessage(`{"path":"a.txt"}`)) {
		t.Fatal("different tool must not hit")
	}
}

func TestStoreConsumeOnHit(t *testing.T) {
	s := NewStore(true)
	s.Approve("rm", json.RawMessage(`{}`))
	if !s.IsApproved("rm", json.RawMessage(`{}`)) {
		t.Fatal("first hit should pass")
	}
	if s.IsApproved("rm", json.RawMessage(`{}`)) {
		t.Fatal("consume-on-hit store must only pass once")
	}
}
