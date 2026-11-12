---
title: Workflow
description: How a WorkflowRun moves through the steps of its Workflow.
---

![Each execution of a Workflow is a WorkflowRun, started by funcdctl or a Sensor. The workflow engine runs it through the steps (Function steps, built-in steps, child workflows) and keeps its progress in the run state](../../assets/workflows.drawio.svg)

A Workflow declares its steps and the stores they bind. Steps run in list order, unless a step names its parents with
`dependsOn`. A step can wait for all of its parents or for any one of them, and a `when` condition can skip it. A step
is a Function, a built-in step (`wait` or `pass`) that the engine runs itself, or another Workflow, run as a child.

A WorkflowRun is one execution of a Workflow, with its input. `funcdctl workflow run`, an applied manifest or a Sensor
starts one, and the run keeps using the revisions of the step Functions that it started with. The workflow engine
runs the steps. It calls each Function step through the activator, passes each step's output to the steps that
follow, retries a failed step as its retry policy says, and keeps the run's progress in the run-state store. A run is
in one of these phases: Pending, Running, Paused, Succeeded, Failed or Cancelled. `funcdctl workflow replay` runs a
finished run again from a chosen step.
