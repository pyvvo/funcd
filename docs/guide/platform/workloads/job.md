---
title: Job
description: How a Job runs its code once for each JobRun, and what a run keeps.
---

A Job is code that runs to its end, such as a report, an import or a migration, rather than code that serves calls. It
declares its code, its runtime and its handler as a Function does, and the stores, Secrets and Functions that the code
uses. Each change to a Job's spec creates an immutable Revision.

A JobRun is one run of a Job, with its input. A run waits until its Job is Ready and a slot is free: a Job lets a set
number of its runs hold a worker at once, and the node sets a number for all Jobs together. When a run takes its first
slot, it pins its Job's current Revision, and funcd starts one worker of that Revision for the run alone. funcd calls
the handler once with the run's input. It stops and removes the worker at every end, and then writes the run's end: a
successful answer becomes the run's result, and any other ends the run Failed.

Unless the Job sets `approval.bypass`, a run first previews: funcd calls the code's preview in a worker of its own,
which answers what the run would do, and keeps that answer as the run's preview, with its digest. The run then waits for
a person, holding no worker and no slot, until that person approves the digest or rejects the run. An approved run runs
exactly the preview that the person approved, and a rejected run ends without running.
[Approve a run](../../develop/workloads/job/approval.md) shows how. A run is in one of these phases: Pending,
Previewing, AwaitingApproval, Running, Succeeded, Failed, Cancelled or Rejected.

funcd never runs a run again on its own. A run that fails, that is cancelled, or that is running when funcd stops
ends for good, and only a new run does the work again. A run reaches the stores, Secrets and Functions of its Job as
the Job itself, a principal apart from every Function, a Function of the same name included. funcd deletes an ended
run once a retention period has passed.
