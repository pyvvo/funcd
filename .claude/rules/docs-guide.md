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
- Order a page around the reader's task. A setting that the next step needs for access, persistence or cost is stated
  before that step, and a step links the exact Spec field or method that it uses.
- Write no empty section and no summary that repeats the page.
- The Platform chapter explains concepts only: no spec fields, YAML or funcdctl commands. Those belong to the Developer
  chapter, `docs/guide/develop/`.
- Check every fact in the code on main before writing it: a default, a limit, a behavior.
- Run every command and example that a page shows before it is published, the way the page tells a reader to: an
  example that `funcdctl dev` runs with it, a tool's command with the tool, and a server configuration by starting
  `funcd --config` with it.
- Diagrams are `.drawio.svg` files in `docs/guide/assets/`, linked with a relative path. A diagram never repeats a path
  that another diagram already shows.
