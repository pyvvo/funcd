---
title: Events
description: What fires an event, and what a Sensor does with it.
---

![EventSources fire events on a timer, a new object or a row change; the in-process fanout hands them to Sensors, which call a Function, start a WorkflowRun, or park a failed delivery as a dead letter](events.drawio.svg)

An EventSource fires named events: on a timer, on a new object in a Bucket or on a row change in a SQL table. Each
event is a CloudEvent, published into an in-process fanout that keeps nothing: an event fired while its Sensor is down
is lost.

A Sensor subscribes to named events and runs an action when one fires: it calls a Function, which the activator wakes
when it has no worker running, or it starts a WorkflowRun. A delivery the Sensor could not make, after its retries or
because it had to stop, is parked as a dead letter in the event store.
