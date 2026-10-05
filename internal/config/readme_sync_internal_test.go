package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// The published READMEs are claims about the implementation: the smoke scripts extract their yaml
// and bash blocks and run them against the real binary. README.zh-CN.md is a translation of the
// same document, so its code blocks have to stay byte-identical - a key fixed in one file and
// forgotten in the other leaves a reader with an example the loader refuses.
const (
	readmeEnglish = "../../README.md"
	readmeChinese = "../../README.zh-CN.md"
)

// fencedBlock matches a fenced code block at the start of a line and captures its language tag
// and body, so indented fences inside a list do not count.
var fencedBlock = regexp.MustCompile("(?ms)^```([^\\n]*)\\n(.*?)^```$")

// readmeBlocks returns the language tag and body of every fenced code block in path.
func readmeBlocks(t *testing.T, path string) [][2]string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(path))
	require.NoError(t, err)

	matches := fencedBlock.FindAllStringSubmatch(string(raw), -1)
	require.NotEmpty(t, matches, "%s has no code blocks", path)

	blocks := make([][2]string, 0, len(matches))

	for _, match := range matches {
		blocks = append(blocks, [2]string{match[1], match[2]})
	}

	return blocks
}

// TestReadmeTranslationsAgree fails when the Chinese README drifts from the English one. It
// compares the code blocks only: the prose is a translation, the snippets are not.
func TestReadmeTranslationsAgree(t *testing.T) {
	t.Parallel()

	english := readmeBlocks(t, readmeEnglish)
	chinese := readmeBlocks(t, readmeChinese)

	require.Len(t, chinese, len(english), "the two READMEs have a different number of code blocks")

	for i := range english {
		require.Equal(t, english[i][0], chinese[i][0], "block %d carries a different language tag", i+1)
		require.Equal(t, english[i][1], chinese[i][1],
			"block %d differs between README.md and README.zh-CN.md", i+1)
	}
}
