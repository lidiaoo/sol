package wol

import (
	"bytes"
	"crypto/hmac"
	"encoding/binary"
	"math"
	"net"
	"slices"
	"time"
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

// TimestampLen is the big-endian unix-seconds stamp a replay-protected packet carries in front
// of its tag (§19.16).
const TimestampLen = 8

// SignTimestampedPacket returns the trailing [stamp][tag] of a replay-protected payload: the
// stamp covers the packet's second, and the tag is a truncated HMAC-SHA256 over every byte that
// precedes the tag, stamp included. A receiver with a window accepts only a fresh stamp and only
// once per tag, so a captured packet cannot be replayed.
func SignTimestampedPacket(key []byte, payload []byte, at time.Time) []byte {
	stamp := TimestampBytes(at)

	return append(stamp, signatureTag(key, payload, stamp)...)
}

// SplitTimestampedSignature strips the trailing stamp and tag from a replay-protected payload.
// It reports the bytes before the stamp, the stamp itself and whether the tag verifies; a
// payload that is too short, or whose tag does not verify, comes back as ok=false.
func SplitTimestampedSignature(key []byte, payload []byte) ([]byte, int64, []byte, bool) {
	if len(key) == 0 || len(payload) <= TimestampLen+PacketSignatureLen {
		return payload, 0, nil, false
	}

	data := payload[:len(payload)-TimestampLen-PacketSignatureLen]
	raw := payload[len(payload)-TimestampLen-PacketSignatureLen : len(payload)-PacketSignatureLen]
	tag := payload[len(payload)-PacketSignatureLen:]

	if !hmac.Equal(signatureTag(key, data, raw), tag) {
		return payload, 0, nil, false
	}

	return data, DecodeTimestamp(raw), tag, true
}

// DecodeTimestamp reads a stamp. One that does not fit a signed second is nonsense and comes
// back as zero, which no window accepts.
func DecodeTimestamp(raw []byte) int64 {
	if len(raw) < TimestampLen {
		return 0
	}

	if value := binary.BigEndian.Uint64(raw[:TimestampLen]); value <= math.MaxInt64 {
		return int64(value)
	}

	return 0
}

// TimestampBytes renders a time as the 8 big-endian bytes of its unix second. A time before 1970
// cannot be represented and stays zero, which no window accepts.
func TimestampBytes(at time.Time) []byte {
	stamp := make([]byte, TimestampLen)

	if unix := at.Unix(); unix > 0 {
		binary.BigEndian.PutUint64(stamp, uint64(unix))
	}

	return stamp
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
