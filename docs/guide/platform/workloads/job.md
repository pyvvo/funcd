---
title: Job
description: How a Job runs its code once for each JobRun, who may start a run, what it waits for, and what it reaches.
---

A Job is code that runs to its end, such as a report, an import or a migration, rather than code that serves calls. It
declares its code, its runtime and its handler as a Function does, and the stores, Secrets and Functions that the code
uses. A JobRun is one run of a Job, with its input. A developer starts a run with `funcdctl job run`, a Function starts
one through its binding, and an App declares the Jobs its parts need.

![An App declares Jobs, and each change of a Job is a Revision. A developer or a Function starts a JobRun with its input. The run pins its Job's Revision, previews and waits for a person's approval unless its Job says bypass, then gets a worker of its own, which calls the handler once. The handler's answer ends the run, and the worker reaches stores, Secrets and egress as the Job](../../assets/jobs.drawio.svg)

## Revisions

Each change to a Job's spec creates an immutable Revision. When a run takes its first slot, it pins its Job's current
Revision and keeps it to its end, so a later change of the Job never changes a run that is already going. A Revision
that a run still pins is kept.

## Waiting for a slot

A run waits in Pending until its Job is Ready and a slot is free. A Job lets a set number of its runs hold a worker at
once, and the node sets a number for all Jobs together; a freed slot goes to the oldest waiting run whose Job is below
its own number. While the platform or the run's namespace is held, as during a restore, no run starts.

## Approval

Unless its Job sets `approval.bypass`, a run first previews: funcd calls the code's preview in a worker of its own,
which answers what the run would do, and keeps that answer as the run's preview, with its digest. The run then waits
for a person who may approve the Job's runs, holding no worker and no slot, until that person approves the digest or
rejects the run. An approved run runs exactly the preview that was approved, and a rejected run ends without running.
An approved preview older than the Job's `approval.expiry` when its run gets a slot is previewed again, and the run
goes on when the new preview has the approved digest. [Approve a run](../../develop/workloads/job/approval.md) shows
how.

## The run's worker

When a run takes its slot, funcd starts one worker of the pinned Revision for that run alone, and calls the handler
once with the run's input. The handler gets the run's name, its Job, its Revision and who started it. The Job's
timeout bounds the call. funcd stops and removes the worker at every end, and then writes the run's end: a successful
answer becomes the run's result, and any other ends the run Failed, with the reason.

## Ends

A run is in one of these phases: Pending, Previewing, AwaitingApproval, Running, Succeeded, Failed, Cancelled or
Rejected. The last four are its ends, and an end is final. funcd never runs a run again on its own: a run that
fails, that is cancelled, or that is running when funcd stops ends for good, and only a new run does the work again. A
restore of the platform cancels the runs it finds open. funcd deletes an ended run once a retention period has passed.

## Started by a Function

A Function whose bindings name a Job may start a run of it and read the run back. It gets the run's name at once, as
the run does not end within a call. A start may name its run, so that a retried start finds the same run rather than a
second one. The run records the Function that started it.

## Jobs in Apps

An App declares Jobs next to its Functions, so an App template carries the Jobs that its parts need, and the App's
upgrades and deletion move them with the App.

## Identity

A run's worker reaches the stores, Secrets and Functions of its Job as the Job itself, a principal apart from every
Function, a Function of the same name included. Each run has a local API of its own. An owner of a store or a Secret,
and an item of an EgressPolicy's `appliesTo`, name a Job with its kind; an EgressPolicy with no `appliesTo` covers the
namespace's Functions and no Job.

## Runtimes from add-ons

A Job runs on funcd's own runtimes, such as `nodejs22` and `python314`, or on a runtime that an
[add-on](../services/addons.md) ships for Jobs, such as the OpenTofu runtime of the InfraStack add-on. The run's worker
runs that runtime's image.
