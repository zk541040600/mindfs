package e2ee

import (
	"bytes"
	"testing"
)

func TestDeriveKeyForProtocolBindsTransportKeyToVersion(t *testing.T) {
	clientPriv, clientPub, err := GenerateECDHKeypair()
	if err != nil {
		t.Fatalf("GenerateECDHKeypair client: %v", err)
	}
	serverPriv, serverPub, err := GenerateECDHKeypair()
	if err != nil {
		t.Fatalf("GenerateECDHKeypair server: %v", err)
	}
	clientPeer, err := DecodePublicKey(serverPub)
	if err != nil {
		t.Fatalf("DecodePublicKey server: %v", err)
	}
	serverPeer, err := DecodePublicKey(clientPub)
	if err != nil {
		t.Fatalf("DecodePublicKey client: %v", err)
	}
	clientV1, err := DeriveKeyForProtocol("secret", "node", clientPub, serverPub, "client-nonce", "server-nonce", clientPriv, clientPeer, ProtocolVersionV1)
	if err != nil {
		t.Fatalf("DeriveKeyForProtocol client v1: %v", err)
	}
	serverV1, err := DeriveKeyForProtocol("secret", "node", clientPub, serverPub, "client-nonce", "server-nonce", serverPriv, serverPeer, ProtocolVersionV1)
	if err != nil {
		t.Fatalf("DeriveKeyForProtocol server v1: %v", err)
	}
	clientV2, err := DeriveKeyForProtocol("secret", "node", clientPub, serverPub, "client-nonce", "server-nonce", clientPriv, clientPeer, ProtocolVersionV2)
	if err != nil {
		t.Fatalf("DeriveKeyForProtocol client v2: %v", err)
	}
	serverV2, err := DeriveKeyForProtocol("secret", "node", clientPub, serverPub, "client-nonce", "server-nonce", serverPriv, serverPeer, ProtocolVersionV2)
	if err != nil {
		t.Fatalf("DeriveKeyForProtocol server v2: %v", err)
	}

	if !bytes.Equal(clientV1.Transport, serverV1.Transport) {
		t.Fatal("v1 peers derived different transport keys")
	}
	if !bytes.Equal(clientV2.Transport, serverV2.Transport) {
		t.Fatal("v2 peers derived different transport keys")
	}
	if bytes.Equal(clientV1.Transport, clientV2.Transport) {
		t.Fatal("v1 and v2 derived the same transport key")
	}
	if clientV2.ProtocolVersion != ProtocolVersionV2 {
		t.Fatalf("v2 protocol version = %d, want %d", clientV2.ProtocolVersion, ProtocolVersionV2)
	}
}

func TestDeriveKeyForProtocolRejectsUnsupportedVersion(t *testing.T) {
	if _, err := DeriveKeyForProtocol("secret", "node", "client", "server", "client-nonce", "server-nonce", nil, nil, 99); err == nil {
		t.Fatal("DeriveKeyForProtocol accepted an unsupported version")
	}
}

func TestEncryptJSONWithAADRejectsDifferentContext(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	envelope, err := EncryptJSONWithAAD(key, map[string]string{"status": "ok"}, []byte("request-proof-a"))
	if err != nil {
		t.Fatalf("EncryptJSONWithAAD: %v", err)
	}

	var payload map[string]string
	if err := DecryptJSONWithAAD(key, envelope, []byte("request-proof-a"), &payload); err != nil {
		t.Fatalf("DecryptJSONWithAAD with matching context: %v", err)
	}
	if payload["status"] != "ok" {
		t.Fatalf("payload = %#v, want status=ok", payload)
	}
	if err := DecryptJSONWithAAD(key, envelope, []byte("request-proof-b"), &payload); err == nil {
		t.Fatal("DecryptJSONWithAAD accepted a different context")
	}
	if err := DecryptJSON(key, envelope, &payload); err == nil {
		t.Fatal("DecryptJSON accepted ciphertext that requires authenticated context")
	}
}
