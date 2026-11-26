# REC

Self-hosted engine for capturing, grouping and diagnosing application errors in real time.

## Language

### Capture

**Event**:
A single occurrence of a failure, reported by an SDK, carrying its stack trace, context and breadcrumbs.
_Avoid_: Error report, occurrence, hit

**Breadcrumb**:
A timestamped record of something that happened in the host application before an Event, sent along with it.
_Avoid_: Trail, log line

**SDK**:
A client library embedded in a host application that captures Events and sends them to a REC server.
_Avoid_: Client, agent, tracker

**Release**:
The version of the host application in which an Event occurred.
_Avoid_: Build, deploy, version (alone)

**Environment**:
The deployment stage of the host application where an Event occurred, such as production or staging.
_Avoid_: Stage, env, tier

### Grouping

**Issue**:
A group that consolidates equivalent Events, with a count and a lifecycle state.
_Avoid_: Ticket (reserved for the planning tracker), group, incident, problem

**Group Key**:
The value computed from an Event that decides which Issue it belongs to.
_Avoid_: Fingerprint, hash, signature

**Custom Group Key**:
A Group Key supplied by the SDK for an Event, replacing the one REC would compute.
_Avoid_: Custom fingerprint, override hash

**In-app frame**:
A stack frame from the host application's own code, as opposed to its dependencies or the runtime.
_Avoid_: User frame, app frame

**Regression**:
The reopening of a resolved Issue because a new Event arrived for it.
_Avoid_: Reopen, recurrence, relapse

### Tenancy

**Project**:
An application or service monitored by a REC instance; every Event and Issue belongs to exactly one Project.
_Avoid_: App, organization, tenant

**Ingest Key**:
The credential a Project's SDKs present to send Events to a REC server.
_Avoid_: API key, token
