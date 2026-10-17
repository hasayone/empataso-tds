package config

import (
	"crypto/sha1"
	"encoding/hex"
	"testing"
)

func TestOutboundPostbackSignature(t *testing.T) {
	a := OutboundPostback{Method: "post", URL: "https://pb.example/hook", Statuses: []string{"sale", "lead"}}
	b := OutboundPostback{Method: "POST", URL: "https://pb.example/hook", Statuses: []string{"lead", "sale"}}
	empty := OutboundPostback{URL: "https://pb.example/hook", Statuses: []string{"sale", "lead"}}
	get := OutboundPostback{Method: "GET", URL: "https://pb.example/hook", Statuses: []string{"lead", "sale"}}
	if a.Signature() != b.Signature() {
		t.Fatalf("case or order changed signature: %q vs %q", a.Signature(), b.Signature())
	}
	if empty.Signature() != get.Signature() {
		t.Fatalf("empty method %q, GET %q", empty.Signature(), get.Signature())
	}
	mixed := OutboundPostback{Method: "POST", URL: "https://pb.example/hook", Statuses: []string{"Sale", "Lead"}}
	if mixed.Signature() != a.Signature() {
		t.Fatalf("status case changed signature: %q vs %q", mixed.Signature(), a.Signature())
	}
	sum := sha1.Sum([]byte("POST\nhttps://pb.example/hook\nlead,sale"))
	want := "pb-" + hex.EncodeToString(sum[:])[:10]
	if PostbackID(a.Signature()) != want || PostbackID(b.Signature()) != want || PostbackID(mixed.Signature()) != want {
		t.Fatalf("id %q, want %q", PostbackID(mixed.Signature()), want)
	}
	if PostbackID(a.Signature()) != PostbackID(a.Signature()) {
		t.Fatal("PostbackID is not stable")
	}
}
