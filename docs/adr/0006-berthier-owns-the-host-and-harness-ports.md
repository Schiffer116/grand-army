# Berthier owns the Host and Harness ports; Herdr and Claude Code are adapters

Berthier had Herdr and Claude Code smeared through it: `herdr` argv built in `app` and `console`, Herdr's JSON shapes as `core` types, Claude's screen parsers and transcript format in `core`, launch flags beside the start code, and Agents told to call `herdr` directly ([ADR 0001](0001-deterministic-core-llm-at-the-edge.md)). Every Herdr or Claude Code change reached everywhere, the delivery logic (draft check, poll, re-Enter, re-send) was tested only through shell fakes, and nothing but the real products could say what either one promises.

So Berthier owns two ports, and the products behind them are plain adapters:

- **Host** (`internal/host`): workspaces (list, get, create, close, rename, worktree create and remove), panes (list, read the screen or the recent N lines, type text, press physical keys (escape, enter, ctrl-c, ctrl-u), rename, whether the process is alive or the pane is back at its shell), starting a process in a pane, and how that process learns its pane id (Herdr: `HERDR_PANE_ID`). Typed methods only; no raw argv passthrough. Tabs, layouts and plugins are Herdr's and stay out. The Host does not detect what an Agent is doing. Adapter: Herdr.
- **Harness** (`internal/claude`): start, resume and clear a session (launch flags, what keys mean: interrupt, submit, clear-draft and end-session map to physical Host keys, `--resume`, model, `/clear`), hook events to agent state, the screen classifiers (draft, prompt taken, usage-limit reset), where a transcript is and its Spend ([ADR 0005](0005-transcripts-are-the-spend-source.md)), and why a session ended. Claude Code is the only adapter for now; another harness is a new adapter that passes the same contract tests.

Each port has a Go fake (`hosttest`, `claudetest`) and one contract suite, run against the fake in the unit tests and against the real adapter in `tests/e2e.sh`. The doc comments on the interfaces are the written contract. Delivery logic (`PromptAgent`, `ClearAgent`) sits above the Host seam, written once and tested in Go against the fake.

`core` gets its own `Snapshot`, `Workspace` and `SnapAgent` types with no Herdr JSON tags; the Herdr adapter maps. `state.json` and the other `state/` files keep their format.

Agents never call `herdr`. What they need comes as `berthier` commands (`check-in`, `army`, `keys`, `rename`, with `prompt`, `ask` and `report`). Only Berthier's own Corps and its Divisions know which Host runs, and they run `herdr` only in named test sessions.

## Supersedes

[ADR 0001](0001-deterministic-core-llm-at-the-edge.md)'s clause "single-command actions (reading a pane, prompting an Agent) are not wrapped: Berthier calls the `herdr` CLI directly". They are wrapped now, in `berthier` commands over the Host port. The rest of 0001 stands.

## Consequences

- Agent state will come from Claude Code hooks (`berthier hook <event>`), not from the Host; Herdr's agent status stops being used. That, the move of the Claude-specific code, and the new commands land as separate steps after the ports.
- A Host that is not Herdr (tmux) is an adapter plus the contract suite, nothing else.
