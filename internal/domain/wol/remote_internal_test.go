package wol

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func remoteTestCommand() RemoteCommand {
	return RemoteCommand{
		ID:   "backup",
		Exec: ExecParams{Command: []string{"/usr/local/bin/backup.sh", "--target={{.Arg.target}}"}},
		Args: map[string]ArgSpec{
			"target":  {Type: ArgTypeString, Enum: []string{"home", "work"}, Required: true},
			"retries": {Type: ArgTypeInt},
			"deep":    {Type: ArgTypeBool},
			"tag":     {Type: ArgTypeString, Pattern: regexp.MustCompile(`^v[0-9]+$`)},
		},
	}
}

func remoteTestPrefix() []byte {
	prefix := bytes.Repeat([]byte{0xFF}, HeaderSize)

	return append(prefix, bytes.Repeat([]byte{0x58, 0x11, 0x22, 0xBC, 0x78, 0x66}, RepeatCount)...)
}

func TestRemoteCommandActionName(t *testing.T) {
	t.Parallel()

	require.Equal(t, Action("remote:backup"), remoteTestCommand().Action())
}

func TestParseRemoteSegment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		id   string
		args map[string]string
		err  error
	}{
		{name: "id only", in: "lock", id: "lock"},
		{name: "id and args", in: "backup:target=home", id: "backup", args: map[string]string{"target": "home"}},
		{
			name: "two args", in: "backup:target=home,retries=3", id: "backup",
			args: map[string]string{"target": "home", "retries": "3"},
		},
		{name: "trailing colon", in: "lock:", id: "lock"},
		{name: "empty segment", in: "", err: ErrRemoteSegmentFormat},
		{name: "empty id", in: ":target=home", err: ErrRemoteSegmentFormat},
		{name: "space in id", in: "lock cd", err: ErrRemoteSegmentFormat},
		{name: "long id", in: strings.Repeat("a", maxRemoteIDLen+1), err: ErrRemoteSegmentFormat},
		{name: "missing equals", in: "backup:target", err: ErrRemoteSegmentFormat},
		{name: "duplicate arg", in: "backup:target=home,target=work", err: ErrRemoteSegmentFormat},
		{name: "too long", in: strings.Repeat("a", MaxRemoteSegment+1), err: ErrRemoteSegmentTooLong},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			id, args, err := ParseRemoteSegment([]byte(tc.in))

			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.id, id)
			require.Equal(t, tc.args, args)
		})
	}
}

func TestValidateRemoteArgs(t *testing.T) {
	t.Parallel()

	cmd := remoteTestCommand()

	require.NoError(t, ValidateRemoteArgs(cmd, map[string]string{"target": "home"}))
	require.NoError(t, ValidateRemoteArgs(cmd, map[string]string{"target": "work", "retries": "3", "deep": "true"}))
	require.NoError(t, ValidateRemoteArgs(cmd, map[string]string{"target": "home", "tag": "v12"}))

	require.ErrorIs(t, ValidateRemoteArgs(cmd, map[string]string{"target": "cloud"}), ErrRemoteArgValue)
	require.ErrorIs(t, ValidateRemoteArgs(cmd, map[string]string{}), ErrRemoteMissingArg)
	require.ErrorIs(t, ValidateRemoteArgs(cmd, map[string]string{"target": "home", "extra": "1"}), ErrRemoteUnknownArg)
	require.ErrorIs(t, ValidateRemoteArgs(cmd, map[string]string{"target": "home", "retries": "many"}), ErrRemoteArgType)
	require.ErrorIs(t, ValidateRemoteArgs(cmd, map[string]string{"target": "home", "deep": "maybe"}), ErrRemoteArgType)
	require.ErrorIs(t, ValidateRemoteArgs(cmd, map[string]string{"target": "home", "tag": "1.2"}), ErrRemoteArgValue)
}

func TestRemoteSignatureRoundTrip(t *testing.T) {
	t.Parallel()

	key := []byte("shared-secret")
	prefix := remoteTestPrefix()
	segment := []byte("backup:target=home")
	tag := RemoteSignature(key, prefix, segment)

	require.Len(t, tag, RemoteSignatureLen)

	signed := slices.Concat(segment, tag)

	got, err := SplitRemoteContent(key, prefix, signed)
	require.NoError(t, err)
	require.Equal(t, segment, got)

	tampered := slices.Clone(signed)
	tampered[0] = 'x'

	_, err = SplitRemoteContent(key, prefix, tampered)
	require.ErrorIs(t, err, ErrRemoteSignature)

	_, err = SplitRemoteContent([]byte("other-key"), prefix, signed)
	require.ErrorIs(t, err, ErrRemoteSignature)

	otherPrefix := remoteTestPrefix()
	otherPrefix[HeaderSize] ^= 0x01

	_, err = SplitRemoteContent(key, otherPrefix, signed)
	require.ErrorIs(t, err, ErrRemoteSignature)
}

func TestSplitRemoteContentUnsigned(t *testing.T) {
	t.Parallel()

	prefix := remoteTestPrefix()
	content := slices.Concat([]byte("lock"), make([]byte, RemoteSignatureLen))

	segment, err := SplitRemoteContent(nil, prefix, content)
	require.NoError(t, err)
	require.Equal(t, []byte("lock"), segment)

	_, err = SplitRemoteContent(nil, prefix, []byte("lock"))
	require.ErrorIs(t, err, ErrRemoteSignature)
}
