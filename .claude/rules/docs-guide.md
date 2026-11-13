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
- State a default together with its exceptions, and keep both the same wherever the page mentions them.
- Give every "only", "exactly one" or "never" its scope, such as the same key or the same version.
- Label an excerpt as an excerpt, explain each name it uses but does not define, and link the complete file beside
  it.
- Show the code that explains the current task. When a file also holds unrelated operations, include a tested region
  for the task and link the complete file, instead of making the reader scan the whole file.
- Use conventional technical terms: a method returns a value, a response contains a field, a caller has
  permission. Repeat the name of an operation or a setting when a pronoun could refer to more than one.
- Keep what a field does apart from what its reference page documents: a field such as `roleRef` names one role, and
  the reference lists every role.
- Title a code block for what it shows, such as "Built-in KV policy". A file name is a title only when the reader
  creates, opens or edits that file.
- Write no empty section and no summary that repeats the page.
- The Platform chapter explains concepts only: no spec fields, YAML or funcdctl commands. Those belong to the Developer
  chapter, `docs/guide/develop/`.
- Check every fact in the code on main before writing it: a default, a limit, a behavior.
- Run every command and example that a page shows before it is published, the way the page tells a reader to: an
  example that `funcdctl dev` runs with it, a tool's command with the tool, and a server configuration by starting
  `funcd --config` with it. Run it with only the tools the page declares on the `PATH`, and send each step as soon as
  the step before it returns: a startup banner or a create acknowledgement is no sign that the work is ready or done,
  so a step that depends on asynchronous work first shows what to wait for and how to check it again.
- Diagrams are `.drawio.svg` files in `docs/guide/assets/`, linked with a relative path. A diagram never repeats a path
  that another diagram already shows.
