# Development

The repository ships an `.envrc` that loads the flake's devShell via [`direnv`](https://direnv.net/). With direnv installed and hooked into your shell, `cd`ing into the repo puts `go`, `gopls`, `exiftool`, `file`, `ffmpeg`, and `sqlite` on `PATH` automatically — editors and terminals launched from that directory inherit the env with no per-tool configuration.

One-time setup:

```sh
nix profile add nixpkgs#direnv          # or: brew install direnv

# hook into zsh (or bash/fish equivalent)
echo 'eval "$(direnv hook zsh)"' >> ~/.zshrc
```

Then from the repo:

```sh
direnv allow
```

`cd`ing into the directory loads the devShell; `cd`ing out restores the previous environment. Flake evaluation can take a few seconds on first entry — installing [`nix-direnv`](https://github.com/nix-community/nix-direnv) on top (it caches the evaluation) makes subsequent loads near-instant:

```sh
nix profile add nixpkgs#nix-direnv
mkdir -p ~/.config/direnv
echo 'source $HOME/.nix-profile/share/nix-direnv/direnvrc' >> ~/.config/direnv/direnvrc
```

For VS Code, install the `mkhl.direnv` extension so the editor picks up the same environment — `gopls`, debuggers, and integrated terminals will resolve binaries through the flake instead of whatever happens to be on the system `PATH`.

Without direnv, the fallback is `nix develop` to drop into an interactive shell, or `nix develop --command <cmd>` to run a single command in the devShell env.
