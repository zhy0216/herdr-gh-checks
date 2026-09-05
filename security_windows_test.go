package main

import "testing"

func TestPathWithinDifferentWindowsVolumes(t *testing.T) {
	if pathWithin(`D:\repo`, `C:\Program Files\Git\cmd`) {
		t.Fatal("path on a different Windows volume was treated as inside the repository")
	}
}
