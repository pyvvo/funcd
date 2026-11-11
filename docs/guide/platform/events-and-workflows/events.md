---
title: Events
description: What emits an event, and what a Sensor does with it.
---

![EventSources emit events on a timer, a new object or a row change. The in-process fanout hands them to Sensors, which call a Function, start a WorkflowRun, or store a failed delivery as a dead letter](../../assets/events.drawio.svg)

An EventSource emits named events: on a timer, on a new object in a Bucket, or on a row change in a SQL table. Each
event is a CloudEvent, which the EventSource publishes into an in-process fanout. The fanout keeps nothing, so an event
emitted while its Sensor is down is lost.

A Sensor subscribes to named events and runs an action when one arrives. It calls a Function, for which the activator
starts a worker when none is running, or it starts a WorkflowRun. When the Sensor cannot make a delivery, after its
retries or because it had to stop, it stores the delivery as a dead letter in the event store.
