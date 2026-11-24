---
title: Job
description: How a Job runs its code once for each JobRun, and what a run keeps.
---

A Job is code that runs to its end, such as a report, an import or a migration, rather than code that serves calls. It
declares its code, its runtime and its handler as a Function does, and the stores, Secrets and Functions that the code
uses. Each change to a Job's spec creates an immutable Revision.

A JobRun is one run of a Job, with its input. A run waits until its Job is Ready and a slot is free: a Job lets a set
number of its runs hold a worker at once, and the node sets a number for all Jobs together. When a run starts, it pins
its Job's current Revision, and funcd starts one worker of that Revision for the run alone. funcd calls the handler
once with the run's input. It stops and removes the worker at every end, and then writes the run's end: a successful
answer becomes the run's result, and any other ends the run Failed. A run is in one of these phases: Pending, Running,
Succeeded, Failed or Cancelled.

funcd never runs a run again on its own. A run that fails, that is cancelled, or that is running when funcd stops
ends for good, and only a new run does the work again. A run reaches the stores, Secrets and Functions of its Job as
the Job itself, a principal apart from every Function, a Function of the same name included. funcd deletes an ended
run once a retention period has passed.
