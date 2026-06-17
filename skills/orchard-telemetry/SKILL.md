---
name: orchard-telemetry
description: Query and interpret Orchard service telemetry (OperationEvents, TraceEvents) on CAPAnalytics island clusters. Covers activity names, customDimensions key layouts, session tracing, model health checks, and log-structured storage diagnostics. Use when investigating Orchard-specific issues like model load failures, persist errors, container session failures, or storage provider behavior.
disable-model-invocation: true
---

# Orchard Telemetry

Orchard-specific guidance for querying `CAPAnalytics` telemetry. For
general query patterns, performance rules, `customDimensions` cost model,
and jacques usage, see the `core-service-telemetry` skill.

## Activity name format

All Orchard activity names are prefixed with
`PowerApps.Authoring.Orchard.` (applied by the `.ActivityName()` extension
in the codebase). Always use the full form in queries:

```kusto
activityName == "PowerApps.Authoring.Orchard.PersistentModelGrain.Persist"
```

Short names below omit the prefix for readability.

## Key activity names

| Activity | What it is |
|---|---|
| `Model.Api.History` | Fetch model version history |
| `Model.Api.Create` | Create a new model |
| `Model.Api.List` | List models (environment-scoped) |
| `Model.Lifecycle.List` | List models via lifecycle service |
| `Harvest.Api.CreateContainerSession` | Spin up a container for a user session |
| `Harvest.Api.CreateContainerSession.Load` | Inner load step (token acquisition, container provision) |
| `PersistentModelGrain.ApplyPatch` | Apply an edit to the model |
| `PersistentModelGrain.Persist` | Save model to blob storage |
| `PersistentModelGrain.AutoSave` | Background auto-save; wraps a Persist call |
| `PersistentModelGrain.Connect` | First grain activation for a connection |
| `PersistentModelGrain.ConnectObserver` | Observer (container session) attaches |
| `PersistentModelGrain.ReadSegment` | Read a page of history from the grain |
| `PersistentModelGrain.Deactivate` | Grain deactivation |
| `AuthorizationService.CheckAccess` | Authz check; `ResourceId` carries the model ID |
| `PersistentModelHub.OnConnected` | SignalR hub connection |
| `PersistentModelHub.OnDisconnected` | SignalR hub disconnection |
| `ModelMetadata.NotifyMetadataChanged` | ESP metadata writeback after patch |
| `EnterpriseStoragePod.GetEnvironmentDocument` | Read environment document from ESP |
| `StandardPersistentModelStrategy.CreateLoadMessage` | Standard strategy: build model load payload |
| `StandardPersistentModelStrategy.Persist` | Standard strategy: serialize model to blob |
| `StandardPersistentModelStrategy.Dispose` | Standard strategy: cleanup on deactivation |
| `ModelBlobClient.OpenWrite` | Open blob write stream |
| `ModelBlobClient.Get` | Read blob |
| `SimpleStorage.Model.Read` | Blob-backed model read |
| `SimpleStorage.Model.Write` | Blob-backed model write |

## The `requestUri` is the richest context field

On API-layer activities (`Model.Api.History`, `Harvest.Api.CreateContainerSession`),
the `requestUri` in `customDimensions` carries the environment ID, model ID,
host, port, and sometimes query parameters. It is often the **only** place
the environment ID appears.

URL structure:
```
.../api/models/e/{environmentId}/filesystem/{modelId}/history
.../api/harvest/container-sessions/e/{environmentId}/project/{modelId}
```

Extract IDs:
```kusto
| extend
    EnvironmentId = extract(@"/e/([0-9a-f-]{36})/", 1, tostring(parse_json(customDimensions)["requestUri"])),
    ModelId = extract(@"/filesystem/([0-9a-f-]{36})/", 1, tostring(parse_json(customDimensions)["requestUri"]))
```

Some non-API activities (`AuthorizationService.CheckAccess`,
`EnterpriseStoragePod.*`, `Model.Lifecycle.List`) carry `environmentId`
as a first-class key in `customDimensions`.

## customDimensions key map by activity

Keys vary across activities. Common patterns:

**API-layer** (`Model.Api.History`, `Harvest.Api.CreateContainerSession`):
`requestUri`, `httpStatusCode`, `httpMethod`, `referrer`, `userAgent`,
`clientSessionId`, `clientTenantId`, `port`, `hostName`,
`requestContentLength`, `isRequestCanceled`, `callerActivityId`

**Grain-layer** (`PersistentModelGrain.*`):
`PersistentModelGrain.ModelId`, `PersistentModelGrain.ObserverCount`,
`PersistentModelGrain.Tombstoned`

**Authz** (`AuthorizationService.CheckAccess`):
`ResourceId` (model ID), `environmentId`, `PrincipalId`, `Permission`,
`HasAccess`, `clientTenantId`

## clientSessionId correlation gap on grain and hub events

**The indexed `clientSessionId` column is systematically empty on all
grain-layer and SignalR hub events.** Searching by `clientSessionId`
alone will find API-layer events but miss the entire
`PersistentModelGrain.*`, `PersistentModelHub.*`, and
`ContainerSessionGrain.*` layer. This is a known telemetry gap in the
Orchard codebase.

### What is affected

| Activity layer | Indexed `clientSessionId` | Where user session ID actually lives |
|---|---|---|
| API-layer (`Model.Api.*`, `Harvest.Api.*`, `Harvest.Proxy.*`) | Populated from `x-ms-client-session-id` HTTP header | Both indexed column and `customDimensions["clientSessionId"]` |
| SignalR hub (`PersistentModelHub.OnConnected`, `.OnDisconnected`, `.ApplyPatch`) | **Empty** | `customDimensions["ClientContext.SessionId"]` |
| PersistentModelGrain (`Connect`, `ConnectObserver`, `ReadSegment`, `ApplyPatch`, `Persist`) | **Empty** | `customDimensions["ClientContext.SessionId"]` |
| ContainerSessionGrain (`EnsureRunningContainer`, `GetActiveContainerGrainSessionState`) | **Empty** | `customDimensions["sessionId"]` (the harvest session ID, not the client session) |

### Why it happens (source-level)

The indexed `clientSessionId` column is populated by CoreFramework's
`ServiceContext.Root.ClientCorrelation.ClientSessionId`, which the
CoreServices HTTP middleware sets from the `x-ms-client-session-id`
request header. Two code paths lose this value:

1. **SignalR hub path** (`PersistentModelHub.OnConnectedAsync` ->
   `grain.ConnectAsync()`): The hub runs on a WebSocket connection, not
   a per-invocation HTTP request. The `ServiceContext.Root` ambient
   context either has no `ClientCorrelation` or has an empty
   `ClientSessionId` by the time the hub method executes. The
   `ActivityContextOutgoingGrainFilter` captures from
   `ServiceContext.CaptureRoot()` and propagates whatever is there
   (empty) to the grain via Orleans `RequestContext`. The grain's
   `ActivityContextIncomingGrainFilter` faithfully restores the empty
   value.

2. **ContainerSessionGrain -> PersistentModelGrain path**
   (`ConnectObserverAsync`): The `ContainerSessionGrain` receives its
   initial activation from an HTTP API call that does carry
   `clientSessionId`. However, `EnsureRunningContainerAsync` calls
   `SetUpSocketAsync`, which delegates to
   `socketService.RegisterSessionAsync(callback)`. The callback invokes
   `SubscribeToModelAsync` -> `modelGrain.ConnectObserverAsync(...)`,
   and by this point the async-local `ServiceContext.Root` context may
   not carry the original HTTP request's `ClientCorrelation` through
   the callback boundary.

Both paths do log the user's session ID in `customDimensions` via
`clientContext.Telemetry()` (key: `ClientContext.SessionId`), and the
grain's own `CommonTelemetry()` (key: `PersistentModelGrain.ModelId`).
The data is present, just not in the indexed column.

### How to find the missing events

Option A -- search by model ID on the grain node (fast if you know the
model ID and approximate time):

```kusto
union OperationEvents, TraceEvents
| where env_time between (datetime(...) .. datetime(...))
  and applicationName == "fabric:/PowerApps.Authoring.Orchard"
  and activityName has_any ("PersistentModelHub", "PersistentModelGrain.Connect", "ConnectObserver")
  and customDimensions has "<modelId>"
| extend msg = column_ifexists("message", "")
| project env_time, activityName, resultType, durationMs,
    correlationId, customDimensions
| order by env_time asc
```

Option B -- scan `customDimensions` for the client session ID (slower;
narrow the time window):

```kusto
union OperationEvents, TraceEvents
| where env_time between (datetime(...) .. datetime(...))
  and applicationName == "fabric:/PowerApps.Authoring.Orchard"
  and activityName has_any ("PersistentModelHub", "PersistentModelGrain.Connect", "ConnectObserver")
  and customDimensions has "<clientSessionId>"
| project env_time, activityName, resultType, durationMs,
    correlationId, customDimensions
| order by env_time asc
```

### Multi-node topology

A single user session typically spans 2-3 Service Fabric nodes:

| Role | What lands here |
|---|---|
| API gateway node(s) | `Model.Api.*`, `Harvest.Api.*`, `Harvest.Proxy.*`, `CoreServices.CoreServicesWebHost.ProcessRequest` |
| Grain host node | `PersistentModelGrain.*`, `SimpleStorage.*`, `ModelBlobClient.*`, `StandardPersistentModelStrategy.*` |
| SignalR hub node | `PersistentModelHub.*` (may be same as API gateway or separate) |

Use `env_cloud_roleInstance` and `pid` to identify which node handled
which part of the session. Grain operations can land on any node in the
Orleans silo, not necessarily the one that received the HTTP request.

### ConnectObserver uses container ID as ClientContext.SessionId

`ConnectObserverAsync` is called by `ContainerSessionGrain` which
passes a `ClientContext` where `SessionId` is the **container ID**
(e.g., `c87f585b-3c1e-4ee2-9de1-8e0b672df64d`), not the user's client
session ID. This is because `CreateClientContext` in
`ContainerSessionGrain` uses `state.State.ContainerId` as the session
ID parameter. To link a `ConnectObserver` event back to the user
session, match on the model ID and time window instead.

## Correlation pull pattern

Once you find an interesting event (OOM, `ModelNotFoundException`,
`MsalServiceException`), grab its `correlationId` and pull all related
events. Pin to a 2-3 minute window around the event for sub-second results:

```kusto
union OperationEvents, TraceEvents
| where env_time between (datetime(2026-05-21T22:57:02Z) .. datetime(2026-05-21T22:57:05Z))
  and applicationName == "fabric:/PowerApps.Authoring.Orchard"
  and correlationId == "<id>"
| extend msg = coalesce(column_ifexists("message", ""), "")
| project env_time, activityName, resultType, exceptionTypeName, durationMs,
    traceLevel = column_ifexists("traceLevel", ""),
    msg = substring(msg, 0, 500),
    customDimensions
| order by env_time asc
```

Use `column_ifexists` for `message` and `traceLevel` since they only exist
on TraceEvents.

## Features

### Log-structured storage

Present only when log-structured storage is enabled for the model.

| Activity | What it is |
|---|---|
| `PersistentModelStrategy.ReadSegment` | Strategy-layer history read |
| `PersistentModelStrategy.ReceivePatch` | Strategy-layer patch application |
| `ArchivingLogStructuredStore.Append` | Append patch to log; may trigger archive |
| `ArchivingLogStructuredStore.Archive` | Archive current block to blob, reset store |
| `ProcessMemoryLogStructuredStore.PersistPendingBlock` | Flush in-process log to blob |
| `RedisLogStructuredStore.Append` | Append to Redis stream |
| `RedisLogStructuredStore.PersistPendingBlock` | Flush Redis log to blob |
| `RedisLogStructuredStore.Reset` | Reset Redis store after archive |
| `RedisLogStructuredStore.ValidateReplay` | Replay-vs-snapshot validation on load |

**Detecting which storage provider is active:** If you see
`ProcessMemoryLogStructuredStore.PersistPendingBlock`, the island uses
in-process memory. If you see `RedisLogStructuredStore.*`, it uses Redis.

**Detecting whether a specific model uses log-structured storage:**
`PersistentModelStrategy.ReadSegment` is only emitted by the
log-structured strategy. The standard strategy returns an empty array
without logging. If a model's events include this activity, it is on
the log-structured path.

> **Note:** The `StorageProvider` setting (`ProcessMemory` vs `Redis`) is
> read at DI registration time (process startup), not via `IOptionsMonitor`.
> ECS overrides of `StorageProvider` require a process restart to take
> effect, unlike the `Enable` flag which is hot-reloadable.
