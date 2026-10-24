#!/usr/bin/env bash
# End-to-end test against an isolated Herdr session with real Claude agents
# (haiku, a few cents). Never touches the user's default Herdr session.
#   tests/e2e.sh
set -euo pipefail
cd "$(dirname "$0")/.."
go build -o bin/berthier ./cmd/berthier

session=berthier-e2e-$$
work=$(mktemp -d)
repo=$work/berthier-e2e-$$
export BERTHIER_HERDR_SESSION=$session BERTHIER_HOME=$work/home BERTHIER_START_TIMEOUT_MS=60000
# The Profile reads only this run's transcripts (Claude names a project
# directory after its cwd, with / and . as -).
export BERTHIER_PROFILE_SINCE=$(date +%s) BERTHIER_PROFILE_PROJECTS="$(sed 's|[/.]|-|g' <<<"$work")*"
export BERTHIER_RESTART_GAP=${BERTHIER_RESTART_GAP:-4}
export BERTHIER_RESTART_SETTLE=${BERTHIER_RESTART_SETTLE:-3}
# Berthier's state comes from Claude Code's hooks (ADR 0006): every Claude of
# this test registers them, as ~/.claude/settings.json does for the user.
mkdir -p "$work"
hooks=$(jq -cn --arg b "$PWD/bin/berthier" '
  def run($e): {type: "command", command: ($b + " hook " + $e)};
  {hooks: {
    SessionStart: [{hooks: [run("SessionStart")]}],
    UserPromptSubmit: [{hooks: [run("UserPromptSubmit")]}],
    Stop: [{hooks: [run("Stop")]}],
    SessionEnd: [{hooks: [run("SessionEnd")]}],
    Notification: [{matcher: "permission_prompt", hooks: [run("Notification")]}],
    PreToolUse: [{matcher: "AskUserQuestion", hooks: [run("PreToolUse")]}],
    PostToolUse: [{hooks: [run("PostToolUse")]}]}}')
settings=$work/hooks.json
jq -n --argjson h "$hooks" '$h' >"$settings"
export BERTHIER_CLAUDE_SETTINGS=$settings
clean_env=(env -u HERDR_ENV -u HERDR_SOCKET_PATH -u HERDR_PANE_ID -u HERDR_TAB_ID -u HERDR_WORKSPACE_ID)
h() { "${clean_env[@]}" herdr --session "$session" "$@"; }
step() { printf '\n== %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*"; exit 1; }
# How many loop.log events match a jq condition, e.g. logged '.kind == "revived"'.
logged() { jq -c --arg p "${2:-}" "select($1)" "${3:-$BERTHIER_HOME/loop.log}" | wc -l; }
# The session a pane's Claude last announced through its hooks (Herdr no longer tells).
agent_sid() { jq -rs --arg p "$1" '[.[] | select(.pane == $p)] | max_by(.seq) | .session // empty' "$BERTHIER_HOME"/agents/*.json 2>/dev/null; }
claude_pid() { h pane process-info --pane "$1" | jq -r '.result.process_info.foreground_processes[] | select(.name == "claude") | .pid'; }

cleanup() {
  h session stop "$session" >/dev/null 2>&1 || "${clean_env[@]}" herdr session stop "$session" >/dev/null 2>&1 || true
  "${clean_env[@]}" herdr session delete "$session" >/dev/null 2>&1 || true
  tmux kill-session -t "$session" 2>/dev/null || true
  rm -rf "$work" "$HOME/.herdr/worktrees/${repo##*/}"
}
trap cleanup EXIT

attach() {
  # Agents inherit BERTHIER_HOME from Herdr, so their berthier commands use the test state.
  tmux new-session -d -s "$session" -x 200 -y 50 "${clean_env[*]} BERTHIER_HOME=$BERTHIER_HOME herdr session attach $session"
  for _ in $(seq 50); do h workspace list >/dev/null 2>&1 && break; sleep 0.2; done
}

step "start isolated Herdr session"
attach

step "the Host and Harness contracts, against this Herdr and a real Claude"
BERTHIER_CONTRACT_HERDR_SESSION=$session "${clean_env[@]}" go test -count=1 -run 'TestHostContract|TestLiveClaude' ./internal/herdr ./internal/claude

mkdir -p "$repo" && git -C "$repo" init -q && git -C "$repo" commit -q --allow-empty -m init
# Herdr relaunches Claude after a restart without the flags it was started with:
# the repo registers the hooks itself, for those sessions.
mkdir -p "$repo/.claude" && cp "$settings" "$repo/.claude/settings.local.json"
pane=$(h workspace create --cwd "$repo" --label demo --no-focus | jq -re '.result.root_pane.pane_id')
ws=$(h pane get "$pane" | jq -re '.result.pane.workspace_id')

step "start a Marshal with one turn (so its session can be resumed)"
# The scratch dir is new to Claude: accept its folder trust dialog for this test.
h agent start demo-main --kind claude --pane "$pane" --timeout 60000 -- --model haiku --settings "$settings" >/dev/null 2>&1 || {
  h agent send-keys demo-main down enter >/dev/null
  h agent wait demo-main --until idle --timeout 60000 >/dev/null
}
# Right after the trust dialog Claude can report idle before it accepts input.
for try in 1 2 3; do
  h agent prompt demo-main "Reply with just: pong" --wait --timeout 90000 >/dev/null 2>&1 && break
  ((try < 3)) || fail "agent never took its first prompt"
  sleep 3
done
bin/berthier tick
sid=$(jq -r --arg p "$pane" '.agents[$p].session_id' "$BERTHIER_HOME/state.json")
[[ $sid =~ ^[0-9a-f-]{36}$ ]] || fail "no session id recorded (got $sid)"

step "crash the agent; the Main Loop must Revive it in place"
kill -KILL "$(claude_pid "$pane")"
sleep 3
bin/berthier tick
for _ in $(seq 30); do
  [[ $(agent_sid "$pane") == "$sid" ]] && claude_pid "$pane" | grep -q . && break
  sleep 1
done
[[ $(agent_sid "$pane") == "$sid" ]] || fail "agent not revived with session $sid"
(($(logged '.kind == "revived" and .pane == $p and .name == "demo-main"' "$pane"))) || fail "revive not logged"
bin/berthier tick  # the loop's next pass sees the revived agent live

step "the agent commands: army <pane>, check-in, keys, rename"
bin/berthier army "$pane" | jq -e '.state == "idle" and .corps == "demo" and .name == "demo-main" and .process == "alive"' >/dev/null \
  || fail "army <pane>: $(bin/berthier army "$pane")"
bin/berthier check-in "$pane" | grep -q '❯' || fail "check-in shows no Claude box: $(bin/berthier check-in "$pane")"
(($(bin/berthier check-in "$pane" --recent 30 | wc -l) > 1)) || fail "check-in --recent"
bin/berthier keys "$pane" interrupt
bin/berthier rename "$pane" demo-main
h agent get demo-main >/dev/null 2>&1 || fail "rename lost the name"

step "a crashed session that never saved a transcript is forgotten, not Revived"
# `claude --resume` of it prints "No conversation found" and returns to the shell.
bare=$(h workspace create --cwd "$repo" --label bare --no-focus | jq -re '.result.root_pane.pane_id')
h agent start bare --kind claude --pane "$bare" --timeout 60000 -- --model haiku --settings "$settings" >/dev/null 2>&1 || {
  h agent send-keys bare down enter >/dev/null
  h agent wait bare --until idle --timeout 60000 >/dev/null
}
bin/berthier tick
bare_sid=$(jq -r --arg p "$bare" '.agents[$p].session_id' "$BERTHIER_HOME/state.json")
[[ $bare_sid =~ ^[0-9a-f-]{36}$ ]] || fail "no session id recorded for the bare agent (got $bare_sid)"
kill -KILL "$(claude_pid "$bare")"
sleep 3
bin/berthier tick
(($(logged '.kind == "forgot" and .pane == $p' "$bare"))) || fail "transcript-less session not forgotten: $(tail -5 "$BERTHIER_HOME/loop.log"; jq -c --arg p "$bare" ".agents[\$p]" "$BERTHIER_HOME/state.json")"
(($(logged '.kind == "revived" and .pane == $p' "$bare") == 0)) || fail "transcript-less session was Revived"

step "cutover: a Claude started before the hooks existed is kept as it was, not Revived"
# The loop restarts on a build with hooks while live Claudes have no record: the
# first tick must not see them vanish (the Revive would be into a live Claude).
mkdir -p "$work/old"
old=$(h workspace create --cwd "$work/old" --label old --no-focus | jq -re '.result.root_pane.pane_id')
old_sid=$(cat /proc/sys/kernel/random/uuid)
h agent start old --kind claude --pane "$old" --timeout 60000 -- --model haiku --session-id "$old_sid" >/dev/null 2>&1 || {
  h agent send-keys old down enter >/dev/null
  h agent wait old --until idle --timeout 60000 >/dev/null
}
old_ws=$(h pane get "$old" | jq -re '.result.pane.workspace_id')
for f in "$BERTHIER_HOME"/agents/*.json; do jq -e --arg p "$old" '.pane == $p' "$f" >/dev/null 2>&1 && rm -f "$f"; done
# What the old build's last tick had written for it.
bin/berthier tick
jq --arg p "$old" --arg pane "$pane" --arg s "$old_sid" --arg w "$old_ws" \
  '.agents[$p] = (.agents[$pane] | .session_id = $s | .name = "old" | .workspace_id = $w | .workspace_label = "old" | .status = "idle")' \
  "$BERTHIER_HOME/state.json" >"$work/state.tmp" && mv "$work/state.tmp" "$BERTHIER_HOME/state.json"
mark=$(wc -l <"$BERTHIER_HOME/loop.log")
for _ in 1 2 3; do bin/berthier tick; sleep 1; done
tail -n "+$((mark + 1))" "$BERTHIER_HOME/loop.log" >"$work/since"
(($(logged '.kind | startswith("revive")' "$old" "$work/since"))) && fail "unrecorded live Claude met a Revive: $(cat "$work/since")"
jq -e --arg p "$old" '.agents[$p].status == "idle"' "$BERTHIER_HOME/state.json" >/dev/null \
  || fail "unrecorded Agent not kept: $(jq -c --arg p "$old" '.agents[$p]' "$BERTHIER_HOME/state.json")"
h workspace close "$old_ws" >/dev/null

step "a cold big Agent that crashes is Frozen: not Revived, its messages held; a Fresh Start delivers them after"
# Its own directory, where Bash is allowed: a Fresh Start's first turn runs berthier notes and git.
mkdir -p "$work/cold/.claude"
echo '{"permissions": {"allow": ["Bash"]}}' >"$work/cold/.claude/settings.local.json"
cold=$(h workspace create --cwd "$work/cold" --label cold --no-focus | jq -re '.result.root_pane.pane_id')
h agent start cold --kind claude --pane "$cold" --timeout 60000 -- --model haiku --settings "$settings" >/dev/null 2>&1 || {
  h agent send-keys cold down enter >/dev/null
  h agent wait cold --until idle --timeout 60000 >/dev/null
}
for try in 1 2 3; do
  h agent prompt cold "Reply with just: pong" --wait --timeout 90000 >/dev/null 2>&1 && break
  ((try < 3)) || fail "cold agent never took its first prompt"
  sleep 3
done
bin/berthier tick
cold_sid=$(jq -r --arg p "$cold" '.agents[$p].session_id' "$BERTHIER_HOME/state.json")
[[ $cold_sid =~ ^[0-9a-f-]{36}$ ]] || fail "no session id recorded for the cold agent (got $cold_sid)"
# Kill it only once it has answered: a session that never called the API has no context to freeze.
cold_jsonl() { ls "${CLAUDE_CONFIG_DIR:-$HOME/.claude}"/projects/*/"$cold_sid".jsonl 2>/dev/null | head -1; }
for _ in $(seq 60); do
  f=$(cold_jsonl)
  [[ -n $f ]] && grep -q '"usage"' "$f" && break
  sleep 1
done
[[ -n $(cold_jsonl) ]] && grep -q '"usage"' "$(cold_jsonl)" || fail "cold agent never answered its first prompt"
kill -KILL "$(claude_pid "$cold")"
sleep 3
# Every cache counts as cold and every context as big: the haiku session plays a big one an hour idle.
frozen() { BERTHIER_CACHE_WARM=0 BERTHIER_FROZEN_MIN=1 BERTHIER_SPEAK=/bin/true bin/berthier "$@"; }
frozen tick
(($(logged '.kind == "frozen" and .pane == $p and .why == "crash"' "$cold") == 1)) || fail "cold agent not Frozen: $(tail -3 "$BERTHIER_HOME/loop.log")"
jq -e --arg p "$cold" '.agents[$p] | .status == "gone" and .frozen.why == "crash" and .frozen.context > 0' "$BERTHIER_HOME/state.json" >/dev/null \
  || fail "Frozen not in the state: $(jq -c --arg p "$cold" '.agents[$p]' "$BERTHIER_HOME/state.json")"
bin/berthier army | jq -e --arg p "$cold" '[.. | objects | select(.pane_id? == $p)][0] | .frozen.revive_usd > 0 and .needs_you' >/dev/null \
  || fail "Console view lacks the Frozen Agent's choice"
frozen notify cold "Reply with just: thawed" >/dev/null || fail "notify to a Frozen Agent refused"
frozen tick
frozen tick
(($(logged '(.kind | startswith("revive")) and .pane == $p' "$cold"))) && fail "Frozen agent was Revived"
(($(logged '.kind == "delivered" and .pane == $p' "$cold"))) && fail "message delivered to a Frozen agent: $(tail -6 "$BERTHIER_HOME/loop.log")"
claude_pid "$cold" | grep -q . && fail "Frozen agent restarted"
out=$(frozen fresh "$cold") || fail "fresh failed: $out"
echo "$out"
[[ $out == *"Fresh Start: cold in $cold"* ]] || fail "fresh misreported: $out"
new_sid=$(agent_sid "$cold")
[[ -n $new_sid && $new_sid != "$cold_sid" ]] || fail "no new session in the pane (got '$new_sid', was $cold_sid)"
h pane read "$cold" --source recent-unwrapped --lines 200 | grep -qF "[berthier fresh] You were restarted fresh: run berthier notes" || fail "fresh prompt not in the pane: $(h pane read "$cold" --source recent-unwrapped --lines 40; tail -5 "$BERTHIER_HOME/loop.log" | cut -c1-300; for f in "$BERTHIER_HOME"/agents/*.json; do jq -c --arg p "$cold" 'select(.pane == $p)' "$f"; done)"
for _ in $(seq 60); do
  frozen tick
  (($(logged '.kind == "delivered" and .pane == $p and .from == "service"' "$cold"))) && break
  sleep 2
done
(($(logged '.kind == "delivered" and .pane == $p and .from == "service"' "$cold"))) || fail "held message not delivered after the Fresh Start: $(tail -5 "$BERTHIER_HOME/loop.log")"
h agent wait cold --timeout 90000 >/dev/null
h pane read "$cold" --source recent-unwrapped --lines 200 | grep -qF "[berthier notify] from service: Reply with just: thawed" || fail "held message not in the pane"
h workspace close "$(h pane get "$cold" | jq -re '.result.pane.workspace_id')" >/dev/null

step "the Stop hook makes an Agent report; an ask reaches an idle Agent"
helper=$(h workspace create --cwd "$repo" --label helper --no-focus | jq -re '.result.root_pane.pane_id')
hook=$work/hooks-stop.json
jq --arg c "$PWD/bin/berthier hook-stop" '.hooks.Stop += [{hooks: [{type: "command", command: $c}]}]' "$settings" >"$hook"
h agent start helper --kind claude --pane "$helper" --timeout 60000 -- --model haiku --settings "$hook" >/dev/null 2>&1
h agent prompt helper "Reply with just: pong" --wait --timeout 120000 >/dev/null
helper_sid=$(agent_sid "$helper")
[[ -s $BERTHIER_HOME/reports/$helper_sid.json ]] || fail "helper ended its turn without a report"
echo "helper reported: $(jq -r .line "$BERTHIER_HOME/reports/$helper_sid.json")"
bin/berthier tick
HERDR_PANE_ID=$helper bin/berthier ask demo "Reply with just: ack" >/dev/null
h agent wait demo-main --until idle --timeout 60000 >/dev/null
bin/berthier tick
(($(logged '.kind == "delivered" and .pane == $p and .from == "helper" and .to == "demo"' "$pane"))) || fail "ask not delivered"
h agent wait demo-main --timeout 90000 >/dev/null
h pane read "$pane" --source recent-unwrapped --lines 60 | grep -q '\[berthier ask\] from helper' || fail "ask not in demo-main's pane"
# The Agent may answer with an ask of its own, to helper.
grep -qsF '"to":"demo"' "$BERTHIER_HOME"/inbox/*.json && fail "delivered ask left in the inbox"

step "an ask naming oversized screenshots is submitted, not left as a draft"
# Claude resizes big images while it pastes their paths and drops the Enter
# that arrives meanwhile (2026-10-21, skipli).
python3 - "$work" <<'PY'
import struct, sys, zlib
w = h = 2400
raw = b"".join(b"\0" + bytes((x + y) % 256 for x in range(w * 3)) for y in range(h))
chunk = lambda t, d: struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d))
png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)) \
    + chunk(b"IDAT", zlib.compress(raw, 9)) + chunk(b"IEND", b"")
for i in range(4):
    open(f"{sys.argv[1]}/shot{i}.png", "wb").write(png)
PY
mark=$(wc -l <"$BERTHIER_HOME/loop.log")
HERDR_PANE_ID=$helper bin/berthier ask demo "Screenshots: $work/shot0.png $work/shot1.png $work/shot2.png $work/shot3.png. Reply with just: seen" >/dev/null
bin/berthier tick
tail -n "+$((mark + 1))" "$BERTHIER_HOME/loop.log" >"$work/since"
(($(logged '.kind == "delivered" and .pane == $p and .from == "helper" and .to == "demo"' "$pane" "$work/since"))) \
  || fail "image ask not delivered: $(cat "$work/since")"
h agent wait demo-main --timeout 90000 >/dev/null
# Claude lists a sent prompt's images under it; a draft only shows [Image #n].
h pane read "$pane" --source recent-unwrapped --lines 60 | grep -q '⎿.*\[Image #' \
  || fail "image ask not submitted: $(h pane read "$pane" --source recent-unwrapped --lines 60)"

step "a notify from a service (no pane, bare PATH) reaches the idle Marshal, multi-line, one-way"
env -i PATH="$PWD/bin:$HOME/.nix-profile/bin:/usr/local/bin:/usr/bin:/bin" HOME="$HOME" BERTHIER_HOME="$BERTHIER_HOME" berthier notify demo $'Learner answered:\nline two. Reply with just: noted' >/dev/null
for _ in $(seq 30); do # the Agent may still be answering the previous ask
  bin/berthier tick
  (($(logged '.kind == "delivered" and .from == "service" and .to == "demo"'))) && break
  sleep 2
done
(($(logged '.kind == "delivered" and .pane == $p and .from == "service" and .to == "demo"' "$pane"))) || fail "notify not delivered: $(tail -3 "$BERTHIER_HOME/loop.log"; ls "$BERTHIER_HOME/inbox"; h agent get "$pane" | jq -c ".result.agent | {agent_status, name}"; h pane read "$pane" --source visible | tail -25)"
h agent wait demo-main --timeout 90000 >/dev/null
out=$(h pane read "$pane" --source recent-unwrapped --lines 80)
grep -q '\[berthier notify\] from service: Learner answered:' <<<"$out" || fail "notify not in demo-main's pane"
grep '\[berthier notify\]' <<<"$out" | grep -q 'Reply or report back' && fail "notify told the Agent to ask back"
grep -qsF '"from":"service"' "$BERTHIER_HOME"/inbox/*.json && fail "delivered notify left in the inbox"

step "the user's draft holds the loop's prompts until the box has been empty for BERTHIER_DRAFT_QUIET"
# The user dictating into an idle Agent's box: an ask typed in would mix in and submit the draft.
h agent wait demo-main --timeout 90000 >/dev/null
h pane send-text "$pane" "half a dictated thought" >/dev/null
sleep 1
# Claude puts a no-break space after ❯.
box() { h pane read "$pane" --source visible | grep -c '^❯.\{1,3\}half a dictated thought *$'; }
mark=$(wc -l <"$BERTHIER_HOME/loop.log")
HERDR_PANE_ID=$helper bin/berthier ask demo "Reply with just: later" >/dev/null
BERTHIER_DRAFT_QUIET=4 bin/berthier tick
BERTHIER_DRAFT_QUIET=4 bin/berthier tick
tail -n "+$((mark + 1))" "$BERTHIER_HOME/loop.log" >"$work/since"
(($(logged '.kind == "draft_held" and .pane == $p' "$pane" "$work/since") == 1)) || fail "draft hold not logged once: $(cat "$work/since")"
(($(logged '.kind == "delivered" and .to == "demo"' "" "$work/since"))) && fail "ask typed into the user's draft"
(($(box) == 1)) || fail "draft disturbed: $(h pane read "$pane" --source visible)"
out=$(bin/berthier prompt "$pane" "go on" 2>&1) && fail "manual prompt typed into the draft"
[[ $out == *"input box holds the user's draft"* ]] || fail "manual prompt refused unclearly: $out"
(($(box) == 1)) || fail "draft disturbed by the manual prompt"
h pane send-keys "$pane" ctrl+u >/dev/null # the user clears the box
sleep 1
BERTHIER_DRAFT_QUIET=4 bin/berthier tick
tail -n "+$((mark + 1))" "$BERTHIER_HOME/loop.log" >"$work/since"
(($(logged '.kind == "delivered" and .to == "demo"' "" "$work/since"))) && fail "ask delivered within the quiet period"
sleep 4
# One message per tick, and Agents answer asks with asks of their own (helper's
# earlier "ack" may be waiting too): tick until ours has gone.
for _ in $(seq 20); do
  BERTHIER_DRAFT_QUIET=4 bin/berthier tick
  grep -qsF 'Reply with just: later' "$BERTHIER_HOME"/inbox/*.json || break
  h agent wait demo-main --timeout 90000 >/dev/null
done
tail -n "+$((mark + 1))" "$BERTHIER_HOME/loop.log" >"$work/since"
(($(logged '.kind == "delivered" and .pane == $p and .from == "helper"' "$pane" "$work/since"))) || fail "held ask not delivered: $(cat "$work/since")"
grep -qsF 'Reply with just: later' "$BERTHIER_HOME"/inbox/*.json && fail "held ask still in the inbox: $(cat "$work/since")"
h agent wait demo-main --timeout 90000 >/dev/null
# Submitted, not left in the box: Claude saved it as the user's prompt (the pane's wording varies).
for _ in $(seq 30); do grep -qs '"content":"\[berthier ask\] from helper: Reply with just: later' "${CLAUDE_CONFIG_DIR:-$HOME/.claude}"/projects/*/"$sid".jsonl && break; sleep 1; done
grep -qs '"content":"\[berthier ask\] from helper: Reply with just: later' "${CLAUDE_CONFIG_DIR:-$HOME/.claude}"/projects/*/"$sid".jsonl \
  || fail "held ask not sent: $(h pane read "$pane" --source recent-unwrapped --lines 60)"

step "adopt makes helper a Division of demo"
bin/berthier adopt helper demo >/dev/null
bin/berthier tick
jq -e '.corps[] | select(.name == "demo") | .divisions | map(.name) == ["helper"]' "$BERTHIER_HOME/state.json" >/dev/null \
  || fail "helper not nested under demo"

step "a Herdr restart keeps every name and Revives nothing"
# Herdr resumes Claude sessions itself after a restart, but forgets names it did
# not assign at agent start (e.g. agent rename) and briefly lists those panes
# without an agent.
typed=$(h workspace create --cwd "$repo" --label typed --no-focus | jq -re '.result.root_pane.pane_id')
h pane run "$typed" "claude --model haiku --settings $settings" >/dev/null
for _ in $(seq 30); do h agent get "$typed" >/dev/null 2>&1 && break; sleep 1; done
h agent wait "$typed" --until idle --timeout 60000 >/dev/null
h agent prompt "$typed" "Reply with just: pong" --wait --timeout 90000 >/dev/null
h agent rename "$typed" typed >/dev/null
bin/berthier tick
# Berthier's own view: Herdr no longer tracks Agents, it only holds pane names.
names() { for p in "$pane" "$helper" "$typed"; do bin/berthier army "$p" | jq -c '[.pane, .name]'; done | jq -sc 'sort'; }
before=$(names) mark=$(wc -l <"$BERTHIER_HOME/loop.log")
h server stop >/dev/null
tmux kill-session -t "$session" 2>/dev/null || true
bin/berthier tick || [[ $? == 3 ]] # the loop's tick while Herdr is down
attach
for _ in $(seq 20); do bin/berthier tick; sleep 1; done
[[ $(names) == "$before" ]] || fail "names or sessions changed across restart: $before -> $(names)
$(tail -n "+$((mark + 1))" "$BERTHIER_HOME/loop.log" | cut -c1-300 | tail -20)
$(h api snapshot | jq -c '.result.snapshot | {agents: [.agents[] | {pane_id, name, agent_status}], panes: [.panes[] | {pane_id, name, label}]}')
$(bin/berthier tick 2>&1; echo rc=$?)
$(tail -3 "$BERTHIER_HOME/loop.log" | cut -c1-300)
$(for f in "$BERTHIER_HOME"/agents/*.json; do cut -c1-250 "$f"; done)
$(jq -c '.agents | map_values({name, status, session_id})' "$BERTHIER_HOME/state.json")
$(h pane process-info --pane "$typed" | jq -c .result.process_info.foreground_processes)"
since=$work/since.log
tail -n "+$((mark + 1))" "$BERTHIER_HOME/loop.log" >"$since"
(($(logged '.kind | startswith("revive")' "" "$since"))) && fail "Revive attempted after restart: $(cat "$since")"
(($(logged '.kind == "renamed" and .pane == $p and .name == "typed"' "$typed" "$since"))) || fail "name restore not logged"

step "a Herdr restart mid-turn: the cut-off Agent is told to continue, an idle one is not"
# Herdr resumes sessions idle; the Main Loop was down meanwhile (ticks exit 3
# while Herdr is unreachable, writing no state), so the gap marks the restart.
cut=$(h workspace create --cwd "$repo" --label cut --no-focus | jq -re '.result.root_pane.pane_id')
jq '.permissions.allow = ["Bash"]' "$settings" >"$work/hooks-bash.json"
h pane run "$cut" "claude --model haiku --settings $work/hooks-bash.json" >/dev/null
for _ in $(seq 30); do h agent get "$cut" >/dev/null 2>&1 && break; sleep 1; done
h agent wait "$cut" --until idle --timeout 60000 >/dev/null
h agent prompt "$cut" "Run this bash command, then reply with just: done. Command: sleep 300" >/dev/null
h agent wait "$cut" --until working --timeout 60000 >/dev/null
sleep 5  # the tool call reaches the transcript
bin/berthier tick
jq -e --arg p "$cut" '.agents[$p].status == "working"' "$BERTHIER_HOME/state.json" >/dev/null || fail "cut agent not working before the restart"
mark=$(wc -l <"$BERTHIER_HOME/loop.log")
h server stop >/dev/null
tmux kill-session -t "$session" 2>/dev/null || true
sleep $((BERTHIER_RESTART_GAP + 2))
bin/berthier tick || [[ $? == 3 ]]
attach
for _ in $(seq 40); do bin/berthier tick 2>/dev/null; sleep 1; done
tail -n "+$((mark + 1))" "$BERTHIER_HOME/loop.log" >"$work/since"
(($(logged '.kind == "continued" and .pane == $p' "$cut" "$work/since") == 1)) || fail "cut-off agent not continued once: $(cat "$work/since")"
(($(logged '.kind == "continued"' "" "$work/since") == 1)) || fail "an Agent that was not mid-turn was told to continue: $(cat "$work/since")"
for _ in $(seq 30); do
  h pane read "$cut" --source recent-unwrapped --lines 200 | grep -qF "continue where you left off" && break
  sleep 1
done
h pane read "$cut" --source recent-unwrapped --lines 200 | grep -qF "continue where you left off" || fail "continue prompt not in the pane"
# The resumed Agent sits on a permission dialog; drop it so it does not stir the screen later.
h workspace close "$(h pane get "$cut" | jq -re '.result.pane.workspace_id')" >/dev/null

step "an Agent stopped on its usage limit is nudged once the limit has reset"
# Haiku plays the stop (Claude renders the line as a real one).
limited=$(h workspace create --cwd "$repo" --label limited --no-focus | jq -re '.result.root_pane.pane_id')
h pane run "$limited" "claude --model haiku --settings $settings" >/dev/null
for _ in $(seq 30); do h agent get "$limited" >/dev/null 2>&1 && break; sleep 1; done
h agent wait "$limited" --until idle --timeout 60000 >/dev/null
h agent rename "$limited" limited >/dev/null
bin/berthier tick
# More history than the screen holds, on screen, so a read that scrolls shows.
h agent prompt limited "Print the numbers 1 to 80, one per line, nothing else." --wait --timeout 90000 >/dev/null
h workspace focus "$(h pane get "$limited" | jq -re '.result.pane.workspace_id')" >/dev/null
# The tick reads the newly idle pane for a limit stop; the user's view must not move.
# Only above Claude's input box: its status rows gain and lose notices (usage, effort) by themselves,
# as does the right-aligned notice just above the box (e.g. "◐ medium · /effort"), so that line is blanked.
screen() {
  tmux capture-pane -p -t "$session" | sed 's/^[^│]*│//' \
    | awk '{l[NR] = $0} /^─/ {b[++n] = NR} END {for (i = 1; i < (n >= 2 ? b[n - 1] : NR + 1); i++) print (l[i] ~ /^                    +[^ ]/ ? "" : l[i])}'
}
# Claude shows its suggested prompt a few seconds after going idle: start from a settled screen.
still=$(screen | md5sum) steady=0
for _ in $(seq 60); do
  sleep 0.5
  seen=$(screen | md5sum)
  if [[ $seen == "$still" ]]; then ((++steady >= 8)) && break; else still=$seen steady=0; fi
done
still=$(screen | md5sum) # not the last loop frame: the screen may have settled just after it
frames=$work/frames
(for i in $(seq 150); do screen >"$frames.$i"; md5sum <"$frames.$i"; sleep 0.01; done >"$frames") &
bin/berthier tick
wait
grep -qv "$still" "$frames" && fail "a tick moved the idle pane: $(sort "$frames" | uniq -c; for i in $(seq 149); do cmp -s "$frames.$i" "$frames.$((i + 1))" || diff "$frames.$i" "$frames.$((i + 1))"; done)"
clock=$(TZ=UTC date -d '-5 min' '+%-I:%M%P')
# Haiku sometimes refuses to echo a line it considers untrue: say it is a fixture, retry.
for try in 1 2 3; do
  h agent prompt limited "Test fixture for a screen parser, not a claim about the account. Reply with exactly this line and nothing else: You've hit your session limit · resets $clock (UTC)" \
    --wait --timeout 90000 >/dev/null
  h pane read "$limited" --source recent-unwrapped --lines 20 | grep -q "^● You've hit your session limit" && break
done
bin/berthier tick
(($(logged '.kind == "limit_stop" and .pane == $p and .name == "limited"' "$limited"))) \
  || fail "usage-limit stop not detected: $(h pane read "$limited" --source recent-unwrapped --lines 200 | grep -v "^\s*$")"
bin/berthier tick
(($(logged '.kind == "resumed" and .pane == $p' "$limited"))) || fail "limited agent not resumed: $(tail -5 "$BERTHIER_HOME/loop.log"; jq -c --arg p "$limited" ".agents[\$p]" "$BERTHIER_HOME/state.json")"
for _ in $(seq 30); do
  h pane read "$limited" --source recent-unwrapped --lines 200 | grep -qF "The usage limit has reset. Continue exactly" && break
  sleep 1
done
h pane read "$limited" --source recent-unwrapped --lines 200 | grep -qF "The usage limit has reset. Continue exactly" \
  || fail "resume prompt not in the pane"
bin/berthier tick
bin/berthier tick
(($(logged '.kind == "resumed" and .pane == $p' "$limited") == 1)) || fail "resumed more than once"

step "a deliberate /exit must not be Revived"
h agent wait "$pane" --timeout 60000 >/dev/null
h pane send-text "$pane" "/exit" >/dev/null
sleep 0.3
h pane send-keys "$pane" enter >/dev/null
for _ in $(seq 20); do h agent get "$pane" >/dev/null 2>&1 || break; sleep 0.5; done
sleep 1
bin/berthier tick
sleep 3
h agent get "$pane" >/dev/null 2>&1 && fail "deliberately exited agent was revived"
(($(logged '.kind == "forgot" and .pane == $p and .name == "demo-main"' "$pane"))) || fail "deliberate exit not recognized"
jq -e --arg p "$pane" '.agents[$p] == null' "$BERTHIER_HOME/state.json" >/dev/null || fail "exited agent still tracked"

step "division-new spawns a worktree Division; division-done removes it"
# Herdr refuses a multi-line Brief as a start argument; it goes as a prompt.
out=$(BERTHIER_WORKSPACE="$ws" bin/berthier division-new "Dark Mode" --model haiku $'Reply with just: first\nThen, on its own line: second')
echo "$out"
[[ $out == *" started: "* ]] || fail "division-new misreported its start"
h agent get demo-dark-mode >/dev/null 2>&1 || fail "division general left unnamed"
h agent wait demo-dark-mode --timeout 90000 >/dev/null
h agent read demo-dark-mode --source recent-unwrapped --lines 40 | grep -q '^  second' || fail "multi-line Brief not answered"
h workspace list | jq -e 'any(.result.workspaces[]; .label == "dark-mode")' >/dev/null || fail "division workspace missing"
jq -e '.["dark-mode"].model == "haiku"' "$BERTHIER_HOME/divisions.json" >/dev/null || fail "division model not recorded"
dpane=$(h agent get demo-dark-mode | jq -r '.result.agent.pane_id')
model_of() { tr '\0' ' ' <"/proc/$(claude_pid "$dpane")/cmdline"; }
[[ $(model_of) == *"--model haiku"* ]] || fail "division not running --model haiku: $(model_of)"
[[ $(model_of) == *"--disallowedTools Artifact"* ]] || fail "division may use Artifact: $(model_of)"
bin/berthier tick
# The recorded model went stale (e.g. a /model switch): a Revive follows the transcript, and the record with it.
jq '.["dark-mode"].model = "sonnet"' "$BERTHIER_HOME/divisions.json" >"$BERTHIER_HOME/divisions.tmp" && mv "$BERTHIER_HOME/divisions.tmp" "$BERTHIER_HOME/divisions.json"
kill -KILL "$(claude_pid "$dpane")"
sleep 3
bin/berthier tick
for _ in $(seq 30); do claude_pid "$dpane" 2>/dev/null | grep -q . && break; sleep 1; done
[[ $(model_of) == *"--resume"*"--model claude-haiku"*"--disallowedTools Artifact"* ]] || fail "revived division not on its transcript's model, or lost its flags: $(model_of)"
jq -e '.["dark-mode"].model | startswith("claude-haiku")' "$BERTHIER_HOME/divisions.json" >/dev/null || fail "division model record did not follow the transcript"
bin/berthier tick
jq -e '.corps[] | select(.name == "demo") | any(.divisions[]; .name == "dark-mode" and .worktree)' "$BERTHIER_HOME/state.json" >/dev/null \
  || fail "state does not show the division under demo"
BERTHIER_WORKSPACE="$ws" bin/berthier division-done "Dark Mode"
h workspace list | jq -e 'any(.result.workspaces[]; .label == "dark-mode") | not' >/dev/null || fail "division workspace not removed"
git -C "$repo" show-ref --verify --quiet refs/heads/dark-mode && fail "merged division branch not deleted"
jq -e '.["dark-mode"] == null' "$BERTHIER_HOME/divisions.json" >/dev/null || fail "division still recorded"

step "wake clears only the Agents that were working at Shutdown, not the idle ones"
# Two fresh Agents on one turn each (so their sessions can be cleared): calm idles, busy runs a long command.
start_agent() {
  local p
  p=$(h workspace create --cwd "$repo" --label "$1" --no-focus | jq -re '.result.root_pane.pane_id')
  h pane run "$p" "claude --model haiku --settings $work/hooks-bash.json" >/dev/null
  for _ in $(seq 30); do h agent get "$p" >/dev/null 2>&1 && break; sleep 1; done
  h agent wait "$p" --until idle --timeout 20000 >/dev/null 2>&1 || { # a folder trust dialog
    h agent send-keys "$p" down enter >/dev/null
    h agent wait "$p" --until idle --timeout 60000 >/dev/null
  }
  for try in 1 2 3; do
    h agent prompt "$p" "Reply with just: pong" --wait --timeout 90000 >/dev/null 2>&1 && break
    ((try < 3)) || fail "$1 never took its first prompt"
    sleep 3
  done
  echo "$p"
}
calm=$(start_agent calm) busy=$(start_agent busy)
h agent prompt "$busy" "Run this bash command, then reply with just: done. Command: sleep 45" >/dev/null
h agent wait "$busy" --until working --timeout 60000 >/dev/null
sleep 3
bin/berthier tick
jq -e --arg b "$busy" --arg c "$calm" '.agents[$b].status == "working" and (.agents[$c].status | IN("idle", "done"))' "$BERTHIER_HOME/state.json" >/dev/null || fail "busy not working and calm idle before the Shutdown: $(jq -c --arg b "$busy" --arg c "$calm" '[.agents[$b].status, .agents[$c].status]' "$BERTHIER_HOME/state.json")"
bin/berthier shutdown "e2e" >/dev/null
h agent wait "$busy" --timeout 120000 >/dev/null # the command ends, the Shutdown follows
for _ in $(seq 60); do # every Agent takes its Shutdown, then settles
  bin/berthier tick
  ls "$BERTHIER_HOME"/inbox/*shutdown* >/dev/null 2>&1 || break
  sleep 2
done
ls "$BERTHIER_HOME"/inbox/*shutdown* >/dev/null 2>&1 && fail "Shutdown not delivered to everyone: $(ls "$BERTHIER_HOME/inbox"; cat "$BERTHIER_HOME"/inbox/*shutdown-0.json; jq -c '.agents | map_values({name, status})' "$BERTHIER_HOME/state.json"; for f in "$BERTHIER_HOME"/agents/*.json; do cut -c1-200 "$f"; done; tail -5 "$BERTHIER_HOME/loop.log" | cut -c1-300)"
h agent wait "$busy" --timeout 120000 >/dev/null
h agent wait "$calm" --timeout 120000 >/dev/null
sleep 3
bin/berthier tick
sid_of() { jq -r --arg p "$1" '.agents[$p].session_id' "$BERTHIER_HOME/state.json"; }
calm_sid=$(sid_of "$calm") busy_sid=$(sid_of "$busy")
mark=$(wc -l <"$BERTHIER_HOME/loop.log")
out=$(bin/berthier wake)
echo "$out"
for _ in $(seq 60); do
  bin/berthier tick
  ls "$BERTHIER_HOME"/inbox/*wake* >/dev/null 2>&1 || break
  sleep 2
done
tail -n "+$((mark + 1))" "$BERTHIER_HOME/loop.log" >"$work/since"
(($(logged '.kind == "delivered" and (.msg | startswith("woke ")) and .to == "calm"' "" "$work/since") == 0)) || fail "Wake cleared the idle calm: $(cat "$work/since")"
(($(logged '.kind == "delivered" and (.msg | startswith("woke ")) and .to == "busy"' "" "$work/since") == 1)) || fail "Wake did not wake busy: $(cat "$work/since")"
bin/berthier tick
[[ $(sid_of "$calm") == "$calm_sid" ]] || fail "calm's session changed: it was cleared"
[[ $(sid_of "$busy") != "$busy_sid" ]] || fail "busy's session did not change: it was not cleared"
grep -q '^woken (had work going, by shutdown): .*busy' <<<"$out" || fail "wake does not say it woke busy: $out"
grep -q '^skipped (idle, left untouched): .*calm' <<<"$out" || fail "wake does not say it skipped calm: $out"
h workspace close "$(h pane get "$busy" | jq -re '.result.pane.workspace_id')" >/dev/null
h workspace close "$(h pane get "$calm" | jq -re '.result.pane.workspace_id')" >/dev/null

step "corps-retire refuses a dirty Corps, then retires it; corps-reopen brings it back"
retiree=$work/retiree
mkdir -p "$retiree" && git -C "$retiree" init -q && git -C "$retiree" commit -q --allow-empty -m init
rpane=$(h workspace create --cwd "$retiree" --label retiree --no-focus | jq -re '.result.root_pane.pane_id')
h agent start retiree --kind claude --pane "$rpane" --timeout 60000 -- --model haiku >/dev/null 2>&1 || {
  h agent send-keys retiree down enter >/dev/null
  h agent wait retiree --until idle --timeout 60000 >/dev/null
}
for try in 1 2 3; do
  h agent prompt retiree "Reply with just: pong" --wait --timeout 90000 >/dev/null 2>&1 && break
  ((try < 3)) || fail "retiree never took its first prompt"
  sleep 3
done
bin/berthier tick
echo wip >"$retiree/wip.txt"
out=$(bin/berthier corps-retire retiree 2>&1) && fail "a dirty Corps was retired"
[[ $out == *"dirty tree"* ]] || fail "dirty refusal unclear: $out"
h workspace list | jq -e 'any(.result.workspaces[]; .label == "retiree")' >/dev/null || fail "refused retire closed the workspace"
rm "$retiree/wip.txt"
bin/berthier corps-retire retiree
h workspace list | jq -e 'any(.result.workspaces[]; .label == "retiree") | not' >/dev/null || fail "retired Corps still has a workspace"
bin/berthier tick
jq -e '[.corps[].name] | index("retiree") == null' "$BERTHIER_HOME/state.json" >/dev/null || fail "retired Corps still in the state"
out=$(HERDR_PANE_ID=$helper bin/berthier ask retiree "hi" 2>&1) && fail "ask to a retired Corps was queued"
[[ $out == *"retired"* ]] || fail "ask refusal does not name it retired: $out"
bin/berthier shutdown >/dev/null
ls "$BERTHIER_HOME"/inbox/*shutdown* | xargs jq -r .to | grep -qx retiree && fail "Shutdown went to a retired Corps"
rm -f "$BERTHIER_HOME"/inbox/*shutdown* "$BERTHIER_HOME/wake.json"
bin/berthier corps-reopen retiree
h workspace list | jq -e 'any(.result.workspaces[]; .label == "retiree")' >/dev/null || fail "reopened Corps has no workspace"
rws=$(h workspace list | jq -re '.result.workspaces[] | select(.label == "retiree") | .workspace_id')
rpane=$(h pane list --workspace "$rws" | jq -re '.result.panes[0].pane_id')
for _ in $(seq 30); do h pane read "$rpane" --source recent-unwrapped --lines 200 | grep -qF "restarted fresh" && break; sleep 1; done
h pane read "$rpane" --source recent-unwrapped --lines 200 | grep -qF "restarted fresh" || fail "reopened Marshal was not told to rebuild"
h workspace close "$rws" >/dev/null
bin/berthier tick

step "a Corps is warned at 80% of its weekly budget and held at 100%"
# Quota samples as the status line would send them; the speaker is a stub.
mkdir -p "$repo/sub"
printf '#!/bin/sh\necho "$*" >>"%s/spoken"\n' "$work" >"$work/speak" && chmod +x "$work/speak"
budgeted=$(h workspace create --cwd "$repo/sub" --label budgeted --no-focus | jq -re '.result.root_pane.pane_id')
h agent start budgeted --kind claude --pane "$budgeted" --timeout 60000 -- --model haiku --settings "$settings" >/dev/null 2>&1 || {
  h agent send-keys budgeted down enter >/dev/null
  h agent wait budgeted --until idle --timeout 60000 >/dev/null
}
for try in 1 2 3; do
  h agent prompt budgeted "Reply with just: pong" --wait --timeout 90000 >/dev/null 2>&1 && break
  ((try < 3)) || fail "budgeted agent never took its first prompt"
  sleep 3
done
bin/berthier tick
resets=$(date -d '+3 days' +%s) base=40
# The Agents' own status lines sample into this home too, reporting the real
# account: continue its week, or their rows would start a new window.
real=$(jq -cs 'map(select(.session != "e2e-quota" and .resets_at != null)) | last // empty' "$BERTHIER_HOME/ledger.jsonl" 2>/dev/null) || real=
[[ -z $real ]] || { base=$(jq .week_pct <<<"$real"); resets=$(jq .resets_at <<<"$real"); }
rm -rf "$BERTHIER_HOME"/{ledger.jsonl,usage.json,sampled}
pong() { # a turn of the budgeted Corps: its Spend explains the next rise
  h agent prompt budgeted "Reply with just: pong" --wait --timeout 90000 >/dev/null 2>&1 || fail "budgeted agent did not answer"
  sleep 1
}
quota() { # cost week_pct_gain: the status line JSON of a session working in the Corps
  # A real status line carries the five-hour window; a row without it is ignored.
  jq -cn --argjson c "$1" --argjson p "$2" --argjson b "$base" --argjson r "$resets" --arg d "$repo/sub" \
    '{session_id: "e2e-quota", cwd: $d, cost: {total_cost_usd: $c}, rate_limits: {seven_day: {used_percentage: ($b + $p), resets_at: $r}, five_hour: {used_percentage: 1, resets_at: ($r - 200000)}}}' \
    | bin/berthier sample
}
tick() { BERTHIER_SPEAK=$work/speak bin/berthier tick; }
bin/berthier budget budgeted 10 >/dev/null
quota 0 0
pong
quota 1 8
tick
(($(logged '.kind == "escalated" and .why == "budget_warn" and .corps == "budgeted"'))) || fail "80% not escalated: $(tail -5 "$BERTHIER_HOME/loop.log")"
(($(logged '.kind == "spoke" and .corps == "budgeted"'))) || fail "80% not logged as spoken"
grep -q 'budgeted has used 80 percent' "$work/spoken" || fail "80% not spoken"
for _ in $(seq 30); do
  h pane read "$budgeted" --source recent-unwrapped --lines 100 | grep -qF "[berthier-loop] Weekly budget:" && break
  sleep 1
done
h pane read "$budgeted" --source recent-unwrapped --lines 100 | grep -qF "[berthier-loop] Weekly budget:" || fail "Marshal not told"
tick
(($(logged '.kind == "escalated" and .why == "budget_warn"') == 1)) || fail "warned more than once"
bin/berthier budget | grep -q '^budgeted  *8%  *10%  *80%' || fail "budget shows: $(bin/berthier budget)"
bin/berthier army | jq -e '.budget.corps | any(.name == "budgeted" and .allowance == 10 and .used > 7.9)' >/dev/null || fail "Console view lacks the budget"
pong
quota 2 11
tick
(($(logged '.kind == "budget_exhausted" and .corps == "budgeted"') == 1)) || fail "100% not logged once"
(($(grep -c 'over its weekly budget' "$work/spoken") == 1)) || fail "100% not spoken once"
mark=$(wc -l <"$BERTHIER_HOME/loop.log")
kill -KILL "$(claude_pid "$budgeted")"
sleep 3
for _ in 1 2 3; do tick; sleep 1; done
(($(logged '.kind == "revived" and .pane == $p' "$budgeted" <(tail -n "+$((mark + 1))" "$BERTHIER_HOME/loop.log")))) && fail "revived a Corps over budget"
claude_pid "$budgeted" 2>/dev/null | grep -q . && fail "agent over budget was restarted"
(($(logged '.kind == "budget_exhausted"') == 1)) || fail "100% logged again"
bin/berthier budget budgeted 50 >/dev/null
tick
for _ in $(seq 30); do claude_pid "$budgeted" 2>/dev/null | grep -q . && break; sleep 1; done
claude_pid "$budgeted" | grep -q . \
  || fail "not revived once its budget was raised: $(tail -4 "$BERTHIER_HOME/loop.log"; jq -c --arg p "$budgeted" '.agents[$p]' "$BERTHIER_HOME/state.json")"

step "the SessionStart hook gives Berthier's repo root its persona, through a real Claude"
# The worktree is the repo root, so its session is Berthier's; its hook prints BERTHIER.md.
hook=$(jq -cn --arg c "$PWD/bin/berthier session-start" '{hooks: {SessionStart: [{hooks: [{type: "command", command: $c}]}]}}')
answer=$(timeout 180 claude -p --model haiku --settings "$hook" "Reply with just the name your persona gives you, nothing else.") || fail "claude -p failed"
[[ $answer == *Berthier* ]] || fail "repo root's session is not Berthier: $answer"

step "session-start is silent outside the Army, and fast"
start_ns=$(date +%s%N)
echo '{"session_id":"x","cwd":"/nonexistent"}' | env -u HERDR_WORKSPACE_ID bin/berthier session-start >"$work/ss" || fail "session-start failed outside the Army"
(( ($(date +%s%N) - start_ns) < 2000000000 )) || fail "session-start took too long"
[[ ! -s $work/ss ]] || fail "session-start printed outside the Army: $(cat "$work/ss")"

printf '\nE2E PASSED\n'
