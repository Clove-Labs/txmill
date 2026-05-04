package keystore_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/clove-labs/txmill/internal/keystore"
)

func TestRoundtrip(t *testing.T) {
	dir := t.TempDir()
	ks, err := keystore.New(dir, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}

	addr, err := ks.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	addrs, err := ks.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(addrs) != 1 || addrs[0] != addr {
		t.Fatalf("List = %v, want [%s]", addrs, addr.Hex())
	}

	priv, err := ks.Load(addr)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if priv == nil {
		t.Fatal("Load returned nil key")
	}
}

func TestEmptyPasswordRejected(t *testing.T) {
	if _, err := keystore.New(t.TempDir(), ""); err == nil {
		t.Fatal("expected error for empty password")
	}
}

func TestWrongPassword(t *testing.T) {
	dir := t.TempDir()
	ks, err := keystore.New(dir, "right")
	if err != nil {
		t.Fatal(err)
	}
	addr, err := ks.Generate()
	if err != nil {
		t.Fatal(err)
	}

	other, err := keystore.New(dir, "wrong")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Load(addr); err == nil {
		t.Fatal("expected decrypt failure with wrong password")
	}
}

func TestTamperedCiphertext(t *testing.T) {
	dir := t.TempDir()
	ks, err := keystore.New(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	addr, err := ks.Generate()
	if err != nil {
		t.Fatal(err)
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no key file: %v", err)
	}

	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err := json.Unmarshal(doc["cipher"], &c); err != nil {
		t.Fatal(err)
	}
	ct, err := base64.StdEncoding.DecodeString(c["ciphertext"].(string))
	if err != nil {
		t.Fatal(err)
	}
	ct[0] ^= 0x01
	c["ciphertext"] = base64.StdEncoding.EncodeToString(ct)

	patched, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	doc["cipher"] = patched
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files[0], out, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ks.Load(addr); err == nil {
		t.Fatal("expected decrypt failure on tampered ciphertext")
	}
}
