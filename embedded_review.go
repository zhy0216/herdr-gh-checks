package main

import _ "embed"

// Keep the review helper tied to the reviewed plugin binary. Loading a
// same-named script from the worktree or an adjacent writable directory would
// let a hostile checkout replace the commands executed by Neovim.
//
//go:embed review.vim
var embeddedReviewVim []byte
