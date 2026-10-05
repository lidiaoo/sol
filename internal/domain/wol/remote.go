package wol

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	ErrRemoteSegmentFormat  = errors.New("invalid remote command segment")
	ErrRemoteSegmentTooLong = errors.New("remote command segment too long")
	ErrRemoteSignature      = errors.New("remote command signature mismatch")
	ErrRemoteUnknownArg     = errors.New("unknown remote command argument")
	ErrRemoteMissingArg     = errors.New("missing remote command argument")
	ErrRemoteArgType        = errors.New("invalid remote command argument type")
	ErrRemoteArgValue       = errors.New("remote command argument rejected by its spec")
	ErrRawShellEmpty        = errors.New("raw shell command is empty")
	ErrRawShellTooLong      = errors.New("raw shell command is too long")
	ErrRawShellNotAllowed   = errors.New("raw shell command rejected by the allowlist")
)

const (
	// MaxRemoteSegment caps the size of a remote command segment ("id:k=v,k=v").
	MaxRemoteSegment = 128
	// RemoteSignatureLen is the truncated HMAC-SHA256 tag length (§21.4).
	RemoteSignatureLen = 8
	// maxRemoteIDLen caps a command id or argument name.
	maxRemoteIDLen = 32
	// MaxRawShellCommand caps a remote shell command (§21.6). It is larger than a command
	// segment because a shell line carries its own arguments, and it stays well inside the
	// listener's read buffer.
	MaxRawShellCommand = 512
)

// RawShellAction is the name the raw shell channel (§21.6) reports to the guards, the audit log
// and the counter map. It is not a registered action: the command arrives with the packet, so
// only the guards and the logging can treat it like one.
const RawShellAction Action = "raw:shell"

// ValidateRawShellCommand checks a remote shell command against the allowlist of §21.6. An empty
// allowlist accepts every command (that is what enabling a remote shell means); entries are
// full-match, so an entry such as `^/usr/local/bin/mark\.sh$` behaves exactly as written.
func ValidateRawShellCommand(allowlist []*regexp.Regexp, command string) error {
	if strings.TrimSpace(command) == "" {
		return ErrRawShellEmpty
	}

	if len(command) > MaxRawShellCommand {
		return fmt.Errorf("%w: %d bytes", ErrRawShellTooLong, len(command))
	}

	if len(allowlist) == 0 {
		return nil
	}

	for _, entry := range allowlist {
		if entry.MatchString(command) {
			return nil
		}
	}

	return fmt.Errorf("%w: %q", ErrRawShellNotAllowed, command)
}

// Remote argument types accepted in commands[].args.<name>.type.
const (
	ArgTypeString = "string"
	ArgTypeInt    = "int"
	ArgTypeBool   = "bool"
)

// ArgSpec constrains one remote command argument.
type ArgSpec struct {
	Type     string
	Enum     []string
	Pattern  *regexp.Regexp
	Required bool
}

// RemoteCommand is a whitelisted command that a remote sender may invoke (§21).
type RemoteCommand struct {
	ID   string
	Exec ExecParams
	Args map[string]ArgSpec
}

// RemoteAction returns the registry action name of a remote command id.
func RemoteAction(id string) Action {
	return Action("remote:" + id)
}

// Action returns the registry action name used for this remote command, so that
// cooldowns, audit logging and manual triggers treat it like any other action.
func (c RemoteCommand) Action() Action {
	return RemoteAction(c.ID)
}

// ParseRemoteSegment splits "<id>[:k=v,k=v]" into the command id and its arguments (§21.4).
func ParseRemoteSegment(segment []byte) (string, map[string]string, error) {
	if len(segment) == 0 {
		return "", nil, ErrRemoteSegmentFormat
	}

	if len(segment) > MaxRemoteSegment {
		return "", nil, fmt.Errorf("%w: %d bytes", ErrRemoteSegmentTooLong, len(segment))
	}

	id, argText, hasArgs := strings.Cut(string(segment), ":")

	if !ValidRemoteName(id) {
		return "", nil, fmt.Errorf("%w: %q", ErrRemoteSegmentFormat, id)
	}

	if !hasArgs || argText == "" {
		return id, nil, nil
	}

	args := make(map[string]string)

	for pair := range strings.SplitSeq(argText, ",") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || !ValidRemoteName(key) {
			return "", nil, fmt.Errorf("%w: %q", ErrRemoteSegmentFormat, pair)
		}

		if _, dup := args[key]; dup {
			return "", nil, fmt.Errorf("%w: duplicate %q", ErrRemoteSegmentFormat, key)
		}

		args[key] = value
	}

	return id, args, nil
}

// ValidateRemoteArgs checks the sent arguments against the command's declared specs.
func ValidateRemoteArgs(cmd RemoteCommand, args map[string]string) error {
	for key := range args {
		if _, declared := cmd.Args[key]; !declared {
			return fmt.Errorf("%w: %s", ErrRemoteUnknownArg, key)
		}
	}

	for name, spec := range cmd.Args {
		value, sent := args[name]

		if !sent {
			if spec.Required {
				return fmt.Errorf("%w: %s", ErrRemoteMissingArg, name)
			}

			continue
		}

		if err := spec.Validate(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}

	return nil
}

// Validate checks a value against this argument spec.
func (s ArgSpec) Validate(value string) error {
	if err := s.checkType(value); err != nil {
		return err
	}

	if len(s.Enum) > 0 && !slices.Contains(s.Enum, value) {
		return fmt.Errorf("%w: %q is not one of %v", ErrRemoteArgValue, value, s.Enum)
	}

	if s.Pattern != nil && !s.Pattern.MatchString(value) {
		return fmt.Errorf("%w: %q does not match %s", ErrRemoteArgValue, value, s.Pattern.String())
	}

	return nil
}

// checkType validates the declared argument type.
func (s ArgSpec) checkType(value string) error {
	switch s.Type {
	case ArgTypeString, "":
		return nil
	case ArgTypeInt:
		if _, err := strconv.Atoi(value); err != nil {
			return fmt.Errorf("%w: %q is not an int", ErrRemoteArgType, value)
		}
	case ArgTypeBool:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%w: %q is not a bool", ErrRemoteArgType, value)
		}
	default:
		return fmt.Errorf("%w: %s", ErrRemoteArgType, s.Type)
	}

	return nil
}

// RemoteSignature returns the truncated HMAC-SHA256 tag over prefix||segment.
func RemoteSignature(key []byte, prefix []byte, segment []byte) []byte {
	return signatureTag(key, prefix, segment)
}

// signatureTag is the truncated HMAC-SHA256 tag over the concatenation of parts. The remote
// command channel and authenticated packets share it, so the truncation cannot drift between
// the two.
func signatureTag(key []byte, parts ...[]byte) []byte {
	mac := hmac.New(sha256.New, key)
	for _, part := range parts {
		mac.Write(part)
	}

	return mac.Sum(nil)[:RemoteSignatureLen]
}

// RemoteStampSignature returns the [stamp][tag] a command segment carries when the channel
// bounds replays (§21.3). The tag covers prefix||segment||stamp, so the stamp is authenticated.
func RemoteStampSignature(key []byte, prefix []byte, segment []byte, at time.Time) []byte {
	stamp := TimestampBytes(at)

	return append(stamp, signatureTag(key, prefix, segment, stamp)...)
}

// SplitTimestampedRemoteContent is SplitRemoteContent for a stamped channel: it returns the
// segment, its stamp and its tag, so the caller can apply its own replay window before acting.
func SplitTimestampedRemoteContent(key []byte, prefix []byte, content []byte) ([]byte, int64, []byte, error) {
	if len(content) < TimestampLen+RemoteSignatureLen {
		return nil, 0, nil, ErrRemoteSignature
	}

	segment := content[:len(content)-TimestampLen-RemoteSignatureLen]
	raw := content[len(content)-TimestampLen-RemoteSignatureLen : len(content)-RemoteSignatureLen]
	tag := content[len(content)-RemoteSignatureLen:]

	if len(key) == 0 {
		return segment, DecodeTimestamp(raw), tag, nil
	}

	if !hmac.Equal(signatureTag(key, prefix, segment, raw), tag) {
		return nil, 0, nil, ErrRemoteSignature
	}

	return segment, DecodeTimestamp(raw), tag, nil
}

// SplitRemoteContent strips the trailing signature tag and, when a key is configured,
// verifies it against prefix||segment. An empty key means "unsigned" (the caller must
// only allow that for the authenticated HTTP transport).
func SplitRemoteContent(key []byte, prefix []byte, content []byte) ([]byte, error) {
	if len(content) < RemoteSignatureLen {
		return nil, ErrRemoteSignature
	}

	segment := content[:len(content)-RemoteSignatureLen]
	tag := content[len(content)-RemoteSignatureLen:]

	if len(key) == 0 {
		return segment, nil
	}

	if !hmac.Equal(RemoteSignature(key, prefix, segment), tag) {
		return nil, ErrRemoteSignature
	}

	return segment, nil
}

// remoteNameChars are the characters allowed in a command id or argument name.
const remoteNameChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-"

// ValidRemoteName accepts command ids and argument names: 1..32 of [A-Za-z0-9_.-].
func ValidRemoteName(value string) bool {
	if value == "" || len(value) > maxRemoteIDLen {
		return false
	}

	for _, r := range value {
		if !strings.ContainsRune(remoteNameChars, r) {
			return false
		}
	}

	return true
}
