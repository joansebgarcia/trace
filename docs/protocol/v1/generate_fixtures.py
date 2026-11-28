"""Writes the conformance fixtures of the ingest protocol v1.

The JSON files in fixtures/ are the contract; this script only saves writing
the large ones by hand. Edit it, then run from anywhere:

    python3 docs/protocol/v1/generate_fixtures.py

It rewrites every fixture it defines; delete a fixture file by hand when you
remove its definition.
"""
import json
import os

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fixtures")

# DROP removes a field from an Event or a Breadcrumb.
def event(n=1, **fields):
    e = {
        "event_id": "0190b7a4-7c1e-7000-8000-%012d" % n,
        "timestamp": "2026-11-27T10:00:00.000Z",
        "platform": "javascript",
        "sdk": {"name": "trace-js", "version": "0.1.0"},
        "exceptions": [{"type": "TypeError", "message": "x is undefined"}],
    }
    for k, v in fields.items():
        if v is DROP:
            e.pop(k, None)
        else:
            e[k] = v
    return e

DROP = object()

def write(kind, name, description, events=None, body=None, code=None, index=None, beyond_schema=None):
    f = {"description": description}
    if code:
        f["code"] = code
    if index is not None:
        f["index"] = index
    if beyond_schema:
        f["beyond_schema"] = beyond_schema
    f["body"] = body if body is not None else {"events": events}
    with open(os.path.join(OUT, kind, name + ".json"), "w") as fh:
        json.dump(f, fh, indent=2, ensure_ascii=False)
        fh.write("\n")

def valid(name, description, *events):
    write("valid", name, description, list(events))

def invalid(name, description, *events, index=0, **kw):
    write("invalid", name, description, list(events), code="invalid_event", index=index, **kw)

def frame(i, in_app=True):
    return {"function": "handler%d" % i, "filename": "https://app.example.com/assets/app-3f2a1b.js", "lineno": 10 + i, "colno": 5, "in_app": in_app}

def exc(**fields):
    e = {"type": "TypeError", "message": "x is undefined"}
    e.update(fields)
    return e

# --- Minimal and full Events
valid("minimal", "An Event with only the required fields", event())

# --- Required fields and their types
invalid("sdk-missing", "sdk is required", event(sdk=DROP))

# --- Severity and capture
valid("level-and-mechanism", "level, handled and mechanism take each allowed value",
      event(1, level="fatal", handled=False, mechanism="onerror"),
      event(2, level="error", handled=False, mechanism="unhandledrejection"),
      event(3, level="warning", handled=False, mechanism="uncaughtException"),
      event(4, handled=True, mechanism="manual"))
invalid("level-unknown", "level must be fatal, error or warning", event(level="info"))
invalid("handled-wrong-type", "handled must be a boolean", event(handled="yes"))
invalid("mechanism-unknown", "mechanism must be one of the listed values", event(mechanism="console"))

# --- Exceptions and frames
valid("exception-chain", "Up to 5 exceptions, from the outermost to its cause, with frames and raw_stack",
      event(exceptions=[exc(type="Error%d" % i, message="m" * 8192 if i == 0 else "cause %d" % i,
                            frames=[frame(j, in_app=(j % 2 == 0)) for j in range(100)] if i == 0 else [frame(0)],
                            raw_stack="Error%d: cause\n    at handler0 (https://app.example.com/assets/app-3f2a1b.js:10:5)" % i)
                        for i in range(5)]))
valid("frame-partial", "Every frame field is optional; in_app absent means the SDK did not tell",
      event(exceptions=[exc(frames=[{"filename": "app.js"}, {"function": "f", "lineno": 0, "colno": 0}, {}])]))
valid("non-error-values", "Thrown values that are not Errors, and muted cross-origin errors, carry a generic type and no frames",
      event(1, exceptions=[{"type": "Error", "message": "x"}]),
      event(2, exceptions=[{"type": "Error", "message": "Script error."}]),
      event(3, exceptions=[{"type": "Error"}]))
invalid("exceptions-empty", "exceptions must hold at least one exception", event(exceptions=[]))
invalid("exceptions-too-many", "exceptions holds at most 5 exceptions", event(exceptions=[exc() for _ in range(6)]))
invalid("exception-type-missing", "exceptions[].type is required", event(exceptions=[{"message": "x"}]))
invalid("exception-message-too-long", "exceptions[].message holds at most 8192 characters", event(exceptions=[exc(message="m" * 8193)]))
invalid("exception-raw-stack-wrong-type", "exceptions[].raw_stack must be a string", event(exceptions=[exc(raw_stack=["at f"])]))
invalid("frames-too-many", "exceptions[].frames holds at most 100 frames", event(exceptions=[exc(frames=[frame(j) for j in range(101)])]))
invalid("frame-lineno-wrong-type", "frames[].lineno must be an integer", event(exceptions=[exc(frames=[dict(frame(0), lineno="10")])]))
invalid("frame-colno-negative", "frames[].colno must not be negative", event(exceptions=[exc(frames=[dict(frame(0), colno=-1)])]))
invalid("frame-in-app-wrong-type", "frames[].in_app must be a boolean", event(exceptions=[exc(frames=[dict(frame(0), in_app=1)])]))

# --- Context
def big_extra():
    # A serialized extra of exactly 16384 bytes.
    extra = {"payload": ""}
    size = len(json.dumps(extra, separators=(",", ":")))
    extra["payload"] = "p" * (16384 - size)
    return extra

valid("context", "environment, release, tags, contexts, request, user and extra at their limits",
      event(environment="staging", release="checkout@1.4.2",
            tags={"tag%02d" % i: "v" * 200 for i in range(50)},
            contexts={"runtime": {"name": "node", "version": "22.11.0"},
                      "os": {"name": "macOS", "version": "15.1"},
                      "browser": {"name": "Firefox", "version": "133.0"}},
            request={"url": "https://app.example.com/checkout?step=2", "method": "POST"},
            user={"id": "u" * 200},
            extra=big_extra()))
valid("extra-nested", "extra holds any JSON object", event(extra={"cart": {"items": [1, 2.5, "three", None, True]}, "empty": {}}))
invalid("environment-wrong-type", "environment must be a string", event(environment=1))
invalid("release-wrong-type", "release must be a string", event(release={"version": "1.0"}))
invalid("tags-too-many", "tags holds at most 50 tags", event(tags={"tag%02d" % i: "v" for i in range(51)}))
invalid("tag-value-too-long", "a tag value holds at most 200 characters", event(tags={"plan": "v" * 201}))
invalid("tag-value-wrong-type", "a tag value must be a string", event(tags={"retries": 3}))
invalid("contexts-wrong-type", "contexts.os must be an object of strings", event(contexts={"os": "macOS"}))
invalid("request-url-wrong-type", "request.url must be a string", event(request={"url": 42, "method": "GET"}))
invalid("user-id-missing", "user.id is required when user is present", event(user={}))
invalid("user-id-too-long", "user.id holds at most 200 characters", event(user={"id": "u" * 201}))
invalid("extra-not-object", "extra must be a JSON object", event(extra=["a", "b"]))
invalid("extra-too-large", "extra serializes to at most 16384 bytes", event(extra=dict(payload=big_extra()["payload"] + "p")),
        beyond_schema="JSON Schema cannot limit the serialized size of a value")

# --- Breadcrumbs
def crumb(i, **fields):
    c = {"timestamp": "2026-11-27T09:59:%02d.%03dZ" % (i // 1000 % 60, i % 1000), "type": "console", "message": "step %d" % i}
    for k, v in fields.items():
        if v is DROP:
            c.pop(k, None)
        else:
            c[k] = v
    return c

valid("breadcrumbs", "Up to 100 Breadcrumbs, from the oldest to the newest, with free types and data",
      event(breadcrumbs=[crumb(0, type="navigation", data={"from": "/cart", "to": "/checkout"}),
                         crumb(1, type="http", data={"method": "POST", "status": 500}),
                         crumb(2, type="custom", message="m" * 1024)]
                        + [crumb(i) for i in range(3, 100)]))
valid("breadcrumb-minimal", "A Breadcrumb needs only timestamp and type", event(breadcrumbs=[crumb(0, message=DROP)]))
invalid("breadcrumbs-too-many", "breadcrumbs holds at most 100 Breadcrumbs", event(breadcrumbs=[crumb(i) for i in range(101)]))
invalid("breadcrumb-timestamp-missing", "breadcrumbs[].timestamp is required", event(breadcrumbs=[crumb(0, timestamp=DROP)]))
invalid("breadcrumb-timestamp-not-utc", "breadcrumbs[].timestamp must be in UTC", event(breadcrumbs=[crumb(0, timestamp="2026-11-27T10:59:00.000+01:00")]))
invalid("breadcrumb-type-missing", "breadcrumbs[].type is required", event(breadcrumbs=[crumb(0, type=DROP)]))
invalid("breadcrumb-message-too-long", "breadcrumbs[].message holds at most 1024 characters", event(breadcrumbs=[crumb(0, message="m" * 1025)]))
invalid("breadcrumb-data-not-object", "breadcrumbs[].data must be a JSON object", event(breadcrumbs=[crumb(0, data="x")]))

# --- Custom Group Key
valid("group-key", "A Custom Group Key of 1 to 10 strings of up to 200 characters",
      event(1, group_key=["checkout"]),
      event(2, group_key=["k" * 200 for _ in range(10)]))
invalid("group-key-empty", "group_key holds at least 1 string", event(group_key=[]))
invalid("group-key-too-many", "group_key holds at most 10 strings", event(group_key=["k%d" % i for i in range(11)]))
invalid("group-key-entry-empty", "each group_key string holds at least 1 character", event(group_key=[""]))
invalid("group-key-entry-too-long", "each group_key string holds at most 200 characters", event(group_key=["k" * 201]))
invalid("group-key-wrong-type", "group_key must be an array of strings", event(group_key="checkout"))

# --- Required fields, envelope and evolution
valid("timestamp-offset-and-id-case", "timestamp may use a +00:00 offset and event_id may be uppercase",
      event(1, timestamp="2026-11-27T10:00:00.123+00:00"),
      dict(event(2), event_id="0190B7A4-7C1E-7000-8000-000000000002"))
valid("unknown-fields", "Unknown fields are ignored at every level, so v1 grows additively",
      dict(event(exceptions=[exc(frames=[dict(frame(0), module_hint="checkout")], cause_hint=1)],
                 sdk={"name": "trace-js", "version": "0.1.0", "integrations": ["browser"]},
                 user={"id": "42", "segment": "beta"}),
           fingerprint_v2={"any": "shape"}, sampled=True))
valid("batch-full", "A batch holds up to 100 Events", *[event(i) for i in range(1, 101)])
invalid("event-id-missing", "event_id is required", event(event_id=DROP))
invalid("event-id-not-uuid", "event_id must be a UUID", event(event_id="0190b7a4-7c1e-7000-8000"))
invalid("timestamp-missing", "timestamp is required", event(timestamp=DROP))
invalid("timestamp-not-rfc3339", "timestamp must be an RFC 3339 date", event(timestamp="2026-11-27 10:00:00"))
invalid("timestamp-not-utc", "timestamp must be in UTC", event(timestamp="2026-11-27T11:00:00.000+01:00"))
invalid("platform-missing", "platform is required", event(platform=DROP))
invalid("platform-wrong-type", "platform must be a string", event(platform=["javascript"]))
invalid("sdk-version-missing", "sdk.version is required", event(sdk={"name": "trace-js"}))
invalid("required-null", "null is not a value of a required field", event(platform=None))
invalid("event-not-object", "each Event must be a JSON object", "event")
invalid("batch-second-invalid", "One invalid Event rejects the whole batch; index points at it",
        event(1), event(2, exceptions=DROP), index=1)
write("invalid", "batch-empty", "events holds at least 1 Event", [], code="invalid_event")
write("invalid", "batch-too-large", "events holds at most 100 Events", [event(i) for i in range(1, 102)], code="invalid_event")
write("invalid", "body-not-batch", "The body is a JSON object with an events array", body=[event()], code="invalid_event")
write("invalid", "events-missing", "events is required", body={"event": event()}, code="invalid_event")

# --- Review fixes: exact names, timestamps and empty enums
valid("case-variant-fields", "Field names are case-sensitive: a case variant of a known field is an unknown field, so it is ignored",
      dict(event(exceptions=[dict(exc(), Type=7, FRAMES="x")]), Level="bogus", PLATFORM=1, Exceptions="x",
           sdk={"name": "trace-js", "version": "0.1.0", "Name": 1}))
write("valid", "case-variant-envelope", "A case variant of events is ignored next to events",
      body={"events": [event()], "EVENTS": 1})
invalid("timestamp-without-ms", "timestamp must carry milliseconds", event(timestamp="2026-11-27T10:00:00Z"))
invalid("timestamp-unknown-offset", "-00:00 means an unknown offset, not UTC", event(timestamp="2026-11-27T10:00:00.000-00:00"))
invalid("breadcrumb-timestamp-without-ms", "breadcrumbs[].timestamp must carry milliseconds", event(breadcrumbs=[crumb(0, timestamp="2026-11-27T09:59:00Z")]))
invalid("level-empty", "An empty level is not a level", event(level=""))
invalid("mechanism-empty", "An empty mechanism is not a mechanism", event(mechanism=""))
