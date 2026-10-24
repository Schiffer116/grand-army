# Berthier

Berthier is the user's chief of staff for coding agents. It runs the Grand Army of Agents in a Host (Herdr today): it starts them on the user's command, keeps them alive, checks on them, and tells the user where things stand. The user talks to Agents directly; Berthier is never in that loop.

## Language

### Who

**Berthier**:
The chief-of-staff agent: whichever coding agent runs in Berthier's own repository, distinguished only by its goal of keeping the Grand Army moving and learning how the user works.
_Avoid_: orchestrator, controller, first mate

**Notes**:
A Corps's running record of open threads, commitments, and recent decisions, kept with its Board so any new session of its Main Agent (Berthier included) continues exactly where the last one stopped. How the user works is kept separately, in instructions.
_Avoid_: memory, journal, log

**Shutdown / Wake**:
Army-wide broadcasts by the user: Shutdown tells every Agent to park its step, push WIP, report and update Notes; Wake, after a restart, clears every Agent's context and tells it to rebuild from Notes, Board, last report, git and Brief, then resume. Frozen Agents are skipped.
_Avoid_: stop, pause, restart

**Quota guard**:
At 90% of the five-hour or seven-day window, each idle Agent is asked once per window for a short handoff in Notes (a quota broadcast); nothing new is started.

**Main Loop**:
The deterministic, always-running process that observes the Grand Army, Revives gone Agents, and Escalates everything else to Berthier.
_Avoid_: daemon, watcher, supervisor

**Host**:
The terminal multiplexer the Grand Army lives in: workspaces of panes, each pane running one process. Berthier reaches it only through its own Host port; Herdr is today's adapter, tmux could be another. Only Berthier's own Corps and its Divisions know which Host it is.
_Avoid_: herdr (outside the adapter), terminal, multiplexer

**Corps**:
One Host workspace, whatever it contains; it need not be a git repository. Same word singular and plural, as in Napoleon's Grande Armée.
_Avoid_: project, herdr session, campaign, repo, folder

**Division**:
A sub-unit of a Corps, with its own Agent; Berthier records which Corps it belongs to. The Host nests a spawned one under its Corps. It is either a worktree the Main Agent spawns for a side feature, which ends once the user has reviewed it, or an existing workspace the Corps adopted, which lasts. A spawned one runs on a model its Corps picks (`division-new --model`, default `BERTHIER_DIVISION_MODEL`, haiku), kept across Revives (a Corps's Main Agent runs on `BERTHIER_CORPS_MODEL`, opus); its Agent has no Artifact unless `division-new --artifact`. The Corps's Main Agent commands it: assigns and reviews its work, and receives its Escalations.
_Avoid_: task, sub-project, child, team

**Agent**:
One coding-agent session in a Host pane inside a Corps, whether the user or Berthier started it.
_Avoid_: worker, crewmate, bot

**Main Agent**:
The Agent working a Corps on its main branch, in the Corps's own workspace; it decides when to spawn Divisions.
_Avoid_: lead, primary

**Grand Army**:
Every Agent currently known to Berthier, after Napoleon's Grande Armée, whose chief of staff was Berthier.
_Avoid_: Army, fleet, crew, swarm

**Grand Army State**:
The Main Loop's latest record of every Corps and Agent; only the Main Loop writes it.
_Avoid_: status file, snapshot

### Work

**Brief**:
The initial instructions handed to a new Agent (by Berthier to a Main Agent, or by a Main Agent to a Division's Agent): the goal and what done looks like.
_Avoid_: orders, prompt, task description

**Board**:
The Skipli board a Corps works from (Development environment for now): its own, or its card on the Grand Army Board. The user adds work there; the Corps shows its plan and progress there. Only the Corps's Main Agent changes its work items; its Divisions report to the Main Agent as usual. A work item is done when all its tasks are; only the user deletes finished work.
_Avoid_: backlog, kanban, todo list

**Grand Army Board**:
The one Board shared by every Corps without its own: one card per Corps, named after it, whose tasks are that Corps's work. Berthier owns its cards; a Corps moves to its own Board only when the user decides.
_Avoid_: army board, master board

### Supervision

**Revive**:
Restarting a crashed or usage-limited Agent by resuming its previous session; the only fix the Main Loop makes on its own, and only while the session's cache is still warm or the session is small. An Agent the user exited on purpose is never Revived.
_Avoid_: restart, respawn

**Frozen**:
An Agent that needs a Revive but whose cache has gone cold, so resuming would pay to rewrite its whole context. It waits, its messages held, until the user chooses: Revive anyway, or a Fresh Start. Only the user decides, except a Division with a kept Brief and a clean worktree, which gets a Fresh Start by itself.
_Avoid_: cold, stuck, dead

**Fresh Start**:
Replacing a Frozen Agent's session with a new one in the same pane that rebuilds from what is written down: its Corps's notes, Board, last report and git (a Division also gets its Brief again). Nothing is cleared without the user's say.
_Avoid_: reset, clear, respawn

**Escalation**:
The Main Loop handing Berthier an Agent problem it does not fix itself, such as a blocked, hung, or stalled Agent, or a failed Revive.
_Avoid_: alert, notification

**Check-in**:
Berthier reading an Agent's recent terminal output to judge whether it is progressing, stuck, or done.
_Avoid_: status report, ping

**Nudge**:
A prompt Berthier sends to a live Agent to get it moving again.
_Avoid_: ping, poke

**Budget**:
The share of the account's weekly (seven-day) quota a Corps may use, set with `berthier budget`. Soft: at 80% the Corps's Main Agent and the user are told once per week; at 100% the Main Loop holds that Corps's and its Divisions' Revives, usage-limit resumes and ask delivery, and nothing else. Each rise in the account's weekly usage is split by the Spend of the turns since the last rise; a Division counts toward its Corps, Berthier's own Spend is Command overhead, Spend nobody's turns explain counts toward "other".
_Avoid_: quota (that is the account's), limit (that is Claude's usage limit)

**Spend**:
What an Agent's turns consumed: tokens by kind (input, cache read, cache write, output) and model, priced into API-equivalent dollars; the account's quota share is derived from it.
_Avoid_: cost (ambiguous with Claude Code's session cost), usage (that is the quota's)

**Profile**:
Spend broken down Corps → Division → session → turn, by model, token kind, cache hit rate and what started each turn; one view read by the user and by optimizing Agents.
_Avoid_: report (that is an Agent's status line), stats

**Waste signal**:
A pattern in the Profile that spent tokens without delivering: context bloat, cache misses, oversized tool results, repeated reads, a stronger model than the work needed.
_Avoid_: inefficiency, smell

**Delivered unit**:
A commit landed on a Corps's main branch, or a finished Division; Spend per delivered unit is the headline efficiency figure.
_Avoid_: output, result

**Turn origin**:
What started a turn: the user, or Berthier (loop event, Ask, notify, Brief, restart continue, usage-limit resume, shutdown or wake broadcast).
_Avoid_: trigger, source

**Command overhead**:
Berthier's own Spend running the Army, shown apart from the Corps it works in.
_Avoid_: Berthier usage, admin cost

### Coordination

**Owner**:
The Corps responsible for an area (the bar, systemd units, an app); listed in `owners.md`. Only the Owner's Agents change it.
_Avoid_: maintainer

**Ask**:
A request one Agent sends another Corps's Agent through Berthier's inbox, delivered when the addressee is ready for input; answered with an Ask back.
_Avoid_: message, ticket, relay

**Report**:
An Agent's own one-line status for the user, written at the end of every turn; "needs you" marks the ones waiting on the user.
_Avoid_: summary, recap

**Demo**:
Where an Agent has a demo or preview of its work hosted, reported with `berthier demo <url> [label]` by each Main Agent and Division for itself; the Console's info panel lists it.
_Avoid_: preview link, deployment
