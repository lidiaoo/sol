package wol

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	PortEcho    = 7
	PortDefault = 9

	BufferSize  = 2048
	HeaderSize  = 6
	RepeatCount = 16
	MACSize     = 6
	MagicByte   = 0xFF

	SecureOnSize      = 6
	PacketLenPlain    = HeaderSize + RepeatCount*MACSize
	PacketLenSecureOn = PacketLenPlain + SecureOnSize

	// SendDefaultInterval is the gap a wol.send action leaves between two packets.
	SendDefaultInterval = 100 * time.Millisecond
	// SendMaxRepeat bounds the copies one wol.send action may send.
	SendMaxRepeat = 10
	// SendMaxInterval bounds the gap a wol.send action may configure.
	SendMaxInterval = 10 * time.Second
)

// ErrMACLength reports a magic packet asked to encode something that is not a 6-byte MAC.
var ErrMACLength = errors.New("mac must be exactly 6 bytes")

// DefaultReservedPorts returns the reserved WOL ports ({7, 9}).
func DefaultReservedPorts() []int {
	return []int{PortEcho, PortDefault}
}

func BuildMagicPacket(mac []byte) []byte {
	pkt := make([]byte, HeaderSize+RepeatCount*MACSize)

	for i := range HeaderSize {
		pkt[i] = MagicByte
	}

	offset := HeaderSize
	for range RepeatCount {
		copy(pkt[offset:offset+MACSize], mac)
		offset += MACSize
	}

	return pkt
}

// EncodeMagicPacket encodes the datagram a wol.send action transmits: the magic packet followed
// by the SecureOn password when one is configured (§19.13). It is the inverse of ParsePacket,
// and the two are tested against each other so a sender and a receiver cannot drift apart.
func EncodeMagicPacket(mac []byte, secureOn []byte) ([]byte, error) {
	if len(mac) != MACSize {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", ErrMACLength, len(mac), MACSize)
	}

	if len(secureOn) != 0 && len(secureOn) != SecureOnSize {
		return nil, fmt.Errorf("%w: got %d bytes", ErrSecureOnLength, len(secureOn))
	}

	packet := make([]byte, 0, PacketLenPlain+len(secureOn))
	packet = append(packet, BuildMagicPacket(slices.Clone(mac))...)
	packet = append(packet, secureOn...)

	return packet, nil
}

func ContainsMagicPacket(payload []byte, expected []byte) bool {
	if len(payload) < len(expected) {
		return false
	}

	return bytes.Contains(payload, expected)
}

func ValidateMagicPacket(payload []byte, mac []byte) bool {
	if len(payload) < HeaderSize+RepeatCount*MACSize {
		return false
	}

	for i := range HeaderSize {
		if payload[i] != MagicByte {
			return false
		}
	}

	offset := HeaderSize
	for range RepeatCount {
		if !bytes.Equal(payload[offset:offset+MACSize], mac) {
			return false
		}

		offset += MACSize
	}

	return true
}
