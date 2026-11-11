package main

import (
	"fmt"
	"strings"
	"testing"
)

// snapshotStateDiff keeps byte-for-byte preservation checks actionable without
// dumping unchanged Git objects or an entire binary index. File values include
// their mode prefix, so offsets refer to captured state rather than file bytes.
func snapshotStateDiff(before, after map[string]string) string {
	keys := make(map[string]bool, len(before)+len(after))
	for key := range before {
		keys[key] = true
	}
	for key := range after {
		keys[key] = true
	}
	var diff strings.Builder
	for _, key := range sortedKeys(keys) {
		want, had := before[key]
		got, has := after[key]
		switch {
		case !had:
			fmt.Fprintf(&diff, "added %q (%d bytes): %q\n", key, len(got), got[:min(len(got), 64)])
		case !has:
			fmt.Fprintf(&diff, "removed %q (%d bytes): %q\n", key, len(want), want[:min(len(want), 64)])
		case want != got:
			offset := 0
			for offset < min(len(want), len(got)) && want[offset] == got[offset] {
				offset++
			}
			start := max(0, offset-16)
			fmt.Fprintf(&diff, "changed %q at state byte %d (excerpt from %d):\n  before (%d bytes): %q\n  after  (%d bytes): %q\n",
				key, offset, start,
				len(want), want[start:min(len(want), offset+48)],
				len(got), got[start:min(len(got), offset+48)])
		}
	}
	return diff.String()
}

func TestSnapshotStateDiffIdentifiesChangesWithoutDumpingUnchangedOrBinaryState(t *testing.T) {
	for _, test := range []struct {
		name          string
		before, after map[string]string
		want          string
	}{
		{
			name:   "identical state",
			before: map[string]string{"index-bytes": "\x00\xff", "empty": ""},
			after:  map[string]string{"empty": "", "index-bytes": "\x00\xff"},
		},
		{
			name:   "sorted additions removals and changes including empty values",
			before: map[string]string{"removed": "", "status": " M file\n", "unchanged": "do not dump"},
			after:  map[string]string{"added\nname": "", "status": " D file\n", "unchanged": "do not dump"},
			want: "added \"added\\nname\" (0 bytes): \"\"\n" +
				"removed \"removed\" (0 bytes): \"\"\n" +
				"changed \"status\" at state byte 1 (excerpt from 0):\n  before (8 bytes): \" M file\\n\"\n  after  (8 bytes): \" D file\\n\"\n",
		},
		{
			name:   "binary difference beyond initial preview",
			before: map[string]string{"index-bytes": strings.Repeat("x", 200) + "\x00" + strings.Repeat("y", 1000)},
			after:  map[string]string{"index-bytes": strings.Repeat("x", 200) + "\xff" + strings.Repeat("y", 1000)},
			want: "changed \"index-bytes\" at state byte 200 (excerpt from 184):\n" +
				"  before (1201 bytes): \"xxxxxxxxxxxxxxxx\\x00" + strings.Repeat("y", 47) + "\"\n" +
				"  after  (1201 bytes): \"xxxxxxxxxxxxxxxx\\xff" + strings.Repeat("y", 47) + "\"\n",
		},
		{
			name:   "truncated state",
			before: map[string]string{"refs": "abc"},
			after:  map[string]string{"refs": "ab"},
			want:   "changed \"refs\" at state byte 2 (excerpt from 0):\n  before (3 bytes): \"abc\"\n  after  (2 bytes): \"ab\"\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := snapshotStateDiff(test.before, test.after); got != test.want {
				t.Fatalf("state diff =\n%s\nwant:\n%s", got, test.want)
			}
		})
	}
}
