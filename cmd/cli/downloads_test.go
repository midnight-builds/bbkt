package cli

import "testing"

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{
		0:         "0 B",
		1023:      "1023 B",
		1024:      "1.0 KiB",
		15178474:  "14.5 MiB",
		154416927: "147.3 MiB",
		1 << 30:   "1.0 GiB",
	}
	for in, want := range cases {
		if got := formatBytes(in); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
