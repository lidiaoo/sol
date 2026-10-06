package wol_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

func TestContentMatcherMatches(t *testing.T) {
	t.Parallel()

	t.Run("suffix", func(t *testing.T) {
		t.Parallel()

		matcher, err := wol.ContentMatcher{Kind: wol.ContentSuffix, Value: "reboot"}.Compile()
		require.NoError(t, err)
		require.True(t, matcher.Matches([]byte("magicreboot")))
		require.False(t, matcher.Matches([]byte("magicboot")))
		require.False(t, matcher.Matches(nil))
	})

	t.Run("none", func(t *testing.T) {
		t.Parallel()

		matcher, err := wol.ContentMatcher{Kind: wol.ContentNone}.Compile()
		require.NoError(t, err)
		require.True(t, matcher.Matches(nil))
		require.False(t, matcher.Matches([]byte("x")))
	})

	t.Run("any", func(t *testing.T) {
		t.Parallel()

		matcher, err := wol.ContentMatcher{Kind: wol.ContentAny}.Compile()
		require.NoError(t, err)
		require.True(t, matcher.Matches(nil))
		require.True(t, matcher.Matches([]byte("anything")))
	})

	t.Run("prefix hex with offset", func(t *testing.T) {
		t.Parallel()

		matcher, err := wol.ContentMatcher{Kind: wol.ContentPrefix, Hex: "0102", Offset: 1}.Compile()
		require.NoError(t, err)
		require.True(t, matcher.Matches([]byte{0x00, 0x01, 0x02}))
		require.False(t, matcher.Matches([]byte{0x01, 0x02}))
	})

	t.Run("default kind is none", func(t *testing.T) {
		t.Parallel()

		matcher, err := wol.ContentMatcher{}.Compile()
		require.NoError(t, err)
		require.True(t, matcher.Matches(nil))
	})
}

func TestContentMatcherErrors(t *testing.T) {
	t.Parallel()

	t.Run("both value and hex rejected", func(t *testing.T) {
		t.Parallel()

		_, err := wol.ContentMatcher{Kind: wol.ContentSuffix, Value: "a", Hex: "00"}.Compile()
		require.ErrorIs(t, err, wol.ErrContentValue)
	})

	t.Run("neither value nor hex rejected", func(t *testing.T) {
		t.Parallel()

		_, err := wol.ContentMatcher{Kind: wol.ContentPrefix}.Compile()
		require.ErrorIs(t, err, wol.ErrContentValue)
	})

	t.Run("too large rejected", func(t *testing.T) {
		t.Parallel()

		long := make([]byte, wol.MaxContentLen+1)
		for i := range long {
			long[i] = 'a'
		}

		_, err := wol.ContentMatcher{Kind: wol.ContentSuffix, Value: string(long)}.Compile()
		require.ErrorIs(t, err, wol.ErrContentTooLarge)
	})

	t.Run("unknown kind rejected", func(t *testing.T) {
		t.Parallel()

		_, err := wol.ContentMatcher{Kind: "regex", Value: "x"}.Compile()
		require.ErrorIs(t, err, wol.ErrUnknownContentKind)
	})
}
