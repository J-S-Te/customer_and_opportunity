package security

import "testing"

func TestSensitiveCodecEncryptDecryptAndTamperDetection(t *testing.T) {
	codec, err := NewSensitiveCodec([]byte("01234567890123456789012345678901"), []byte("abcdefghijklmnopqrstuvwxyz123456"))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := codec.Encrypt("secret@example.test")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := codec.Decrypt(ciphertext)
	if err != nil || plaintext != "secret@example.test" {
		t.Fatalf("roundtrip plaintext=%q err=%v", plaintext, err)
	}
	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 1
	if _, err = codec.Decrypt(tampered); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}
}
