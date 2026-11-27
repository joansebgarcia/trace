# Research: SDK JS fail-safe — transportes, handlers y tamaño

Ticket: joansebgarcia/trace#6 (hijo del mapa #1). Fecha: 2026-11-24.

## Pregunta

Para un SDK JS ligero que nunca bloquee ni tumbe la app anfitriona: transportes disponibles (`fetch` con `keepalive`, `sendBeacon`, `http`/`https` de Node) y sus límites; handlers globales (`window.onerror`, `unhandledrejection`, `process` `uncaughtException`/`unhandledRejection`) y sus trampas; patrones de timeouts, buffering y flush al cerrar; presupuestos de bundle realistas.

Este documento recoge hechos de fuentes primarias (WHATWG Fetch, WHATWG HTML, W3C Beacon, MDN, docs de Node.js, V8). No toma la decisión.

## 1. Transportes en el navegador

### 1.1 `fetch()` con `keepalive: true`

- **Para qué sirve**: con `keepalive`, "the browser will not abort the associated request if the page that initiated it is unloaded before the request is complete". A diferencia de `sendBeacon`, permite otros métodos, headers propios y leer la respuesta. [MDN, RequestInit](https://developer.mozilla.org/en-US/docs/Web/API/RequestInit)
- **Lo que dice la spec**: al terminar un fetch group, solo se cancelan los fetches pendientes cuyo `keepalive` es `false`. Los que tienen `keepalive` sobreviven al documento. [Fetch, §2.4 Fetch groups](https://fetch.spec.whatwg.org/#fetch-groups)
- **Límite de 64 KiB, compartido**: en HTTP-network-or-cache fetch, si la request tiene `keepalive`, la spec suma el `body` de **todas** las requests `keepalive` en vuelo del mismo fetch group. "If the sum of contentLength and inflightKeepaliveBytes is greater than 64 kibibytes, then return a network error." No es un tope por request: el presupuesto lo comparte todo el documento, incluido el código de la app anfitriona y de terceros. [Fetch, §4.6 HTTP-network-or-cache fetch](https://fetch.spec.whatwg.org/#http-network-or-cache-fetch)
- **No acepta streams**: si el body es un `ReadableStream` y `keepalive` es `true`, extraer el body "throw a TypeError". [Fetch, §5 BodyInit extract](https://fetch.spec.whatwg.org/#concept-bodyinit-extract)
- **Cancelación y timeout**: `signal` (`AbortSignal`) permite abortar la request. [MDN, RequestInit](https://developer.mozilla.org/en-US/docs/Web/API/RequestInit) `AbortSignal.timeout(ms)` existe también en Node (desde v17.3.0). [Node.js, Globals](https://nodejs.org/api/globals.html#static-method-abortsignaltimeoutdelay)
- **Prioridad**: `priority: "low" | "high" | "auto"` (por defecto `auto`) indica la prioridad relativa a otras requests del mismo tipo. [MDN, RequestInit](https://developer.mozilla.org/en-US/docs/Web/API/RequestInit)

### 1.2 `navigator.sendBeacon(url, data)`

- **Semántica**: siempre `POST`. Devuelve `true` si el user agent pudo **encolar** los datos y `false` si no. Que devuelva `true` no confirma la entrega. No da acceso a la respuesta. [MDN, sendBeacon](https://developer.mozilla.org/en-US/docs/Web/API/Navigator/sendBeacon)
- **Mismo cupo que `keepalive`**: la spec hace la request con `keepalive` `true`, y si los datos superan lo que admite el cupo de requests `keepalive`, la llamada devuelve `false`. "All requests with this flag set share the same in-flight quota restrictions that is enforced within the Fetch API." MDN lo da como 64 KiB. [W3C Beacon, §3 Processing Model](https://w3c.github.io/beacon/#sec-processing-model); [MDN, sendBeacon](https://developer.mozilla.org/en-US/docs/Web/API/Navigator/sendBeacon)
- **Sin headers propios**: la `header list` solo lleva `Content-Type`, sacado del tipo de `data`. No se puede mandar un header de autenticación, así que la Ingest Key tendría que ir en la URL o en el body. [W3C Beacon, §3](https://w3c.github.io/beacon/#sec-processing-model)
- **CORS y credenciales**: `credentials mode` siempre es `include`. Si el `Content-Type` es CORS-safelisted, el modo es `no-cors`. Si no lo es (p. ej. `application/json`), el modo es `cors` y "a CORS preflight is made". [W3C Beacon, §3–4](https://w3c.github.io/beacon/#sec-processing-model)
  - Los únicos MIME CORS-safelisted para `Content-Type` son `application/x-www-form-urlencoded`, `multipart/form-data` y `text/plain`, y el valor no puede pasar de 128 bytes. [Fetch, CORS-safelisted request-header](https://fetch.spec.whatwg.org/#cors-safelisted-request-header)
  - En el CORS check, el wildcard `Access-Control-Allow-Origin: *` solo vale "if request's credentials mode is not \"include\"". Por tanto, un beacon en modo `cors` necesita que el servidor devuelva el origin exacto y `Access-Control-Allow-Credentials`. [Fetch, CORS check](https://fetch.spec.whatwg.org/#concept-cors-check)
  - Consecuencia directa: mandar el body como `text/plain` (aunque el contenido sea JSON) evita el preflight, tanto con `sendBeacon` como con `fetch`.

### 1.3 `fetchLater()` (deferred fetch)

- Crea un fetch diferido que se envía al salir de la página (se destruye o entra en bfcache) o tras `activateAfter`, lo que ocurra antes. La respuesta se ignora. [MDN, fetchLater](https://developer.mozilla.org/en-US/docs/Web/API/Window/fetchLater)
- Está marcado **Experimental, no Baseline**: "does not work in some of the most widely-used browsers". Lanza `QuotaExceededError`, y MDN recomienda capturarlo "in almost all cases, especially if they are embedding third-party JavaScript". [MDN, fetchLater](https://developer.mozilla.org/en-US/docs/Web/API/Window/fetchLater)
- Cupo: 640 KiB por top-level traversable, con un máximo de 64 KiB a la vez por origin de destino. Un iframe cross-origin recibe 8 KiB por defecto. [Fetch, §4.12.1 Deferred fetching quota](https://fetch.spec.whatwg.org/#deferred-fetch-quota)

### 1.4 Cuándo hacer flush al cerrar (navegador)

- Pasar a `hidden` "is the last event that's reliably observable by the page", y MDN recomienda tratarlo como el fin probable de la sesión. [MDN, visibilitychange](https://developer.mozilla.org/en-US/docs/Web/API/Document/visibilitychange_event)
- `unload`, `beforeunload` y `pagehide` son poco fiables ("especially on mobile, the browser will not fire these events"). Además, `unload` es incompatible con bfcache. `pagehide` sí es compatible con bfcache, pero "suffers from the same reliability problems". [MDN, sendBeacon](https://developer.mozilla.org/en-US/docs/Web/API/Navigator/sendBeacon)

## 2. Transportes en Node.js

- **`fetch` global**: estable desde v21 (sin flag desde v18). Está basado en undici, y `process.versions.undici` indica la versión. [Node.js, Globals — fetch](https://nodejs.org/api/globals.html#fetch)
- **`http.request`, errores**: cualquier error (DNS, TCP, parse) emite `'error'` en la request, y "if no listeners are registered the error will be thrown". [Node.js, http.request](https://nodejs.org/api/http.html#httprequestoptions-callback) En general, un `EventEmitter` que emite `'error'` sin listener lanza la excepción "and the Node.js process exits". [Node.js, Events — Error events](https://nodejs.org/api/events.html#error-events) Si un SDK olvida el listener de `'error'`, puede tumbar la app.
- **Consumir la respuesta**: si hay handler de `'response'`, hay que consumir el body (`read()`, `'data'` o `.resume()`). Si no, no llega `'end'` y "it will consume memory that can eventually lead to a 'process out of memory' error". [Node.js, http.ClientRequest](https://nodejs.org/api/http.html#class-httpclientrequest)
- **Timeouts**: la opción `timeout` de `http.request` y `request.setTimeout()` son timeouts de **inactividad del socket**. El evento `'timeout'` "only notifies that the socket has been idle. The request must be destroyed manually." [Node.js, http — Event 'timeout'](https://nodejs.org/api/http.html#event-timeout) No hay timeout total incorporado: hay que usar un `AbortSignal`/timer propio.
- **No retener el proceso**: `timeout.unref()` hace que el timer no mantenga vivo el event loop ("the process may exit before the Timeout object's callback is invoked"). [Node.js, Timers](https://nodejs.org/api/timers.html#timeoutunref) Con `keepAlive`, los sockets libres del `Agent` quedan unref por defecto. [Node.js, http.Agent](https://nodejs.org/api/http.html#class-httpagent)

## 3. Handlers globales y sus trampas

### 3.1 Navegador: `error`

- Se dispara en `window` por errores de script **síncronos** (carga inicial, event handlers). Las promesas rechazadas sin handler disparan `unhandledrejection`. [MDN, Window error event](https://developer.mozilla.org/en-US/docs/Web/API/Window/error_event)
- `onerror` recibe 5 argumentos `(message, source, lineno, colno, error)`. `addEventListener('error')` recibe un `ErrorEvent`. Solo devolver `true` desde `onerror` cancela el default (que no aparezca en consola), y aun así "the current script will still stop executing". [MDN, Window error event](https://developer.mozilla.org/en-US/docs/Web/API/Window/error_event)
- Asignar `window.onerror` / `onunhandledrejection` **reemplaza** el handler que ya tuviera la app. `addEventListener` no lo hace. [MDN, unhandledrejection](https://developer.mozilla.org/en-US/docs/Web/API/Window/unhandledrejection_event)
- **Errores silenciados cross-origin**: si el script es clásico y tiene `muted errors`, el `ErrorEvent` llega con `error = null`, `message = "Script error."`, `filename` vacío y `lineno`/`colno` en 0. Un script tiene `muted errors` cuando su respuesta es CORS-cross-origin. [HTML, §8.1.4.6 Runtime script errors](https://html.spec.whatwg.org/multipage/webappapis.html#report-an-exception)
- **Reentrada**: el global tiene un flag `in error reporting mode`. Mientras está activo no se vuelve a disparar `error`, así que un error lanzado dentro del handler no provoca recursión infinita. [HTML, report an exception](https://html.spec.whatwg.org/multipage/webappapis.html#report-an-exception)

### 3.2 Navegador: `unhandledrejection`

- Es un `PromiseRejectionEvent` con `promise` y `reason`, `cancelable`. Se dispara en una task posterior, y solo si la promesa sigue sin handler (`[[PromiseIsHandled]]` false). Si no se cancela, el UA "may report" la razón en consola. [HTML, §8.1.4.7 Unhandled promise rejections](https://html.spec.whatwg.org/multipage/webappapis.html#unhandled-promise-rejections)
- `reason` puede ser cualquier valor, no necesariamente un `Error`. [HTML, PromiseRejectionEvent](https://html.spec.whatwg.org/multipage/webappapis.html#promiserejectionevent)
- Los rechazos de scripts cross-origin no disparan el evento. [MDN, unhandledrejection](https://developer.mozilla.org/en-US/docs/Web/API/Window/unhandledrejection_event)

### 3.3 Node: `uncaughtException`, `uncaughtExceptionMonitor`, `unhandledRejection`

- **Instalar un listener cambia el comportamiento de la app**. Por defecto Node imprime el stack y sale con código 1. "Adding a handler for the 'uncaughtException' event overrides this default behavior", y si el handler no fija `process.exitCode`, "the process will exit with 0". [Node.js, process — 'uncaughtException'](https://nodejs.org/api/process.html#event-uncaughtexception)
- "It is not safe to resume normal operation after 'uncaughtException'". Su uso correcto es "synchronous cleanup … before shutting down". Una excepción lanzada dentro del handler no se captura y el proceso sale con código distinto de 0. [Node.js, process](https://nodejs.org/api/process.html#warning-using-uncaughtexception-correctly)
- **`uncaughtExceptionMonitor`** observa sin alterar el comportamiento: "The process will still crash if no 'uncaughtException' listener is installed". Se emite antes que `uncaughtException`. [Node.js, process — 'uncaughtExceptionMonitor'](https://nodejs.org/api/process.html#event-uncaughtexceptionmonitor)
  - Tensión (deducción, no la dice la fuente): con el monitor el proceso termina justo después, así que no hay tiempo para un envío por red asíncrono. Las opciones son persistir de forma síncrona (p. ej. `fs.writeSync`) o instalar `uncaughtException`, hacer el flush y salir con código 1, reimplementando el default.
- **`unhandledRejection`**: se emite si una promesa se rechaza sin handler "within a turn of the event loop". Con el modo por defecto `throw` (desde v15), si nadie escucha `unhandledRejection` el rechazo se relanza como excepción no capturada. Si el SDK **instala** un listener, la app deja de caerse por rechazos sin manejar. Es otro cambio de comportamiento. [Node.js, process — 'unhandledRejection'](https://nodejs.org/api/process.html#event-unhandledrejection); [Node.js, CLI `--unhandled-rejections`](https://nodejs.org/api/cli.html#--unhandled-rejectionsmode)
  - Modos: `throw` (default), `strict`, `warn`, `warn-with-error-code` y `none`. Un rechazo durante la carga estática de ES modules del entry point siempre se lanza como excepción no capturada. [Node.js, CLI](https://nodejs.org/api/cli.html#--unhandled-rejectionsmode)

### 3.4 Node: flush al cerrar

- `'exit'`: los listeners "must only perform synchronous operations". Después el proceso sale y se abandona todo el trabajo pendiente. [Node.js, process — 'exit'](https://nodejs.org/api/process.html#event-exit)
- `'beforeExit'`: permite trabajo asíncrono, pero "is not emitted for conditions causing explicit termination, such as calling process.exit() or uncaught exceptions". [Node.js, process — 'beforeExit'](https://nodejs.org/api/process.html#event-beforeexit)
- `process.exit()` fuerza la salida "even if there are still asynchronous operations pending". [Node.js, process.exit](https://nodejs.org/api/process.html#processexitcode)
- `SIGINT`/`SIGTERM`: "If one of these signals has a listener installed, its default behavior will be removed". Si el SDK escucha señales, pasa a ser responsable de terminar el proceso. `SIGKILL` no admite listener. [Node.js, process — Signal events](https://nodejs.org/api/process.html#signal-events)
- Consecuencia: en Node no hay un hook universal y asíncrono de "al cerrar". Un `flush(timeoutMs)` explícito que llama la app (antes de su propio `exit`) es el único punto que cubre todos los casos.

## 4. Robustez del propio SDK

- `JSON.stringify` lanza `TypeError` con referencias circulares y con `BigInt`. [MDN, JSON.stringify](https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/JSON/stringify) Por eso serializar contexto o `extra` del usuario tiene que ir dentro de `try/catch`.
- `Error.prototype.stack` "is not standardized". El formato difiere entre V8, SpiderMonkey y JavaScriptCore (ejemplos en la fuente). El parser de stack en servidor (o en el SDK) tiene que tolerar los tres. [MDN, Error.stack](https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/Error/stack)
- En V8 el stack se captura al **crear** el `Error` y por defecto tiene 10 frames (`Error.stackTraceLimit`). `Error.prepareStackTrace` y `captureStackTrace` son exclusivos de V8. [V8, Stack trace API](https://v8.dev/docs/stack-trace-api)

## 5. Presupuesto de bundle (medición propia)

No hay una fuente primaria que fije un "presupuesto razonable" para un SDK de este tipo. Lo siguiente es una medición local, no una cita:

- Boceto mínimo de navegador (handlers `error`/`unhandledrejection`, cola en memoria acotada a 30 Events, flush con debounce de 2 s, `sendBeacon` en `visibilitychange`/`pagehide`, `fetch` con `keepalive` + timeout de 5 s por `AbortController`, normalización y truncado del error, todo dentro de `try/catch`): **1.4 KB min / 0.77 KB gzip** (esbuild 0.24.2, `--minify --format=esm`, `gzip -9`).
- Referencia: `web-vitals` 4.2.4 entero (librería ligera de telemetría de navegador) mide **7.2 KB min / 2.6 KB gzip** con el mismo pipeline.
- Lectura: el núcleo de transporte + handlers cabe en ~1 KB gzip. El presupuesto lo deciden las funcionalidades añadidas: Breadcrumbs (wrapping de `console`/`fetch`/`history`), scopes/contexto, reintentos persistentes y parsing de stack en cliente. Un objetivo de pocos KB gzip para el bundle de navegador es realista. El de Node no afecta a la carga de páginas.

## Hechos clave para la decisión (resumen)

1. En navegador, los envíos `keepalive` y `sendBeacon` comparten un cupo de **64 KiB en vuelo por documento**, también con el código de la app anfitriona. Hay que trocear lotes y tener fallback a `fetch` normal.
2. Con un `Content-Type` `text/plain` no hay preflight. `sendBeacon` no permite headers, así que la autenticación va en URL o body. Además usa `credentials: include`, y por eso en modo `cors` el servidor no puede responder con `*`.
3. El flush del navegador se hace en `visibilitychange → hidden`. `unload` no es fiable.
4. En Node, cualquier listener de `uncaughtException`, `unhandledRejection` o señales **cambia la semántica de salida de la app**. `uncaughtExceptionMonitor` no la cambia, pero no deja tiempo para enviar por red.
5. En Node, `'exit'` es síncrono y `beforeExit` no cubre `process.exit()` ni crashes. Hace falta un `flush()` explícito con timeout y timers `unref()`.
6. En `http.request` siempre hace falta un listener de `'error'`, consumir la respuesta y un timeout total propio, porque el `timeout` nativo es solo de inactividad.
