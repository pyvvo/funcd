---
paths:
  - "docs/guide/**"
---

# The user guide

- Write in a neutral voice, as Kubernetes' concept pages do: never "you", "your" or "we", in text, alt texts and
  diagram labels. Name the subject ("the operator", "the Function's code") and write steps in the imperative.
- Write standard written English: complete sentences with a subject and a verb, articles kept, one idea per sentence,
  and literal words ("starts a worker", not "wakes").
- The Platform chapter explains concepts only: no spec fields, YAML or funcdctl commands. Those belong to the Developer
  chapter, `docs/guide/develop/`.
- Check every fact in the code on main before writing it: a default, a limit, a behavior.
- Run every command and example that a page shows before it is published; an example runs with `funcdctl dev`.
- Diagrams are `.drawio.svg` files in `docs/guide/assets/`, linked with a relative path. A diagram never repeats a path
  that another diagram already shows.
