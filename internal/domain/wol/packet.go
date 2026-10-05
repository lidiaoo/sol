package wol

import (
	"bytes"
	"crypto/hmac"
	"net"
	"slices"
)

// ParsedPacket is the result of parsing a UDP payload as a Wake-on-LAN magic packet.
type ParsedPacket struct {
	MAC     net.HardwareAddr
	Content []byte
	// Authenticated reports that a valid trailing tag was stripped from the payload
	// (§19.14); it stays false for every packet when no packet key is configured.
	Authenticated bool
}

// PacketSignatureLen is the truncated HMAC-SHA256 tag length of an authenticated packet; the
// remote command channel uses the same truncation so one sender-side helper serves both.
const PacketSignatureLen = RemoteSignatureLen

// SignPacket returns the trailing authentication tag of a payload: a truncated HMAC-SHA256
// over every byte that precedes the tag.
func SignPacket(key []byte, payload []byte) []byte {
	return signatureTag(key, payload)
}

// SplitPacketSignature strips the trailing tag from payload and reports whether it verifies
// against key. A payload that is too short, or carries no valid tag, is returned unchanged so
// the caller can treat it as unauthenticated.
func SplitPacketSignature(key []byte, payload []byte) ([]byte, bool) {
	if len(key) == 0 || len(payload) <= PacketSignatureLen {
		return payload, false
	}

	data := payload[:len(payload)-PacketSignatureLen]
	tag := payload[len(payload)-PacketSignatureLen:]

	if !hmac.Equal(signatureTag(key, data), tag) {
		return payload, false
	}

	return data, true
}

// ParsePacket parses payload as a magic packet starting at offset 0.
// When secureOn is non-empty it must appear right after the 16 MAC repetitions.
func ParsePacket(payload []byte, secureOn []byte) (ParsedPacket, bool) {
	if len(payload) < PacketLenPlain {
		return ParsedPacket{}, false
	}

	for i := range HeaderSize {
		if payload[i] != MagicByte {
			return ParsedPacket{}, false
		}
	}

	mac := payload[HeaderSize : HeaderSize+MACSize]
	for rep := 1; rep < RepeatCount; rep++ {
		start := HeaderSize + rep*MACSize
		if !bytes.Equal(payload[start:start+MACSize], mac) {
			return ParsedPacket{}, false
		}
	}

	offset := PacketLenPlain

	if len(secureOn) > 0 {
		if len(payload) < PacketLenSecureOn || !bytes.Equal(payload[PacketLenPlain:PacketLenSecureOn], secureOn) {
			return ParsedPacket{}, false
		}

		offset = PacketLenSecureOn
	}

	parsed := ParsedPacket{
		MAC:     net.HardwareAddr(slices.Clone(mac)),
		Content: slices.Clone(payload[offset:]),
	}

	return parsed, true
}
