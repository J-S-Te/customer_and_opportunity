package machineauth

import (
	"crypto/sha256"
	"encoding/hex"
)

func hashReplay(tenantID, oauthClientID, subject, nonce string) string {
	sum := sha256.Sum256([]byte(tenantID + "\x00" + oauthClientID + "\x00" + subject + "\x00" + nonce))
	return hex.EncodeToString(sum[:])
}
