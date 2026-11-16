---
paths:
  - "docs/guide/**"
---

# The user guide

- Write in a neutral voice, as Kubernetes' concept pages do: never "you", "your" or "we", in text, alt texts and
  diagram labels. Name the subject ("the operator", "the Function's code") and write steps in the imperative.
- Write plain, standard written English for a reader who knows the task but not funcd's code: complete sentences with
  a subject and a verb, articles kept, one idea per sentence, common words, named actors, precise verbs and literal
  words ("starts a worker", not "wakes"). Define a term before using it and keep exact identifiers. Avoid internal
  slang, vague adjectives and long noun chains, and never shorten a useful explanation until it becomes cryptic.
- Order a page around the reader's task: lead with the goal and the expected result, and introduce a term where the
  example first needs it, not in a glossary before the task. A short introduction is never made by joining
  definitions into one long sentence: keep it to the purpose, and move storage and access internals to the section
  that needs them. A setting that the next step needs for access, persistence or cost is stated before that step, and
  a step links the exact Spec field or method that it uses.
- Keep a Develop guide short: reach a runnable example after a brief purpose and its prerequisites, counted in the
  rendered page with every include expanded. A long included file, such as a whole handler, comes after the first
  commands or in the task it serves, and the first example links it. Keep a sentence only when it helps perform the
  task, choose a setting, check the result or recover. Link concepts, field descriptions and other reference detail;
  remove repeated facts and commentary that restates code. Shortening must keep complete English, required inputs,
  access, waits, expected results, retry steps and relevant data-loss behavior. It also keeps every condition and
  qualifier: a deadline still says that only its expiry fails the call, a wait says what it waits for, and words such
  as only, can, unless and if keep their scope, so that an option never reads as a restriction.
- A rewrite keeps every useful concept and task of the page it replaces. Record where each remains or the exact
  Spec or API Reference section that explains it now, and read that destination. Check coverage rather than word
  count or unchanged commands. Keep the short explanation when no reference explains the fact.
- Assume knowledge of the programming language. Keep language names in the tabs, and remove prose that teaches
  ordinary imports, types, promises, awaiting a call or reading a field. Explain the funcd-specific choice or
  consequence, and link method contracts to Spec. In a wording-only rewrite, preserve existing example code,
  complete manifests, imports, types, commands, outputs and language tabs. Shorten the surrounding prose.
- In Develop Usage pages, keep Status in a table of phases or conditions, reasons and meanings. Keep local CLI
  errors in a table with the message or cause and the action to take. Comprehensive operation, request/response
  and error catalogues belong to the separate API Reference, generated from authoritative schemas and operation
  metadata. Keep the minimal payloads, response examples and recovery steps needed by the Usage task. HTTP and
  server-validation failures remain API errors when a CLI prints them.
- State a default together with its exceptions, and keep both the same wherever the page mentions them.
- Give every "only", "exactly one" or "never" its scope, such as the same key or the same version.
- Label an excerpt and link its complete file. Explain an omitted alias or setting only when the task depends on
  it and its meaning is not clear from the example. Do not add a prose glossary for ordinary variables, input/output
  types or language syntax.
- For a new example, choose tested code for the task. A tested region can leave unrelated operations in the linked
  complete file. In a wording-only rewrite, preserve the existing included code rather than choosing a shorter region.
- Use conventional technical terms: a method returns a value, a response contains a field, a caller has
  permission. Repeat the name of an operation or a setting when a pronoun could refer to more than one.
- Keep what a field does apart from what its reference page documents: a field such as `roleRef` names one role, and
  the reference lists every role.
- Title a code block for what it shows, such as "Built-in KV policy". A file name is a title only when the reader
  creates, opens or edits that file.
- Write no empty section and no summary that repeats the page.
- The Platform chapter explains concepts only: no spec fields, YAML or funcdctl commands. Those belong to the Developer
  chapter, `docs/guide/develop/`.
- Check every fact in the code on main before writing it: a default, a limit, a behavior. Check a behavior through
  the call the reader makes (the route and its validation, the SDK and its errors), not through one helper inside it.
- Run every command and example that a page shows before it is published, the way the page tells a reader to: an
  example that `funcdctl dev` runs with it, a tool's command with the tool, and a server configuration by starting
  `funcd --config` with it. Run it with only the tools the page declares on the `PATH`, and send each step as soon as
  the step before it returns: a startup banner or a create acknowledgement is no sign that the work is ready or done,
  so a step that depends on asynchronous work first shows what to wait for and how to check it again. A name in a
  list is no sign of completion either: check the stated phase itself, and repeat the check until it holds.
- Keep the state that a task needs: a task that uses what an earlier task started keeps it running until the last
  step that needs it, and says where to stop it; a task that needs a state the page has already stopped or replaced
  gives the exact steps to start or set it up again. Check it by running the tasks in order, tracking the current
  folder, the running processes and their ports, and the credential variables: each later task says whether it reuses
  them or starts fresh, so that it never clones into a clone or reuses a stopped process.
- Diagrams are `.drawio.svg` files in `docs/guide/assets/`, linked with a relative path. A diagram never repeats a path
  that another diagram already shows.

- In Develop Usage, avoid a file-by-file inventory when the complete manifests and linked example already show
  those files. State a necessary dependency or relationship once where the reader needs it.
