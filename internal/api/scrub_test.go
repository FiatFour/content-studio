package api

import "testing"

func TestScrubPathsHidesDirectoriesButKeepsTheRestOfTheMessage(t *testing.T) {
	for in, want := range map[string]string{
		"render: /home/fiat/project/content-studio/var/storage/episodes/6/a1/shot.png: No such file": "render: …/shot.png: No such file",
		"open '/var/storage/x/y.wav' failed":                "open '…/y.wav' failed",
		"input=/tmp/a/b.mp4":                                "input=…/b.mp4",
		"dialogue and/or 1/2 of 14.1 characters per second": "dialogue and/or 1/2 of 14.1 characters per second",
		"บทพูดยาว 554 ตัวอักษร":                             "บทพูดยาว 554 ตัวอักษร",
	} {
		got := scrubPaths(&in)
		if *got != want {
			t.Errorf("scrubPaths(%q) = %q, want %q", in, *got, want)
		}
	}
	if scrubPaths(nil) != nil {
		t.Error("nil must stay nil")
	}
}
