export const meta = {
  name: 'fix-batch',
  description: 'Fix a wave of funcd issues: per issue a test-first fix, an independent review and rework in its own worktree; per group an integrator, the gate and one PR; then a wave check of the group branches together, one ledger PR and the Lima lanes',
  whenToUse: 'The /fix-batch skill, once its issues are resolved into groups (args: sp, repo, wave, units)',
  phases: [
    { title: 'Fix', detail: 'one fixer per issue, in its own worktree from origin/main, all in parallel' },
    { title: 'Review', detail: 'one independent fix-review per issue, right after its fix' },
    { title: 'Rework', detail: 'address changes-requested findings, then re-review (at most 3 reviews)' },
    { title: 'Integrate', detail: 'per group, at most two at a time: cherry-pick the passing fixes onto main, gate, one PR', model: 'sonnet' },
    { title: 'Wave', detail: 'merge every group branch together and gate the result once, before anything is queued', model: 'sonnet' },
    { title: 'Ledger', detail: 'one PR recording every review of the wave in the model ledger', model: 'sonnet' },
    { title: 'Lanes', detail: 'the Lima lanes of the groups that touch containerd, network or e2e, one after the other', model: 'sonnet' },
  ],
}

// args: {
//   sp: a scratch directory for the worktrees and review reports (outside the repository),
//   repo: a funcd checkout to add the worktrees from (its own branch is never touched),
//   wave: a label for the wave's ledger branch,
//   units: [{ key, group, tracker (0 for none), all (the unit holds every open sub-issue of its tracker),
//             issues: [{ n, p (priority), t (title), k ('bug', 'flake' or 'task'; default 'bug'),
//                        note (a decision the person already made for it, optional) }] }],
//   model, trailer: the producing model's id and commit trailer, ledger: false to skip the ledger PR }
// Returns per group the PR, the fixed and parked issues, the gate, wave and lane results and the new defects noted.

const SP = args.sp
const REPO = args.repo
const MODEL = args.model || 'claude-opus-5-5'
const TRAILER = args.trailer || 'Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>'
for (const k of ['sp', 'repo', 'wave', 'units']) {
  if (args[k] == null) throw new Error(`fix-batch: args.${k} is required`)
}
const LANE_PATHS = 'internal/runtime/containerd, internal/network, e2e/ or scripts/lanes.yaml'

// This run changes code, so at most five of its agents run at once, whatever the workflow runner allows (a
// read-only campaign may run more): more parallel agents on one host caused the 2026-10 campaign's contention
// incidents. Every agent call goes through run().
const MAX_AGENTS = Math.min(5, args.maxAgents || 5)
let freeAgents = MAX_AGENTS
const agentWaiters = []
const run = async (prompt, opts) => {
  if (freeAgents > 0) freeAgents--
  else await new Promise(resolve => agentWaiters.push(resolve))
  try {
    return await agent(prompt, opts)
  } finally {
    const next = agentWaiters.shift()
    if (next) next()
    else freeAgents++
  }
}

const EFFICIENT = `WORK EFFICIENTLY: every turn costs seconds, so use few of them.
- Send independent tool calls together in ONE turn. Read a whole file once rather than many slices. Chain dependent shell steps with && in one call.
- Keep outputs short: run only the tests you need (\`-run\`) and filter output (\`2>&1 | tail -25\`, \`| grep -E 'FAIL|ok |panic'\`).
- Run Go and just as \`scripts/agent/d <cmd>\` from the worktree: the pinned dev shell, cached (never \`nix develop -c\`).
- Do not run what later stages run: no e2e suite, no \`go test ./...\` over the repo, no Linux lint, no Lima lanes.`

const RULES = (wt) => `RULES:
- Work only in ${wt} (start every shell command with \`cd ${wt} &&\`). Never modify, switch or reset another checkout; \`git worktree add\` from ${REPO} is the only allowed use of it. git stash is refused in this repository (refs/stash is shared by every worktree).
- No pushes, no PRs, no GitHub writes (reading issues is fine).
- Never write an absolute path, a username or an email into a commit, code, test or report.
- The repo's CLAUDE.md applies (block-style YAML, top-level imports, no comment bloat, ADR-0002); never edit an Accepted or Implemented ADR.
- Kill any process you start, by PID. The host is shared: no stress loop (keep a probe to a few thousand keep-alive requests), at most -count=20, and a test that assembles a platform with funcd.New uses a short data dir (\`os.MkdirTemp("", "funcd")\`), never \`t.TempDir()\` (the Unix socket path limit).`

const FIX = {
  type: 'object',
  properties: {
    status: { type: 'string', enum: ['committed', 'parked'] },
    commits: { type: 'array', items: { type: 'string' } },
    test: { type: 'string' }, revert_check: { type: 'string' }, reason: { type: 'string' },
    needs_lane: { type: 'boolean' },
    new_defects: { type: 'array', items: { type: 'string' } },
  },
  required: ['status', 'commits', 'test', 'revert_check', 'reason', 'needs_lane', 'new_defects'],
}
const REVIEW = {
  type: 'object',
  properties: {
    verdict: { type: 'string', enum: ['pass', 'changes-requested', 'fail'] },
    report_path: { type: 'string' },
    blockers: { type: 'integer' }, majors: { type: 'integer' }, minors: { type: 'integer' },
    model_attributed: { type: 'integer' }, dod_passed: { type: 'integer' }, dod_total: { type: 'integer' },
    notes: { type: 'string' }, summary: { type: 'string' },
  },
  required: ['verdict', 'report_path', 'blockers', 'majors', 'minors', 'model_attributed', 'dod_passed', 'dod_total', 'notes', 'summary'],
}
const REWORK = {
  type: 'object',
  properties: { done: { type: 'boolean' }, commits: { type: 'array', items: { type: 'string' } }, note: { type: 'string' } },
  required: ['done', 'commits', 'note'],
}
const INTEGRATE = {
  type: 'object',
  properties: {
    pr_url: { type: 'string' }, branch: { type: 'string' }, fixed: { type: 'array', items: { type: 'integer' } },
    parked: { type: 'array', items: { type: 'object', properties: { issue: { type: 'integer' }, reason: { type: 'string' } }, required: ['issue', 'reason'] } },
    needs_lane: { type: 'boolean' }, lanes_hint: { type: 'string' }, gate: { type: 'string' }, problems: { type: 'string' },
  },
  required: ['pr_url', 'branch', 'fixed', 'parked', 'needs_lane', 'lanes_hint', 'gate', 'problems'],
}
const WAVE = {
  type: 'object',
  properties: { passed: { type: 'boolean' }, conflicts: { type: 'array', items: { type: 'string' } }, gate: { type: 'string' }, note: { type: 'string' } },
  required: ['passed', 'conflicts', 'gate', 'note'],
}
const PRS = { type: 'object', properties: { pr_url: { type: 'string' }, problems: { type: 'string' } }, required: ['pr_url', 'problems'] }
const LANE = {
  type: 'object',
  properties: { passed: { type: 'boolean' }, lines: { type: 'array', items: { type: 'string' } }, note: { type: 'string' } },
  required: ['passed', 'lines', 'note'],
}

const iwt = (n) => `${SP}/wt/i${n}`
// Branch names carry the wave, so a later wave can retry an issue without colliding with an earlier branch.
const ibranch = (n) => `fix/w${args.wave}-i${n}`
const gwt = (u) => `${SP}/wt/g-${u.key}`
const reportOf = (n, round) => `${SP}/reports/issue-${n}-fix-${MODEL}${round > 1 ? '-' + round : ''}.md`
const gbranch = (u) => u.tracker ? `fix/w${args.wave}-${u.tracker}-${u.key}` : `fix/w${args.wave}-${u.issues[0].n}-${u.key}`
const multi = (u) => u.tracker || u.issues.length > 1

const isTask = (it) => it.k === 'task'

const FIX_STEPS = (it) => `1. Write the regression test TestIssue${it.n}_<Behavior> FIRST, beside the package's tests, reusing their harnesses. It must FAIL on the unfixed code for the reported reason.
2. Make the smallest root-cause fix. Search before you write: reuse existing helpers, types, harnesses and dependencies; never duplicate logic.
3. Revert check: write \`git show origin/main:<file>\` of each non-test file you changed to a scratch file outside the worktree and run the test with \`go test -overlay\` mapping the file to it: it must fail.
4. Checks on the touched packages only: the regression test with -race, the packages' tests, \`go build ./...\`, and \`go vet\` and \`go tool golangci-lint run\` on the touched packages.
5. One commit: subject \`fix(<scope>): <what is fixed>\`, a body with the cause, the fix and the test name, a line \`Fixes #${it.n}\`, then the line \`${TRAILER}\`.
PARK instead (no commit; give the reason) if the issue does not reproduce as written, or the only sound fix needs a design decision or changes an Accepted ADR's decision.`

const TASK_STEPS = (it) => `This is a task (kind/task), not a bug: the issue's "Done when" section is the target.
1. Make the smallest change that meets the "Done when". Search before you write: reuse existing helpers, types, harnesses and dependencies; never duplicate logic.
2. Where the change alters behavior, write TestIssue${it.n}_<Behavior> beside the package's tests so that it fails without the change, and prove it with the revert check: \`git show origin/main:<file>\` of each changed non-test file into a scratch file outside the worktree, then \`go test -overlay\` (it must fail). Where it does not (a refactor, a moved file, a comment, a test-only cleanup), the touched packages' existing tests must pass unchanged; say in revert_check how the "Done when" is verified.
3. Checks on the touched packages only: their tests with -race, \`go build ./...\`, and \`go vet\` and \`go tool golangci-lint run\` on them. A change to the flake, CI or a script also runs, once, the command that change enables or affects (not the gate: a later stage runs it).
4. One commit: a Conventional Commit subject whose type fits the change (\`fix\` when it changes behavior; \`test\`, \`refactor\`, \`ci\`, \`build\` or \`chore\` otherwise) with a scope, a body with what changed, why, and how the "Done when" is met, a line \`Fixes #${it.n}\`, then the line \`${TRAILER}\`.
PARK instead (no commit; give the reason) if the "Done when" cannot be met without a design decision, or it changes an Accepted ADR's decision.`

const fixPrompt = (it) => `${isTask(it) ? 'Do the GitHub task' : 'Fix GitHub issue'} #${it.n} of pyvvo/funcd (${it.p}): "${it.t}", following ${iwt(it.n)}/.claude/skills/fix/SKILL.md Steps 2-6 for scope, checks and commits${isTask(it) ? '' : ', test-first'} (a later stage reviews it and opens the PR).
${it.note ? `\nThe person already decided, for this issue: ${it.note}\n` : ''}
Setup, one call: \`cd ${REPO} && git fetch -q origin && git worktree add -q -b ${ibranch(it.n)} ${iwt(it.n)} origin/main\`. Then read the issue (\`cd ${iwt(it.n)} && python3 .claude/skills/issue-management/driver.py show ${it.n} --body\`); it names the likely cause or the place to change (file:line): start there, and read the code it points at in the same turn.

${isTask(it) ? TASK_STEPS(it) : FIX_STEPS(it)}
needs_lane = true if your commit touches ${LANE_PATHS}.
List in new_defects only another defect you would rate medium or high (wrong behavior a user can hit, data loss, a crash or a hang), one line each with evidence; leave out wording, logging, docs, test-only and unlikely-edge issues. Do not fix it.

${EFFICIENT}

${RULES(iwt(it.n))}`

const reviewPrompt = (it, commits, round) => `You are the independent review gate for the fix of pyvvo/funcd issue #${it.n} ("${it.t}"). Read ${iwt(it.n)}/.claude/skills/fix-review/SKILL.md and apply it, with these adaptations:
- The change: branch ${ibranch(it.n)} in the worktree ${iwt(it.n)}; its commits: ${commits.join(', ')} (\`git diff origin/main...HEAD\`). Read the issue with \`python3 .claude/skills/issue-management/driver.py show ${it.n} --body\`.
- Revert check as Step 2.1 says: an overlay of the \`origin/main\` version of each changed non-test file (\`git show origin/main:<file>\` into a scratch file, \`go test -overlay\`), so the regression test stays and must FAIL for the issue's reason; then it must PASS with -race without the overlay. Never revert the commit (that removes the test too) and leave the worktree clean.${isTask(it) ? `
- This is a task (kind/task): its "Done when" section is the target. When the commit adds a TestIssue${it.n} test, run the revert check above on it. Otherwise verify the "Done when" directly (read the change, run what it enables) and check that the touched packages' tests pass unchanged and that a refactor changes no behavior.` : ''}${it.note ? `
- The person already decided, for this issue: ${it.note}. Judge the change against that decision, not against the alternatives.` : ''}
- 1-3 targeted mutants on the fix's key lines (edit, run only the relevant tests with -run, restore); each must fail a test.
- Apply Step 2.7 (reuse: search for existing code the change duplicates or reinvents) and Step 2.8 (conventions).
- Checks: the touched packages only (tests with -race, vet, lint). No e2e suite, no repo-wide tests, no Linux lint, no lanes: the group gate runs them.
- Producing model: ${MODEL}. Write the report to ${reportOf(it.n, round)} (format: .claude/skills/adr-impl-review/references/review-method.md). Do NOT run scorecard.py and do NOT edit docs/reviews/: return the ledger fields.
${round > 1 ? `- Re-review round ${round}: the previous report is ${reportOf(it.n, round - 1)}; check that the later commits resolve its model findings.\n` : ''}
${EFFICIENT}

${RULES(iwt(it.n))}

Return the verdict, the report path, the counts (blockers, majors, minors, model_attributed), dod_passed of dod_total for the checklist items that apply, a one-line ledger note tagging each finding's attribution, and a two-sentence summary.`

const reworkPrompt = (it, report) => `Address the review findings on the fix for pyvvo/funcd issue #${it.n}, in its worktree ${iwt(it.n)} (branch ${ibranch(it.n)}), following ${iwt(it.n)}/.claude/skills/fix/SKILL.md (rework). The report: ${report}.${isTask(it) ? ' This is a task (kind/task): its "Done when" is the target, not a failing-first regression test.' : ''}${it.note ? ` The person already decided, for this issue: ${it.note}; keep to that decision.` : ''} Resolve every finding attributed to the model (leave issue, adr and env ones, but mention them) in ONE new commit on top (\`fix(<scope>): address review of #${it.n}\`, body \`Refs #${it.n}\`, then \`${TRAILER}\`); never rewrite earlier commits. Re-run the regression test with -race and the touched packages' checks. If a finding needs a design decision, set done=false and explain.

${EFFICIENT}

${RULES(iwt(it.n))}`

const integratePrompt = (u, passed, parkedEarly) => `Integrate the reviewed fixes of the funcd issue group "${u.group}"${u.tracker ? ` (tracker #${u.tracker})` : ''} into one PR.

1. One call: \`cd ${REPO} && git fetch -q origin && git worktree add -q -b ${gbranch(u)} ${gwt(u)} origin/main\`.
2. Cherry-pick each issue's commits, in this order: ${passed.map(p => `#${p.issue}: ${p.commits.join(' ')}`).join('; ')}. On a conflict, resolve it when it is mechanical (keep both sides' intent); otherwise \`git cherry-pick --abort\`, skip that issue and report it parked with the reason.
3. Copy these review reports into docs/reviews/ (keep the file names): ${passed.flatMap(p => p.reports.map(r => `${SP}/reports/${r}`)).join(', ')}. First check them for machine paths (\`grep -nE '/(Users|home)/[^<]|/private/|/var/folders/' <the copied files>\`) and rewrite any hit as a repo-relative path or drop it: these files are tracked. Commit them: \`docs(reviews): fix reviews for ${u.tracker ? `the ${u.group} group (#${u.tracker})` : passed.map(p => `#${p.issue}`).join(', ')}\`, then the line \`${TRAILER}\`. Do NOT touch docs/reviews/model-ledger.json or model-scorecard.md: one ledger PR records the whole wave.
4. Run the gate, one call (about 5 minutes): \`cd ${gwt(u)} && scripts/agent/gate.sh\`. It prints PASS/FAIL per step (logs in .cache/gate/). On GATE FAIL, find the issue whose commit broke it (the failing package or test points at it), \`git revert --no-edit\` that issue's commits, remove its review reports from docs/reviews/ in the same follow-up commit, run the gate once more, and report that issue parked. If the gate still fails, do not open a PR: report the failing step and every issue left parked. An \`audit\` FAIL names a hard flag with its file:line (a new production copy, a production time.Sleep, an unexplained //nolint or a new direct dependency): remove it with a follow-up commit when that is mechanical (reuse the existing helper, explain the nolint, wait on a hook instead of a sleep); otherwise revert that issue's commits and park it. Never write an \`audit-allow:\` waiver yourself. A \`host\` FAIL means the machine's ports are exhausted: wait a few minutes and rerun, never park for it.
5. Push (\`git push -q -u origin ${gbranch(u)}\`) and open the PR: \`gh pr create --repo pyvvo/funcd --base main --head ${gbranch(u)}\`, with
   - the title ${u.tracker ? `\`<type>(<scope>): <what the group does, ≤ 72 chars> (#${u.tracker})\`` : multi(u) ? '`<type>(<scope>): <what the group does, ≤ 72 chars>`' : "of the commit's subject"}, where the type is \`fix\` when the group changes behavior and \`test\`, \`refactor\`, \`ci\`, \`build\` or \`chore\` otherwise (a Conventional Commit: it becomes the squash commit and the release note);
   - a body: a summary; a table (issue, cause, fix, regression test, review verdict with a link to its docs/reviews report); the parked issues with reasons; the gate result with the audit's size, clone and complexity lines; one \`Fixes #N\` line per fixed issue${u.tracker ? (u.all ? `, plus \`Fixes #${u.tracker}\` if every listed issue is fixed (this unit holds every open issue of the tracker)` : `; do NOT add \`Fixes #${u.tracker}\`: the tracker keeps issues that need an ADR`) : ''}; a "Lima lane pending" note when a fix touches ${LANE_PATHS}; and the final line \`🤖 Generated with [Claude Code](https://claude.com/claude-code)\`.
Do not merge or queue. Issues parked before review (list them in the body): ${JSON.stringify(parkedEarly)}.

${EFFICIENT.split('\n').slice(0, 3).join('\n')}

${RULES(gwt(u)).replace('- No pushes, no PRs, no GitHub writes (reading issues is fine).\n', '- Push only this branch and open only this PR; no other GitHub writes.\n')}

Return the PR URL, the branch, the fixed issues, the parked ones with reasons, needs_lane and the lanes that cover the touched paths (from scripts/lanes.yaml), the gate result line, and any problem.`

const wavePrompt = (wt, branches) => `Check this wave's group branches together before any of them is queued. One call: \`cd ${wt} && scripts/agent/wave-check.sh ${branches.join(' ')}\` (it merges them onto origin/main in that order in a scratch worktree, names each branch that conflicts with the ones before it, then gates the merged result; about 5 minutes). Change, push and queue nothing.
Return passed (WAVE PASS), every CONFLICT line, the gate's PASS/FAIL lines, and a note naming the branches a failure points at.`

const ledgerPrompt = (rows) => `Record the fix reviews of this wave in the funcd model ledger and open one PR.
1. One call: \`cd ${REPO} && git fetch -q origin && git worktree add -q -b docs/fix-review-ledger-${args.wave} ${SP}/wt/ledger origin/main\`.
2. For each row with copy=true, copy ${SP}/reports/<report> into docs/reviews/ (no group PR adds it: its issue was parked or reverted), after checking it for machine paths as the integrators do. Then for each row below, in order, from the worktree: \`python3 .claude/skills/adr-impl-review/scripts/scorecard.py record --ledger docs/reviews/model-ledger.json --issue <issue> --phase fix --model ${MODEL} --verdict <verdict> --blockers <b> --majors <m> --minors <n> --model-attributed <a> --dod-passed <p> --dod-total <t> --report docs/reviews/<report file name> --notes "<notes>"\`.
3. Commit (\`docs(reviews): record the fix reviews of wave ${args.wave}\`, then \`${TRAILER}\`), push, and open the PR (\`gh pr create --repo pyvvo/funcd --base main\`): the body says what it records, that it merges after the wave's group PRs (its report links point at files they add), and ends with the line \`🤖 Generated with [Claude Code](https://claude.com/claude-code)\`.
${RULES(`${SP}/wt/ledger`).replace('- No pushes, no PRs, no GitHub writes (reading issues is fine).\n', '- Push only this branch and open only this PR.\n')}
Rows: ${JSON.stringify(rows)}`

const lanePrompt = (wt, specs) => `Run the Lima lanes for this wave. colima must be running first (\`colima status\`, else \`colima start\`; \`docker context show\` prints colima). Then run \`cd ${wt} && scripts/agent/lanes.sh ${specs.join(' ')}\` with the Bash tool's run_in_background and wait for its completion notification, never as one foreground call: a full lane run takes about 12 minutes per spec, and the tool stops a foreground call after 10 (each lane runs through the host lane lock, one after the other; a lane that refuses because a funcd VM is already running names it: report that, do not delete a VM you did not start). Edit, commit and push nothing.
Return whether all passed, each PASS/FAIL line, and a note with the failing case and assertion from .cache/lanes/<branch>-<lane>.log of the main checkout (slashes in the branch become underscores) if one failed.`

// A thrown agent call parks its issue with the reason instead of dropping it from the result.
async function doIssue(it) {
  try {
    return await doIssueSteps(it)
  } catch (e) {
    log(`#${it.n}: the pipeline failed on it: ${e && e.message}`)
    return { issue: it.n, status: 'parked', reason: `the pipeline failed on it: ${e && e.message}`, defects: [] }
  }
}

async function doIssueSteps(it) {
  const fx = await run(fixPrompt(it), { label: `fix:#${it.n}`, phase: 'Fix', schema: FIX })
  if (!fx) return { issue: it.n, status: 'parked', reason: 'the fixer returned nothing', defects: [] }
  if (fx.status !== 'committed' || !fx.commits.length) return { issue: it.n, status: 'parked', reason: fx.reason, defects: fx.new_defects }
  const commits = [...fx.commits]
  const rounds = []
  for (let round = 1; round <= 3; round++) {
    if (round > 1) {
      const rw = await run(reworkPrompt(it, rounds[rounds.length - 1].report_path), { label: `rework:#${it.n}`, phase: 'Rework', schema: REWORK })
      if (!rw || !rw.done) return { issue: it.n, status: 'parked', reason: rw ? rw.note : 'rework failed', rounds, defects: fx.new_defects }
      commits.push(...rw.commits)
    }
    const v = await run(reviewPrompt(it, commits, round), { label: `review:#${it.n}${round > 1 ? '-r' + round : ''}`, phase: round === 1 ? 'Review' : 'Rework', schema: REVIEW, effort: 'medium' })
    if (!v) return { issue: it.n, status: 'parked', reason: 'the reviewer returned nothing', defects: fx.new_defects }
    rounds.push(v)
    if (v.verdict === 'pass') return { issue: it.n, status: 'passed', commits, rounds, needs_lane: fx.needs_lane, defects: fx.new_defects }
    if (v.verdict === 'fail') break
  }
  const last = rounds[rounds.length - 1]
  return { issue: it.n, status: 'parked', reason: `review ${last.verdict}: ${last.summary}`, rounds, defects: fx.new_defects }
}

phase('Fix')
const units = args.units
let slots = 2 // integrators gate in parallel; more than two at once contend for the host's ports and the lint lock
const waiters = []
const acquire = () => slots > 0 ? (slots--, Promise.resolve()) : new Promise(r => waiters.push(r))
const release = () => { const w = waiters.shift(); if (w) w(); else slots++ }
const done = await pipeline(
  units,
  (u) => parallel(u.issues.map(it => () => doIssue(it))).then(rs => ({ u, rs: rs.filter(Boolean) })),
  async ({ u, rs }) => {
    const passed = rs.filter(r => r.status === 'passed').map(r => ({ issue: r.issue, commits: r.commits, reports: r.rounds.map(v => v.report_path.split('/').pop()), rounds: r.rounds }))
    const parkedEarly = rs.filter(r => r.status === 'parked').map(r => ({ issue: r.issue, reason: r.reason }))
    log(`${u.key}: ${passed.length} passed review, ${parkedEarly.length} parked`)
    // Every review of the group goes to the ledger, a parked issue's too; reports no group PR adds are copied by the ledger PR.
    const rowsOf = (keep) => rs.flatMap(r => (r.rounds || []).map(v => ({ issue: r.issue, verdict: v.verdict, b: v.blockers, m: v.majors, n: v.minors, a: v.model_attributed, p: v.dod_passed, t: v.dod_total, report: v.report_path.split('/').pop(), copy: !keep.has(r.issue), notes: v.notes })))
    const base = { unit: u.key, parked: parkedEarly, defects: rs.flatMap(r => r.defects || []), rows: rowsOf(new Set()) }
    if (!passed.length) return { ...base, pr: '', fixed: [] }
    await acquire()
    let ig
    try {
      ig = await run(integratePrompt(u, passed, parkedEarly), { label: `integrate:${u.key}`, phase: 'Integrate', schema: INTEGRATE, model: 'sonnet', effort: 'medium' })
    } catch (e) {
      log(`${u.key}: the integrator failed: ${e && e.message}`)
      ig = null
    } finally {
      release()
    }
    if (!ig) return { ...base, pr: '', fixed: [], problems: 'integrator failed' }
    const fixedSet = new Set(ig.fixed)
    const rows = rowsOf(fixedSet)
    return { ...base, pr: ig.pr_url, branch: ig.branch || gbranch(u), fixed: ig.fixed, parked: [...parkedEarly, ...ig.parked], needs_lane: ig.needs_lane, lanes_hint: ig.lanes_hint, gate: ig.gate, problems: ig.problems, rows }
  },
)

const out = done.filter(Boolean)
const opened = out.filter(r => r.pr)
let wave = null
if (opened.length > 1) {
  phase('Wave')
  const first = units.find(u => u.key === opened[0].unit)
  wave = await run(wavePrompt(gwt(first), opened.map(r => r.branch)), { label: 'wave-check', phase: 'Wave', schema: WAVE, model: 'sonnet', effort: 'low' })
}
const rows = out.flatMap(r => r.rows)
let ledger = null
if (rows.length && args.ledger !== false) {
  phase('Ledger')
  ledger = await run(ledgerPrompt(rows), { label: 'ledger', phase: 'Ledger', schema: PRS, model: 'sonnet', effort: 'low' })
}
const laneSpecs = opened.filter(r => r.needs_lane).map(r => `${r.branch}:all`)
let lanes = null
if (laneSpecs.length) {
  phase('Lanes')
  const first = units.find(u => u.key === opened[0].unit)
  lanes = await run(lanePrompt(gwt(first), laneSpecs), { label: 'lanes', phase: 'Lanes', schema: LANE, model: 'sonnet', effort: 'low' })
}
return {
  ledger_pr: ledger ? ledger.pr_url : '',
  wave: wave || { passed: opened.length < 2, conflicts: [], gate: '', note: opened.length < 2 ? 'one PR or none: nothing to merge together' : 'the wave agent failed' },
  lanes,
  rows: args.ledger === false ? rows : undefined,
  groups: out.map(r => ({ unit: r.unit, pr: r.pr, branch: r.branch || '', fixed: r.fixed, parked: r.parked, needs_lane: !!r.needs_lane, gate: r.gate || '', problems: r.problems || '', new_defects: r.defects })),
}
