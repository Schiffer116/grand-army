# Everything decidable without reasoning lives in bash, not the agent

Berthier the agent should spend tokens only on judgment calls about managing Herdr sessions. Anything that can be decided concretely and automatically, such as observing Herdr, detecting a crashed Agent and Reviving it, is done by a bash Main Loop under systemd that costs no tokens and is the only writer of the Grand Army State. Within it, only `lib/tick.jq` writes the Grand Army State; bash gathers observations and carries out actions. Multi-step conventions (creating a Corps, spawning and finishing a Division) are scripts so every Agent performs them identically. Single-command actions (reading a pane, prompting an Agent) are not wrapped: Berthier, otherwise a normal agent, calls the `herdr` CLI directly and is trusted to use judgment per its `CLAUDE.md` rather than fenced by a permission list.

*Update: the bash Main Loop and `lib/tick.jq` described above are now Go (`internal/core`, `internal/app`); the decision stands. See [ADR 0004](0004-go-replaces-bash-and-jq.md).*

*Update: the clause "single-command actions are not wrapped" is superseded by [ADR 0006](0006-berthier-owns-the-host-and-harness-ports.md): reading a pane and prompting an Agent are `berthier` commands over the Host port, and Agents never call `herdr`.*
