package wol

import (
	"bytes"
	"net"
	"slices"
)

// ParsedPacket is the result of parsing a UDP payload as a Wake-on-LAN magic packet.
type ParsedPacket struct {
	MAC     net.HardwareAddr
	Content []byte
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
