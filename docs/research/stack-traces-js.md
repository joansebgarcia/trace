# Formatos de stack trace JS y normalización

Ticket: joansebgarcia/trace#5 (mapa #1). Fecha: 2026-11-24.

**Pregunta.** ¿Cómo se ven los stack traces de V8 (Node, Chrome), SpiderMonkey (Firefox) y JavaScriptCore (Safari), incluyendo async frames, `eval` y código minificado? ¿Qué librerías o técnicas existen para parsearlos a frames estructurados (`file`, `function`, `line`, `column`) y cómo distinguir frames in-app de `node_modules`/vendors?

Este documento recoge hechos, no la decisión. Cada afirmación cita su fuente. Las marcadas **[empírico]** se capturaron localmente con Node v24.14.1 (V8) y el binario `jsc` de JavaScriptCore incluido en macOS (Darwin 25.6); el script usado se reproduce en el Apéndice A.

---

## 1. Estado de estandarización

- `Error.prototype.stack` **no es estándar**: "Not part of any standard". En Firefox es un accessor en `Error.prototype`; en Chrome y Safari es una data property propia de cada instancia (writable, non-enumerable, configurable). "You cannot rely on the precise content of the stack string due to implementation inconsistencies." — [MDN `Error.prototype.stack`](https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/Error/stack)
- Propuestas de TC39 en curso ([tc39/proposals README](https://github.com/tc39/proposals/blob/main/README.md), [stage 1](https://github.com/tc39/proposals/blob/main/stage-1-proposals.md)):
  - [Error Stack Accessor](https://github.com/tc39/proposal-error-stack-accessor): **Stage 3**. Estandariza solo la existencia del accessor, no el formato del string.
  - [`Error.captureStackTrace`](https://github.com/tc39/proposal-error-capturestacktrace): **Stage 2**.
  - [Error stacks](https://github.com/tc39/proposal-error-stacks) (estructura/formato): **Stage 1**.
- Consecuencia: hoy **no existe** un formato normativo. Cualquier parser se basa en el comportamiento observado de cada engine.

## 2. Límites de profundidad (`Error.stackTraceLimit`)

| Engine | Default | Fuente |
|---|---|---|
| V8 | 10 frames (configurable por contexto, `0` desactiva, `Infinity` permitido; flag `--stack-trace-limit`) | [V8 Stack trace API](https://v8.dev/docs/stack-trace-api); **[empírico]** `Error.stackTraceLimit === 10` en Node 24 |
| SpiderMonkey | `MAX_REPORTED_STACK_DEPTH = 1u << 7` (128); `Error.stackTraceLimit` existe y se acota a ese máximo | [`SavedStacks.h`](https://github.com/mozilla-firefox/firefox/blob/main/js/src/vm/SavedStacks.h), [`ErrorObject.cpp`](https://github.com/mozilla-firefox/firefox/blob/main/js/src/vm/ErrorObject.cpp) (`stackTraceLimit`, `captureStackTrace`) |
| JavaScriptCore | `defaultErrorStackTraceLimit = 100` | [`OptionsList.h`](https://github.com/WebKit/WebKit/blob/main/Source/JavaScriptCore/runtime/OptionsList.h) |

Implicación: en Node, con el default, los frames de bootstrap de `node:internal` consumen buena parte de los 10 frames (ver §3.1).

---

## 3. Formatos por engine

### 3.1 V8 (Node, Chrome, Edge, Deno)

**Gramática** (Apéndice de [V8 Stack trace API](https://v8.dev/docs/stack-trace-api)):

- Primera línea: `${error.name}: ${error.message}` (header, no es un frame).
- Cada frame: 4 espacios + `at ` + una de estas formas:
  - `at Type.functionName [as methodName] (location)`
  - `at new functionName (location)` (llamada a constructor)
  - `at async functionName (location)` (async frame)
  - `at Type.name (location)` si solo hay uno de los dos nombres o coinciden
  - `at functionName [as methodName] (location)` si el receiver es el global object
  - Sin nombre: se usa `<anonymous>`; en la práctica, si no hay nombre se omiten los paréntesis: `at location` **[empírico]**.
- `location`:
  - `fileName:lineNumber:columnNumber` (1-based)
  - `eval at position`, anidable: `eval at Foo.a (eval at Bar.z (myscript.js:10:3))`
  - `native` (internos de V8)
  - `unknown location`
- API estructurada: `Error.prepareStackTrace(error, callSites)` recibe `CallSite[]` con `getTypeName`, `getFunctionName`, `getMethodName`, `getFileName`, `getLineNumber`, `getColumnNumber`, `getEvalOrigin`, `isToplevel`, `isEval`, `isNative`, `isConstructor`, `isAsync`, `isPromiseAll`, `getPromiseIndex`, etc. Se invoca de forma lazy al primer acceso a `.stack`. `Error.captureStackTrace(obj, constructorOpt)` añade `.stack` a cualquier objeto y oculta frames por encima de `constructorOpt`. — [V8 Stack trace API](https://v8.dev/docs/stack-trace-api)

**Ejemplos [empírico], Node v24.14.1, CommonJS:**

```
Error: sync
    at baz (/private/tmp/rs5/scripts/sample.js:4:24)
    at bar (/private/tmp/rs5/scripts/sample.js:3:18)
    at foo (/private/tmp/rs5/scripts/sample.js:2:18)
    at Object.<anonymous> (/private/tmp/rs5/scripts/sample.js:5:7)
    at Module._compile (node:internal/modules/cjs/loader:1812:14)
    at Object..js (node:internal/modules/cjs/loader:1943:10)
    at Module.load (node:internal/modules/cjs/loader:1533:32)
    at Module._load (node:internal/modules/cjs/loader:1335:12)
    at wrapModuleLoad (node:internal/modules/cjs/loader:255:19)
    at Module.executeUserEntryPoint [as runMain] (node:internal/modules/run_main:154:5)
```

Constructor, función anónima y frame nativo:

```
Error: ctor
    at new Widget (/private/tmp/rs5/scripts/sample.js:6:38)
    ...
Error: anon cb
    at /private/tmp/rs5/scripts/sample.js:11:35
    at Array.map (<anonymous>)
    at Object.<anonymous> (/private/tmp/rs5/scripts/sample.js:11:11)
```

`eval` y `new Function` (la location del frame interno es relativa al código evaluado, `<anonymous>:line:col`; la posición de la llamada a `eval` va dentro de `eval at ...`):

```
Error: eval
    at inEval (eval at <anonymous> (/private/tmp/rs5/scripts/sample.js:12:7), <anonymous>:1:26)
    at eval (eval at <anonymous> (/private/tmp/rs5/scripts/sample.js:12:7), <anonymous>:1:47)
    at Object.<anonymous> (/private/tmp/rs5/scripts/sample.js:12:7)
Error: Function ctor
    at eval (eval at <anonymous> (/private/tmp/rs5/scripts/sample.js:13:7), <anonymous>:3:7)
    at Object.<anonymous> (/private/tmp/rs5/scripts/sample.js:13:55)
```

Nota: con `new Function(body)` la línea interna es `3` para código escrito en la línea 1 del body; V8 envuelve el body en `function anonymous(\n) {\n...` y la numeración incluye ese wrapper **[empírico]**.

ESM (las locations son URLs `file://`; los frames de nivel superior del módulo no tienen nombre):

```
Error: esm
    at f (file:///private/tmp/rs5/scripts/esm.mjs:3:22)
    at file:///private/tmp/rs5/scripts/esm.mjs:4:7
    at ModuleJob.run (node:internal/modules/esm/module_job:430:25)
    at async onImport.tracePromise.__proto__ (node:internal/modules/esm/loader:661:26)
    at async asyncRunEntryPointWithESMLoader (node:internal/modules/run_main:101:5)
```

En Chrome las locations son URLs `http(s)://`, `blob:`, `chrome-extension://`, etc. (el regex de `stacktrace-parser` enumera `file|https?|blob|chrome-extension|native|eval|webpack|rsc|<anonymous>`; ver §5).

**Async frames.** "Zero-cost async stack traces" activados por defecto desde V8 v7.3 (`--async-stack-traces`). Solo cubren `await`, `Promise.all()` y `Promise.any()`; con `Promise#then()`/`catch()` la cadena del caller se pierde porque "for `await` the resume and suspend locations are the same", lo que no ocurre con `then`. — [V8 Stack trace API](https://v8.dev/docs/stack-trace-api), [V8 blog: Faster async functions and promises](https://v8.dev/blog/fast-async)

```
Error: async
    at a2 (/private/tmp/rs5/scripts/sample.js:15:41)
    at process.processTicksAndRejections (node:internal/process/task_queues:104:5)
    at async a1 (/private/tmp/rs5/scripts/sample.js:14:23)

Error: promise.all
    at p2 (/private/tmp/rs5/scripts/sample.js:18:41)
    at process.processTicksAndRejections (node:internal/process/task_queues:104:5)
    at async Promise.all (index 0)
    at async p1 (/private/tmp/rs5/scripts/sample.js:17:23)

Error: then chain
    at t2 (/private/tmp/rs5/scripts/sample.js:20:69)
    at process.processTicksAndRejections (node:internal/process/task_queues:104:5)
```

`at async Promise.all (index 0)` es un frame **sin file/line/column** (la "location" es el índice del elemento) **[empírico]**; corresponde a `CallSite.isPromiseAll()` / `getPromiseIndex()`.

**APIs Node para frames estructurados sin parsear strings:**

- `Error.prepareStackTrace` + `CallSite` (ver arriba). **[empírico]** devuelve, p. ej., `{"fn":"g","file":"file:///.../esm.mjs","line":11,"col":23,"native":false,"eval":false,"ctor":false,"async":false}`.
- `util.getCallSites([frameCount][, options])`: added v22.9.0, Stability 1.1 (Active development), devuelve `functionName`, `scriptName`, `scriptId`, `lineNumber`, `columnNumber` (1-based); `options.sourceMap` reconstruye la posición original. Captura el stack **actual**, no el de un `Error` ya lanzado. — [Node.js `util`](https://nodejs.org/api/util.html)
- `--enable-source-maps` (added v12.12.0, estable desde v15.11.0/v14.18.0) reescribe `Error.stack` a posiciones originales; sobrescribir `Error.prepareStackTrace` puede impedirlo salvo que se delegue al original; tiene coste de latencia al acceder a `.stack`. — [Node.js CLI](https://nodejs.org/api/cli.html)

### 3.2 SpiderMonkey (Firefox)

**Gramática**, tomada del código que construye el string ([`js/src/vm/SavedStacks.cpp`](https://github.com/mozilla-firefox/firefox/blob/main/js/src/vm/SavedStacks.cpp), `FormatSpiderMonkeyStackFrame`):

```
[asyncCause "*"] [functionDisplayName] "@" source ":" line ":" column "\n"
```

- **Sin header**: el string no incluye `name: message` (a diferencia de V8).
- Función sin nombre: la línea empieza por `@`.
- `column` es 1-based (`getColumn().oneOriginValue()`).
- Wasm: `line` = `wasm-function[N]`, `column` = `0x<offset hex>`.
- El mismo archivo contiene un modo `StackFormat::V8` que emula el formato de V8; el default es `StackFormat::SpiderMonkey`.
- `eval` / `Function`: el `source` se compone como `<url del introductor> line <N> > eval` o `... > Function` (separadores `" line "` y `" > "` en [`JSScript.cpp`](https://github.com/mozilla-firefox/firefox/blob/main/js/src/vm/JSScript.cpp)); se puede nombrar con `//# sourceURL`. — [MDN](https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/Error/stack)

**Ejemplos** (de [MDN](https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/Error/stack); no se dispuso de un binario SpiderMonkey local):

```
baz@filename.js:10:15
bar@filename.js:6:3
foo@filename.js:2:3
@filename.js:13:1
```

```
anonymous@filename.js line 1 > Function:1:8
@filename.js:1:65

@filename.js line 3 > eval:1:1
@filename.js:3:13
```

**Async frames.** Cuando existen, el nombre del frame lleva como prefijo la `asyncCause` seguida de `*` (p. ej. `async*name@...`; si se saltaron frames async sin causa, se usa la causa `Async`). `asyncCause` es "a string representing the type of asynchronous call" (ej. `"Promise"`). — [`SavedStacks.cpp`](https://github.com/mozilla-firefox/firefox/blob/main/js/src/vm/SavedStacks.cpp), [SavedFrame docs](https://firefox-source-docs.mozilla.org/js/SavedFrame/index.html)

Ojo: por defecto Firefox **solo captura async stacks cuando DevTools está abierto**:

```js
pref("javascript.options.asyncstack", true);
// Broadly capturing async stack data adds overhead that is only advisable for
// developers, so we only enable it when the devtools are open, by default.
pref("javascript.options.asyncstack_capture_debuggee_only", true);
```

— [`modules/libpref/init/all.js`](https://github.com/mozilla-firefox/firefox/blob/main/modules/libpref/init/all.js), [Bug 1601179](https://bugzilla.mozilla.org/show_bug.cgi?id=1601179) (Firefox 79). En producción, un usuario final normalmente obtendrá solo el tramo síncrono.

### 3.3 JavaScriptCore (Safari, WebKit, Bun)

**Gramática**, del código que construye cada línea ([`Source/JavaScriptCore/runtime/StackFrame.cpp`](https://github.com/WebKit/WebKit/blob/main/Source/JavaScriptCore/runtime/StackFrame.cpp), `StackFrame::appendTo`):

```
functionName "@" sourceURL [":" line ":" column]
```

- **Sin header** `name: message`.
- `functionName` especiales: `global code`, `eval code`, `module code`; frames nativos tienen `sourceURL = [native code]` y no llevan line/column; si no hay sourceURL ni line/column se emite `name@` (p. ej. `eval code@`, `anonymous@`).
- Async frames: `"async " + name` (solo con `useAsyncStackTrace`, ver abajo).
- Wasm: `wasm-function[N]` o `<module>:wasm-function[N]`.
- Las columnas apuntan a otra posición que en V8 para el mismo `throw new Error(...)` (p. ej. V8 `4:24` vs JSC `4:33`) **[empírico]**; MDN muestra la misma diferencia (`10:15` vs `10:24`). Las columnas **no son comparables entre engines**.

**Ejemplos [empírico], `jsc` de macOS (mismo script que V8):**

```
baz@/tmp/rs5/scripts/sample.js:4:33
bar@/tmp/rs5/scripts/sample.js:3:21
foo@/tmp/rs5/scripts/sample.js:2:21
global code@/tmp/rs5/scripts/sample.js:5:10

Widget@/tmp/rs5/scripts/sample.js:6:47          <- constructor: sin "new"
global code@/tmp/rs5/scripts/sample.js:7:17

@/tmp/rs5/scripts/sample.js:11:44               <- función anónima
map@[native code]
global code@/tmp/rs5/scripts/sample.js:11:14

inEval@                                         <- eval: sin location
eval code@
eval@[native code]
global code@/tmp/rs5/scripts/sample.js:12:11

anonymous@                                      <- new Function
global code@/tmp/rs5/scripts/sample.js:13:55
```

Ejemplo de MDN para Safari: `baz@filename.js:10:24 … global code@filename.js:13:4`. — [MDN](https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/Error/stack)

**Async frames.** Históricamente JSC cortaba el stack en el último `await` (`two@./test.js:6:20`). Cronología en WebKit:

- 2025-09: async stack traces detrás de `--useAsyncStackTrace` ([f8a073dfbe](https://github.com/WebKit/WebKit/commit/f8a073dfbe), bug [296261](https://bugs.webkit.org/show_bug.cgi?id=296261)) y prefijo `async ` ([5dd8842ba4](https://github.com/WebKit/WebKit/commit/5dd8842ba4), bug [298508](https://bugs.webkit.org/show_bug.cgi?id=298508)); luego soporte de `Promise.race`, `Promise.any`, `Promise.allSettled`.
- 2026-02-04: **activado por defecto** (`useAsyncStackTrace` = `true`), "[JSC] Enable AsyncStackTrace" ([10d5c981ca](https://github.com/WebKit/WebKit/commit/10d5c981ca), 306759@main, bug [306918](https://bugs.webkit.org/show_bug.cgi?id=306918)).
- No se verificó en qué versión de Safari se publicó. El `jsc` de macOS probado lo trae desactivado por defecto: sin flag `a2@...:15:50` a secas; con `--useAsyncStackTrace=1` **[empírico]**:

```
a2@/tmp/rs5/scripts/sample.js:15:50
async a1@/tmp/rs5/scripts/sample.js:14:31

p2@/tmp/rs5/scripts/sample.js:18:50
async p1@/tmp/rs5/scripts/sample.js:17:40
```

(A diferencia de V8, no aparece un frame `Promise.all (index N)`.)

### 3.4 Tabla comparativa

| Aspecto | V8 | SpiderMonkey | JavaScriptCore |
|---|---|---|---|
| Header `Name: message` | Sí | No | No |
| Separador frame | `    at name (loc)` / `    at loc` | `name@loc` | `name@loc` |
| Sin nombre | `at loc` | `@loc` | `@loc` |
| Top-level | `Object.<anonymous>` (CJS) o sin nombre (ESM/script) | `@loc` | `global code@` / `module code@` |
| Constructor | `new X` | `X` | `X` |
| Alias de método | `Type.fn [as alias]` | no | no |
| Nativo | `(native)` / `(<anonymous>)` / `node:internal/...` | sin frames self-hosted | `name@[native code]` |
| eval | `eval at <caller> (<outer loc>), <anonymous>:l:c` | `url line N > eval:l:c` | `eval code@` sin location |
| `new Function` | igual que eval, nombre `eval` o `anonymous` | `url line N > Function:l:c` | `anonymous@` sin location |
| Async | `at async fn (loc)`, `at async Promise.all (index N)` | `asyncCause*fn@loc`, solo con DevTools abierto (default) | `async fn@loc` (default en WebKit desde 2026-02) |
| Límite default | 10 | 128 | 100 |

---

## 4. Código minificado

- Un bundle minificado produce frames con nombres de función mangled y casi siempre `line` = 1 (o pocas líneas) con columnas grandes. **[empírico]** código de una línea `var a=function(n){return n.x.y},b=function(){return a({})};b();` en V8:

  ```
  TypeError: Cannot read properties of undefined (reading 'y')
      at a (eval at <anonymous> (file:///.../esm.mjs:7:7), <anonymous>:3:30)
      at b (eval at <anonymous> (file:///.../esm.mjs:7:7), <anonymous>:3:53)
  ```

  Los nombres (`a`, `b`) y coordenadas no dicen nada sin el source map.
- La única forma estándar de recuperar posición/archivo/nombre originales es el **source map** ([ECMA-426](https://tc39.es/ecma426/)): `mappings` (generated position → original source/line/column y opcionalmente un índice a `names`), `sources`, `sourcesContent`, `ignoreList`. La resolución es por `(line, column)` del frame generado, así que **la column es imprescindible**; los frames sin column (p. ej. eval en Firefox tras normalizar, o `[native code]`) no se pueden simbolizar. Los nombres originales de funciones solo se recuperan si el generador emitió `names` en la posición correspondiente.
- En Node, `--enable-source-maps` y `util.getCallSites({ sourceMap: true })` lo resuelven en el proceso (§3.1). En navegador el engine no aplica source maps a `Error.stack`; hay que hacerlo fuera (en el servidor). Esto conecta con el punto abierto "Source maps y releases" del mapa #1.
- Errores de scripts cross-origin: el HTML spec define *muted errors*; al reportarse una excepción de un classic script con muted errors, `errorInfo` se reemplaza por `message = "Script error."`, `filename = ""`, `lineno = 0`, `colno = 0`, `error = null`, es decir, **no hay stack**. — [HTML spec, report an exception](https://html.spec.whatwg.org/multipage/webappapis.html#report-the-exception). (Se evita sirviendo el script con CORS y el atributo `crossorigin`.)

---

## 5. Librerías y técnicas para parsear

### 5.1 Librerías (MIT, JS puro)

| Librería | Última versión npm | Campos por frame | Notas |
|---|---|---|---|
| [`error-stack-parser`](https://github.com/stacktracejs/error-stack-parser) (stacktrace.js) | 2.1.4 (2022-06-06) | `functionName`, `args`, `fileName`, `lineNumber`, `columnNumber`, `isNative`, `isEval`, `isConstructor`, `source` (vía `stackframe`) | Recibe un `Error`; detecta familia (`CHROME_IE_STACK_REGEXP` vs `FIREFOX_SAFARI_STACK_REGEXP`) y aplica un parser por familia. Incluye ramas Opera legacy. |
| [`stacktrace-parser`](https://github.com/errwischt/stacktrace-parser) | 0.1.11 (2025-02-18) | `file`, `methodName`, `arguments`, `lineNumber`, `column` | Recibe un string; prueba por línea, en orden, regex `chrome`, `winjs`, `gecko`, `node`, `JSC`. Declara: "This borrows heavily from TraceKit (https://github.com/csnover/TraceKit)". |

Fechas de [registry.npmjs.org](https://registry.npmjs.org/). Ambas son pequeñas (≈200 y ≈140 líneas) y basadas en regex; por ejemplo, la de V8 en `stacktrace-parser`:

```js
/^\s*at (.*?) ?\(((?:file|https?|blob|chrome-extension|native|eval|webpack|rsc|<anonymous>|\/|[a-z]:\\|\\\\).*?)(?::(\d+))?(?::(\d+))?\)?\s*$/i
```

**Comportamiento observado [empírico]** (ambas librerías con las muestras de §3; script en Apéndice B):

- Async: ninguna separa el marcador async. V8 `at async a1 (...)` → nombre `"async a1"`; Firefox `async*a1@...` → `"async*a1"`; JSC `async a1@...` → `"async a1"`. Hay que post-procesar el prefijo para obtener un `function` limpio y un flag `async`.
- `at async Promise.all (index 0)`: ambas lo **descartan** (no hay file:line).
- `eval` en V8: ambas **sustituyen** la posición interna por la del call site exterior (`inEval`, `/app/a.js`, 12, 7); se pierde `<anonymous>:1:26`. En Firefox `stacktrace-parser` hace lo mismo para `> eval` (column = `null`), pero **no** para `> Function` (deja `file = "https://example.com/app.js line 1 > Function"`).
- Nativos: `Array.map (<anonymous>)` lo descarta `error-stack-parser` y lo conserva `stacktrace-parser` con `file: "<anonymous>"`, sin line. JSC `map@[native code]` lo conservan ambas con `file: "[native code]"`.
- `eval code@` (JSC, sin location): `error-stack-parser` lo conserva con `fileName: ""`; `stacktrace-parser` lo descarta.
- `node:internal/...` se parsea como un archivo normal: clasificarlo queda a cargo del consumidor.

### 5.2 Técnicas sin parsear strings

- **V8/Node**: `Error.prepareStackTrace` + `CallSite` da frames ya estructurados (incluye `isAsync`, `isPromiseAll`, `isEval`, `getEvalOrigin`, `isNative`, `isConstructor`, `getTypeName`). Limitaciones: es global, solo V8, interfiere con otras herramientas que también lo sobrescriben, y con `--enable-source-maps` hay que delegar al original ([Node CLI](https://nodejs.org/api/cli.html)).
- **Node ≥ 22.9**: `util.getCallSites()` para el stack actual (no para errores ya capturados).
- **SpiderMonkey**: `SavedFrame` (`source`, `line`, `column`, `functionDisplayName`, `asyncCause`, `asyncParent`, `parent`) es estructurado, pero solo accesible para código privilegiado/DevTools, no para contenido web. — [SavedFrame docs](https://firefox-source-docs.mozilla.org/js/SavedFrame/index.html)
- **Navegador en general**: no hay API estructurada portable; parsear `error.stack` es la única opción cross-engine.

---

## 6. Distinguir frames in-app vs `node_modules`/vendor/internos

No existe un mecanismo estándar en el engine. Señales disponibles en las fuentes:

1. **Ruta con `/node_modules/`** (y `/bower_components/`). Es la heurística por defecto del ignore list de Chrome DevTools: "By default, the Debugger ignores scripts from `/node_modules/` and `/bower_components/`." — [Chrome DevTools, Ignore list](https://developer.chrome.com/docs/devtools/settings/ignore-list). En Node aparece tal cual en el path absoluto o en la URL `file://`. En navegador, tras un bundler, la ruta `node_modules` solo aparece **después** de simbolizar con el source map (el frame generado apunta al bundle).
2. **`ignoreList` del source map** (ECMA-426): "an optional list of indices of files that should be considered third party code, such as framework code or bundler-generated code… Some browsers may also use the deprecated `x_google_ignoreList` field". DevTools lo usa para "Automatically add known third-party scripts to ignore list". — [ECMA-426 §9](https://tc39.es/ecma426/), [Chrome DevTools](https://developer.chrome.com/docs/devtools/settings/ignore-list). Es una señal que emite el propio bundler, aplicable por source tras simbolizar.
3. **Internos del runtime**: en Node los módulos internos aparecen como `node:internal/...` y los builtins con prefijo `node:` (`module.isBuiltin('node:fs') === true` **[empírico]**); `process.processTicksAndRejections` también es interno. En V8, `native` / `<anonymous>` sin line; en JSC `[native code]`. Son frames sin source in-app.
4. **Esquemas de URL**: `chrome-extension://`, `moz-extension://`, `safari-web-extension://`, `resource://`, `blob:`, `data:` indican código ajeno a la app o generado (los regex de `stacktrace-parser` ya enumeran varios). Un frame cuyo origin difiere del de la app indica, típicamente, un script de terceros (CDN, analytics).
5. **Configuración del usuario**: lista de prefijos/patrones in-app o excluidos por Project (p. ej. raíz del proyecto en Node, origin propio en navegador). Ninguna fuente primaria la define; es la alternativa a las heurísticas anteriores.

---

## 7. Hechos relevantes para la normalización (resumen)

- No hay estándar de formato; los tres engines tienen gramáticas distintas pero estables y documentadas en su código fuente (`v8.dev`, `SavedStacks.cpp`, `StackFrame.cpp`).
- V8 es el único que incluye header `Name: message`; un parser debe saltar líneas que no son frames.
- Las tres familias marcan los async frames de forma distinta (`at async`, `cause*`, `async `), y en Firefox normalmente no aparecen en producción.
- `eval`/`Function` tienen tres representaciones incompatibles; en JSC no hay location.
- Columnas 1-based en los tres, pero apuntan a posiciones distintas para la misma expresión; no sirven para comparar entre engines y sí para simbolizar con source maps dentro de un mismo engine.
- Frames sin file/line (`Promise.all (index N)`, nativos, `eval code@`) existen en los tres y las librerías los tratan de forma inconsistente (descartar vs conservar).
- Las dos librerías JS más usadas son pequeñas, regex, MIT, de mantenimiento bajo (última release 2022 y 2025), y no normalizan async ni eval de forma completa.
- Profundidad por defecto muy distinta (10 / 128 / 100).

---

## Apéndice A: script de captura

```js
var out = (typeof console !== 'undefined' && console.log) ? console.log.bind(console) : print;
function foo() { bar(); }
function bar() { baz(); }
function baz() { throw new Error('sync'); }
try { foo(); } catch (e) { out('--- sync\n' + e.stack); }
class Widget { constructor() { throw new Error('ctor'); } render() { throw new Error('method'); } }
try { new Widget(); } catch (e) { out('--- constructor\n' + e.stack); }
try { Widget.prototype.render.call({}); } catch (e) { out('--- class method\n' + e.stack); }
var obj = { handler: function () { throw new Error('alias'); } }; obj.alias = obj.handler;
try { obj.alias(); } catch (e) { out('--- method alias\n' + e.stack); }
try { [1].map(function () { throw new Error('anon cb'); }); } catch (e) { out('--- anonymous + native\n' + e.stack); }
try { eval("function inEval(){ throw new Error('eval'); } inEval()"); } catch (e) { out('--- eval\n' + e.stack); }
try { new Function("throw new Error('Function ctor')")(); } catch (e) { out('--- Function ctor\n' + e.stack); }
async function a1() { await a2(); }
async function a2() { await null; throw new Error('async'); }
a1().catch(function (e) { out('--- async/await\n' + e.stack); });
async function p1() { await Promise.all([p2(), 1]); }
async function p2() { await null; throw new Error('promise.all'); }
p1().catch(function (e) { out('--- Promise.all\n' + e.stack); });
function t1() { return Promise.resolve().then(function t2() { throw new Error('then chain'); }); }
t1().catch(function (e) { out('--- .then chain\n' + e.stack); });
```

Ejecutado con `node sample.js` y `/System/Library/Frameworks/JavaScriptCore.framework/Versions/A/Helpers/jsc [--useAsyncStackTrace=1] sample.js`.

## Apéndice B: salida de los parsers

`[function, file, line, column]` por frame, con `error-stack-parser@2.1.4` (ESP) y `stacktrace-parser@0.1.11` (STP):

```
=== v8_async
ESP: [["a2","/app/src/a.js",15,41],["process.processTicksAndRejections","node:internal/process/task_queues",104,5],["async a1","/app/node_modules/lib/index.js",14,23]]
STP: [["a2","/app/src/a.js",15,41],["process.processTicksAndRejections","node:internal/process/task_queues",104,5],["async a1","/app/node_modules/lib/index.js",14,23]]
=== v8_eval
ESP: [["inEval","/app/a.js",12,7],["new Widget","file:///app/a.mjs",6,38]]
STP: [["inEval","/app/a.js",12,7],["Array.map","<anonymous>",null,null],["new Widget","file:///app/a.mjs",6,38]]
=== gecko
ESP: [["baz","https://example.com/app.js",10,15],["async*a1","https://example.com/app.js",14,23],[null,"https://example.com/app.js",3,null],["anonymous","https://example.com/app.js line 1 > Function",1,8]]
STP: [["baz","https://example.com/app.js",10,15],["async*a1","https://example.com/app.js",14,23],["<unknown>","https://example.com/app.js",3,null],["anonymous","https://example.com/app.js line 1 > Function",1,8]]
=== jsc
ESP: [["baz","https://example.com/app.js",10,24],["async a1","https://example.com/app.js",14,31],["map","[native code]",null,null],["eval code","",null,null],["global code","https://example.com/app.js",13,4]]
STP: [["baz","https://example.com/app.js",10,24],["async a1","https://example.com/app.js",14,31],["map","[native code]",null,null],["global code","https://example.com/app.js",13,4]]
```

Las muestras `gecko` y `jsc` de entrada se construyeron a mano siguiendo las gramáticas de §3.2 y §3.3.
