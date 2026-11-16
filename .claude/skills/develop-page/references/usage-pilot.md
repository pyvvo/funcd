# A pilot for Usage pages

Read the complete service pilot at `docs/guide/develop/services/secrets/index.md`. It demonstrates how to shorten
prose while preserving the full runnable examples. For a workload, also read the Site pilot. Transfer the writing
choices and reader contract; do not copy Secrets-specific sections or behavior into another feature.

## What to copy

- Start with the purpose and the action the reader takes. Keep setup and a clear first result.
- Keep the complete manifests and handlers in their language tabs, with the commands and expected outputs.
- Use brief labels to introduce code. Do not explain every import, type, argument or return value after showing it.
- An excerpt needs a complete-file link, not a glossary of obvious variable or type names. Explain only omitted
  settings or aliases whose meaning changes how the task is performed.
- Let complete manifests supply names and values. Replace a repetitive file inventory with the necessary resource
  relationship, and explain each dependency only where it affects a step.
- Keep shared facts outside the tabs. Give a specific role, scope or behavior where the task needs it.
- Keep Status and CLI errors as tables. Keep applicable roles and the Cedar policy visible.
- Keep access, lifecycle, concurrency and data-loss conditions that change what the reader should do.

## A wording decision

Before:

> Neither method returns a value. In TypeScript, each returns a promise, which the handler awaits, so that the write
> is done before the handler reads the key again.

After: remove the paragraph. Preserve the full handler with its types and `await`, and link the Handler API for
the method contract. Removing prose is not permission to remove the code it describes.

The opening can say:

> Secrets store named values such as API keys. Declare and bind a Secret to a Function, then read its values through
> `ctx.secret`.

This is a worked example, not a sentence template that every feature must repeat.

For another feature, a paragraph explaining how an ORM routes statements or how a standard package tool creates
its environment can also be removed. Keep the required tool and command. Keep the migration's repeat safety and
transaction behavior, rather than the helper's internal dispatch algorithm. The full code still stays on the page.

## A coverage decision

The pilot keeps the binary-value encoding note because the generated Spec states the text restriction but does not
explain that encoding recipe. It keeps the distinction between deleting a Secret and stopping an existing worker.
It links the parent declarations for Apps and Workflows. These are decisions and consequences a reader cannot learn
from a method signature alone.

The method signatures, supported value rules and write-conflict responses already have a precise home in the
Handler API, so the Usage page links them. Read the reference before making the same decision for another page.
If a method contract is missing there, record a proposed addition to its owning reference rather than expanding
Usage into a catalogue. Keep the short operational explanation that the task needs and report the reference gap
until that addition exists. Removed code narration does not need a new home in Spec.

## Check the result

Compare the original and rewritten built Markdown with `scripts/check_examples.py`. It checks code and language
tabs, not prose coverage. Review the concept map separately and run the page's tasks in order, tracking folders and
running processes. Judge the normal site's rendered page and Markdown export. A shorter source file or a custom
preview alone is not the acceptance check.
