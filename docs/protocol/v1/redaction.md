# Redaction rules v1

This is the normative list of the values a Trace server replaces with `"[redacted]"` before an Event is stored. SDKs SHOULD apply the same rules before sending, but only the server is a guarantee: it applies them to every Event it accepts, after validation and before the Event reaches disk, and before it computes the Group Key. [`fixtures/redaction/`](fixtures/redaction/) holds Events with the Event they are stored as; the server's tests and the SDKs' tests both load them.

The marker is always the string `[redacted]`, with no hash, length or other trace of the value. The server never rejects an Event because it holds sensitive data. Adding a term or a pattern is an additive change within v1.

## By key name

A key is sensitive when, lowercased and without `-` or `_`, it **contains** one of these terms:

`password`, `passwd`, `pwd`, `secret`, `token`, `apikey`, `auth`, `authorization`, `cookie`, `session`, `csrf`, `xsrf`, `credential`, `privatekey`, `card`, `cvv`, `ssn`

So `X-Api-Key`, `user_password` and `Session-Id` are sensitive. False positives, such as `author`, are accepted.

The value of a sensitive key is replaced whole, whatever its type: a nested object or array becomes the string `"[redacted]"`. The rule applies to the keys of:

- `extra`, at any depth, including objects inside arrays;
- `contexts.runtime`, `contexts.os` and `contexts.browser`;
- `tags`;
- `data` of each Breadcrumb, at any depth;
- the query parameters of URLs (see below).

A Project MAY add terms of its own, matched the same way. They only add to the default terms, which cannot be turned off, and they apply to the Events accepted from then on: Events already stored stay as they are.

## By pattern

These fragments are replaced, keeping the rest of the string:

| Pattern | Example | Becomes |
| --- | --- | --- |
| A card number: 13 to 19 digits, optionally grouped by spaces or dashes, that pass the Luhn check | `card 4111 1111 1111 1111` | `card [redacted]` |
| A Bearer token | `Bearer abc.def-123` | `Bearer [redacted]` |
| A JWT: three base64url parts, the first starting with `eyJ` | `eyJhbGciOi….eyJzdWIi….sig` | `[redacted]` |
| The userinfo of a URL with a password | `https://ann:pa55@host/` | `https://[redacted]@host/` |

The patterns apply to every exception `message` and `raw_stack`, every Breadcrumb `message`, every string in `extra`, and every URL.

## URLs

A URL is `request.url`, or a string in `extra` or in Breadcrumb `data` that starts with a scheme, such as `https://`. In a URL, the patterns apply, and the value of each query parameter whose name is sensitive is replaced: `?token=abc&page=2` becomes `?token=[redacted]&page=2`. The rest of the URL, the fragment included, stays byte for byte. Strings of Breadcrumb `data` that are not URLs only follow the key rule.

## Never redacted

`type`, the frames (`function`, `filename`, `lineno`, `colno`, `in_app`), `event_id`, `timestamp`, `platform`, `sdk`, `level`, `environment`, `release`, `user` and the Custom Group Key are stored as sent. Emails and IP addresses are not redacted by default. `user.id` is opaque and stored as sent, so the host application SHOULD NOT use emails as ids.

## What the server does not keep

The server does not store the IP the Events come from, and its logs never hold the body of an Event: at most its `event_id`, its Project and an error code.

## Fixtures

Each file of [`fixtures/redaction/`](fixtures/redaction/) is:

```json
{
  "description": "What the fixture shows",
  "terms": ["order-ref"],
  "input": {"event_id": "…"},
  "output": {"event_id": "…"}
}
```

- `input` is one Event to send, and `output` the Event as it must be stored, before the server fills its defaults (`level`, `environment`).
- `terms`, if present, are the extra key terms of the Project.
- Key order inside objects does not count.
