package mlat

import (
	"encoding/hex"
	"github.com/libp2p/go-libp2p/core/peer"
)


func PeerIDToPublicKey(peerIDStr string) string {
	pid, err := peer.Decode(peerIDStr)
	if err != nil {
		return ""
	}
	pubKey, err := pid.ExtractPublicKey()
	if err != nil || pubKey == nil {
		return ""
	}
	raw, err := pubKey.Raw()
	if err != nil {
		return ""
	}
	return hex.EncodeToString(raw)
}
