# Security policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately through the repository's [security advisory form](https://github.com/itisbryan/herdr-gh-checks/security/advisories/new) rather than opening a public issue. Include the affected version, platform, reproduction steps, and whether the issue involves a GitHub PR, a local worktree, or an installer artifact.

Until a fix is available, avoid running the plugin on untrusted repositories or PRs, and prefer building from a reviewed source checkout.

## Trust model

GitHub PR metadata and workflow output are treated as hostile input. The plugin invokes `gh`, `git`, `nvim`, and `herdr` using the authenticated user's permissions. GitHub-changing operations are intentionally user-triggered; the plugin does not silently merge, approve, dispatch, or update a branch. The Neovim annotation helper is embedded in the binary and cached only in the per-user state directory; it is not loaded from a checked-out worktree.

The hardened command environment is scoped to `github.com`: inherited `GH_REPO`, `GH_HOST`, and custom Git/GitHub config routing variables are ignored, and repository remotes are checked before API operations. GitHub Enterprise remotes are therefore rejected by this plugin build.

For review fetches, repository-local Git hooks, URL rewrites, credential helpers, proxies, and custom upload-pack/SSH settings are deliberately disabled. This is a security boundary; configure authentication through the user's normal `gh` account or SSH agent instead of relying on repository-local transport configuration.
