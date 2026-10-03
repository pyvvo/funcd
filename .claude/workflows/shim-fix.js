export const meta = {
  name: 'shim-fix',
  description: 'Fix funcd issues whose root cause is in a language repo: one fixer + independent review per issue in that repo, then one PR per repo',
  whenToUse: 'A /fix-batch run whose issues have their root cause in the shim or examples of pyvvo/funcd-typescript or pyvvo/funcd-python',
  phases: [
    { title: 'Fix', detail: 'one fixer per issue, in its own worktree of the language repo' },
    { title: 'Review', detail: 'one independent fix-review per issue, right after its fix' },
    { title: 'Rework', detail: 'address changes-requested findings, then re-review (max 2 rounds)' },
    { title: 'Integrate', detail: 'per repo: cherry-pick the passing fixes onto main, just ci, one PR', model: 'sonnet' },
  ],
}

// args: {
//   sp: a scratch directory for the worktrees and review reports (outside every repository),
//   funcd: a read-only funcd checkout (its docs/adr and .claude/skills are read),
//   branch: the integration branch name, unique per run (e.g. fix/funcd-shim-fixes-<n>),
//   units: [{ key ('ts' or 'py'), repo, lang, clone (a checkout of the language repo),
//             issues: [{ repoIssue | n, p, t, split? }] }],
//   model, trailer: the producing model's id and commit trailer, pilot: true to stop before integrating }
// Returns per repo the PR, the fixed and parked issues, the review rows and reports, and the new defects noted.

const SP = args.sp
const FUNCD = args.funcd
const BR = args.branch
const MODEL = args.model || 'claude-opus-5-5'
const TRAILER = args.trailer || 'Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>'
const D = 'scripts/agent/d'

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

const ref = (it) => it.repoIssue ? `#${it.repoIssue}` : `pyvvo/funcd#${it.n}`
const tag = (it) => it.repoIssue ? `r${it.repoIssue}` : `${it.n}`
const iwt = (u, it) => `${SP}/wt/${u.key}-${tag(it)}`
const report = (u, it, round) => `${SP}/reports/issue-${it.repoIssue ? u.key + '-' + it.repoIssue : it.n}-fix-${MODEL}${it.split ? '-' + u.key : ''}${round > 1 ? '-' + round : ''}.md`

const ENV = (u) => `Toolchain: run every repo command as \`${D} <cmd>\` (the repo's pinned dev shell, cached; never \`nix develop -c\`)${u.key === 'py' ? ', with `TMPDIR=/tmp` in front (pytest\'s default macOS temp dir pushes Unix socket paths past the AF_UNIX limit)' : ''}. The repo's full check is \`${D} just ci\`${u.key === 'py' ? ' (with TMPDIR=/tmp)' : ''}: it takes under a minute, so run it once before you commit.`

const EFFICIENT = `WORK EFFICIENTLY: send independent tool calls together in one turn, read a whole file once, chain dependent shell steps with &&, and filter output (\`| tail -25\`).`

const RULES = (wt, repo) => `RULES:
- Work only in ${wt} (start every shell command with \`cd ${wt} &&\`); the one exception is the setup command that creates it from the language repo's checkout. Never touch another worktree, the session's working directory or the user's sibling checkouts; never git stash (refs/stash is shared by every worktree).
- Read ${wt}/CLAUDE.md first and follow it: the formatter/linter it names, built files committed (TypeScript: \`just build\` after changing shim/src or an example, commit the outputs), Conventional Commits, block-style YAML, top-level imports, no comment bloat, release-please owns versions (never edit version.txt, CHANGELOG.md or package versions).
- Design decisions live in funcd (${FUNCD}/docs/adr, read-only for you). A change to the funcd <-> shim contract (FUNCD_* env vars, the health endpoints, the invoke socket protocol, the log-capture wire format, trace spans) needs a funcd ADR first: if the only sound fix changes that contract, PARK. Filling a field the ADR already defines, or making the shim do what an Accepted/Implemented ADR already says, is not a contract change.
- No pushes, no PRs, no GitHub writes (reading issues is fine).
- Never write an absolute path, a username or an email into a commit, code, test or report. Kill any process you start, by PID. No stress loop that opens many connections (the host is shared).`

const FIX = {
  type: 'object',
  properties: {
    status: { type: 'string', enum: ['committed', 'parked'] },
    commits: { type: 'array', items: { type: 'string' } },
    test: { type: 'string' }, revert_check: { type: 'string' }, reason: { type: 'string' },
    funcd_side: { type: 'string', description: 'what funcd itself still needs for this issue (a gate, a regression test through the embedded shim), or "none"' },
    new_defects: { type: 'array', items: { type: 'string' } },
  },
  required: ['status', 'commits', 'test', 'revert_check', 'reason', 'funcd_side', 'new_defects'],
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
    pr_url: { type: 'string' }, fixed: { type: 'array', items: { type: 'string' } },
    parked: { type: 'array', items: { type: 'object', properties: { issue: { type: 'string' }, reason: { type: 'string' } }, required: ['issue', 'reason'] } },
    ci: { type: 'string' }, problems: { type: 'string' },
  },
  required: ['pr_url', 'fixed', 'parked', 'ci', 'problems'],
}

const fixPrompt = (u, it) => `Fix ${it.repoIssue ? `issue #${it.repoIssue} of pyvvo/${u.repo}` : `pyvvo/funcd issue #${it.n}, whose root cause is in pyvvo/${u.repo}`} (${it.p}): "${it.t}"${it.split ? ` — only the ${u.lang} half; the other language repo fixes its own half` : ''}. Work test-first, following the method of ${FUNCD}/.claude/skills/fix/SKILL.md Steps 2–6, adapted to this repo.

Setup, one call: \`git -C ${u.clone} fetch -q origin && git -C ${u.clone} worktree add -q -b fix/${tag(it)}-${u.key} ${iwt(u, it)} origin/main\`. Then read the issue and its comments (\`gh issue view ${it.repoIssue || it.n} --repo pyvvo/${it.repoIssue ? u.repo : 'funcd'} --comments\`): a comment from the fix pipeline names the cause and a suggested fix; verify it in the code before relying on it. Read the funcd ADRs it cites in ${FUNCD}/docs/adr.

1. Write the regression test FIRST, in this repo's test suite beside the module's existing tests, reusing their helpers; name it after the issue (${u.key === 'ts' ? `a test titled "issue ${tag(it)}: <behavior>"` : `test_issue_${tag(it)}_<behavior>`}). It must FAIL on the unfixed code for the reported reason.
2. Make the smallest root-cause fix. Search before you write: reuse existing helpers and dependencies; never duplicate logic. A new dependency must be MIT/Apache-2.0/BSD and pinned in the lockfile.
3. Revert check: copy each changed non-test source file from \`git show origin/main:<file>\` over the working copy, run the test (it must fail), then restore your version (\`git checkout -- <file>\` after committing, or keep a scratch copy outside the worktree). git stash is refused (refs/stash is shared by every worktree).
4. Run \`${D} just ci\` once; it must pass. ${u.key === 'ts' ? 'It fails when a build changes a committed file, so commit the rebuilt outputs.' : ''}
5. One commit: subject \`fix(<scope>): <what is fixed>\` (Conventional Commit), a body with the cause, the fix and the test name, a line ${it.repoIssue ? `\`Fixes #${it.repoIssue}\`` : `\`Refs ${ref(it)}\` (funcd closes its own issue when it pins the release)`}, then the line \`${TRAILER}\`.
PARK instead (no commit; give the reason) if the issue does not reproduce, or the only sound fix needs a design decision or changes the funcd <-> shim contract.
funcd_side: say what funcd itself still needs once this ships (e.g. a gate, or a regression test through the embedded shim), or "none".
List in new_defects only another defect you would rate medium or high (wrong behavior a user can hit, data loss, a crash or a hang), one line each with evidence; leave out wording, logging, docs, test-only and unlikely-edge issues. Do not fix it.

${ENV(u)}
${EFFICIENT}

${RULES(iwt(u, it), u.repo)}`

const reviewPrompt = (u, it, commits, round) => `You are the independent review gate for a fix in pyvvo/${u.repo} for ${it.repoIssue ? `its issue #${it.repoIssue}` : `pyvvo/funcd issue #${it.n}`} ("${it.t}")${it.split ? ` — the ${u.lang} half only` : ''}. Read ${FUNCD}/.claude/skills/fix-review/SKILL.md and apply its checklist to this repo, with these adaptations:
- The change: branch fix/${tag(it)}-${u.key} in the worktree ${iwt(u, it)}; its commits: ${commits.join(', ')} (\`git diff origin/main...HEAD\`). Read the issue with \`gh issue view ${it.repoIssue || it.n} --repo pyvvo/${it.repoIssue ? u.repo : 'funcd'} --comments\`.
- Revert check: copy the \`origin/main\` version of each changed non-test source file over the working copy (\`git show origin/main:<file>\`), so the regression test stays: it must FAIL for the issue's reason; then restore the files (\`git checkout -- <file>\`) and it must PASS. Never revert the commit (that removes the test too); leave the worktree clean.
- 1–3 targeted mutants on the fix's key lines (edit, run only the relevant test, restore); each must fail a test.
- Reuse and no duplication (Step 2.7), this repo's conventions from its CLAUDE.md (Step 2.8), and conformance to the funcd ADRs the issue cites (${FUNCD}/docs/adr). A change to the funcd <-> shim contract without a funcd ADR is a Blocker.
- Checks: \`${D} just ci\`${u.key === 'py' ? ' with TMPDIR=/tmp' : ''}.
- Producing model: ${MODEL}. Write the report to ${report(u, it, round)} (the format of ${FUNCD}/.claude/skills/adr-impl-review/references/review-method.md; say it reviews a pyvvo/${u.repo} change). Do NOT edit anything in funcd.
${round > 1 ? `- Re-review round ${round}: the previous report is ${report(u, it, round - 1)}; check its model findings are resolved by the later commit(s).\n` : ''}
${ENV(u)}
${EFFICIENT}

${RULES(iwt(u, it), u.repo)}

Return the verdict, the report path, the counts (blockers, majors, minors, model_attributed), dod_passed of dod_total, a one-line ledger note tagging each finding's attribution, and a two-sentence summary.`

const reworkPrompt = (u, it, rep) => `Address the review findings on the fix for ${ref(it)} in pyvvo/${u.repo}, in its worktree ${iwt(u, it)} (branch fix/${tag(it)}-${u.key}). The report: ${rep}. Resolve every finding attributed to the model (leave issue/adr/env ones, but mention them) in ONE new commit on top (\`fix(<scope>): address review of ${ref(it)}\`, body \`Refs ${ref(it)}\`, then \`${TRAILER}\`); never rewrite earlier commits. Re-run the regression test and \`${D} just ci\`. If a finding needs a design decision, set done=false and explain.

${ENV(u)}
${EFFICIENT}

${RULES(iwt(u, it), u.repo)}`

const integratePrompt = (u, passed, parkedEarly) => `Integrate the reviewed fixes for pyvvo/${u.repo} into one PR.

1. One call: \`git -C ${u.clone} fetch -q origin && git -C ${u.clone} worktree add -q -b ${BR} ${SP}/wt/${u.key}-integrate origin/main\`.
2. Cherry-pick each issue's commits, in this order: ${passed.map(p => `${p.ref}: ${p.commits.join(' ')}`).join('; ')}.${u.key === 'ts' ? ' Built outputs (shim/*.mjs, example .mjs) conflict between fixes: resolve a conflict in a built file by taking either side, then `just build` and amend the regenerated outputs into that cherry-pick.' : ''} On a conflict in source, resolve it when it is mechanical (keep both sides' intent); otherwise \`git cherry-pick --abort\`, skip that issue and report it parked with the reason.
3. Run \`${D} just ci\`${u.key === 'py' ? ' with TMPDIR=/tmp' : ''}, one call. If it fails, find the issue whose commit broke it, \`git revert --no-edit\` that issue's commits, re-run, and report that issue parked.
4. Push (\`git push -q -u origin ${BR}\`) and open the PR: \`gh pr create --repo pyvvo/${u.repo} --base main --head ${BR}\`, with
   - the title \`fix(shim): <what the fixes do, ≤ 72 chars>\` (a Conventional Commit: it becomes the squash commit and the release note);
   - a body: a summary; a table (issue, cause, fix, regression test, review verdict); the parked issues with reasons; the \`just ci\` result; one line per fixed issue: \`Fixes #N\` for this repo's own issues, \`Refs pyvvo/funcd#N\` for funcd issues (funcd closes those when it pins the release); and the final line \`🤖 Generated with [Claude Code](https://claude.com/claude-code)\`.
Do not merge. Issues parked before review (list them in the body): ${JSON.stringify(parkedEarly)}.

${ENV(u)}

${RULES(`${SP}/wt/${u.key}-integrate`, u.repo).replace('- No pushes, no PRs, no GitHub writes (reading issues is fine).\n', '- Push only this branch and open only this PR; no other GitHub writes.\n')}

Return the PR URL, the fixed issue refs, the parked ones with reasons, the just ci result, and any problem.`

async function doIssue(u, it) {
  const label = `${u.key}:${tag(it)}`
  const fx = await run(fixPrompt(u, it), { label: `fix:${label}`, phase: 'Fix', schema: FIX })
  if (!fx) return { ref: ref(it), it, status: 'parked', reason: 'the fixer returned nothing', defects: [] }
  if (fx.status !== 'committed' || !fx.commits.length) return { ref: ref(it), it, status: 'parked', reason: fx.reason, defects: fx.new_defects, funcd_side: fx.funcd_side }
  const commits = [...fx.commits]
  const rounds = []
  for (let round = 1; round <= 3; round++) {
    if (round > 1) {
      const rw = await run(reworkPrompt(u, it, rounds[rounds.length - 1].report_path), { label: `rework:${label}`, phase: 'Rework', schema: REWORK })
      if (!rw || !rw.done) return { ref: ref(it), it, status: 'parked', reason: rw ? rw.note : 'rework failed', defects: fx.new_defects, funcd_side: fx.funcd_side }
      commits.push(...rw.commits)
    }
    const v = await run(reviewPrompt(u, it, commits, round), { label: `review:${label}${round > 1 ? '-r' + round : ''}`, phase: round === 1 ? 'Review' : 'Rework', schema: REVIEW, effort: 'medium' })
    if (!v) return { ref: ref(it), it, status: 'parked', reason: 'the reviewer returned nothing', defects: fx.new_defects, funcd_side: fx.funcd_side }
    rounds.push(v)
    if (v.verdict === 'pass') return { ref: ref(it), it, status: 'passed', commits, rounds, defects: fx.new_defects, funcd_side: fx.funcd_side, test: fx.test }
    if (v.verdict === 'fail') break
  }
  const last = rounds[rounds.length - 1]
  return { ref: ref(it), it, status: 'parked', reason: `review ${last.verdict}: ${last.summary}`, rounds, defects: fx.new_defects, funcd_side: fx.funcd_side }
}

const out = await pipeline(
  args.units,
  (u) => parallel(u.issues.map(it => () => doIssue(u, it))).then(rs => ({ u, rs: rs.filter(Boolean) })),
  async ({ u, rs }) => {
    const passed = rs.filter(r => r.status === 'passed')
    const parkedEarly = rs.filter(r => r.status === 'parked').map(r => ({ issue: r.ref, reason: r.reason }))
    log(`${u.repo}: ${passed.length} passed review, ${parkedEarly.length} parked`)
    if (args.pilot) return { repo: u.repo, pilot: rs.map(r => ({ ref: r.ref, status: r.status, reason: r.reason || '', commits: r.commits || [], funcd_side: r.funcd_side || '', rounds: (r.rounds || []).map(v => ({ verdict: v.verdict, summary: v.summary })) })) }
    const base = { repo: u.repo, results: rs.map(r => ({ ref: r.ref, n: r.it.n || null, repoIssue: r.it.repoIssue || null, status: r.status, reason: r.reason || '', test: r.test || '', funcd_side: r.funcd_side || '', reports: (r.rounds || []).map(v => v.report_path), rows: (r.rounds || []).map(v => ({ verdict: v.verdict, b: v.blockers, m: v.majors, n: v.minors, a: v.model_attributed, p: v.dod_passed, t: v.dod_total, notes: v.notes })), defects: r.defects || [] })) }
    if (!passed.length) return { ...base, pr: '', fixed: [], parked: parkedEarly }
    const ig = await run(integratePrompt(u, passed, parkedEarly), { label: `integrate:${u.key}`, phase: 'Integrate', schema: INTEGRATE, model: 'sonnet', effort: 'medium' })
    if (!ig) return { ...base, pr: '', fixed: [], parked: parkedEarly, problems: 'integrator failed' }
    return { ...base, pr: ig.pr_url, fixed: ig.fixed, parked: [...parkedEarly, ...ig.parked], ci: ig.ci, problems: ig.problems }
  },
)
return { repos: out.filter(Boolean) }
