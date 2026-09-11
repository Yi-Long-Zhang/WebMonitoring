package identity

import (
	"crypto/rand"
	"encoding/hex"
)

func New() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("secure random source unavailable: " + err.Error())
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return hex.EncodeToString(value[0:4]) + "-" +
		hex.EncodeToString(value[4:6]) + "-" +
		hex.EncodeToString(value[6:8]) + "-" +
		hex.EncodeToString(value[8:10]) + "-" +
		hex.EncodeToString(value[10:16])
}
