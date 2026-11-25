# Almacenamiento: Postgres vs SQLite embebido

Ticket: [#2](https://github.com/joansebgarcia/rec/issues/2) (mapa [#1](https://github.com/joansebgarcia/rec/issues/1)).
Fecha de investigación: 2026-11-24.

**Pregunta.** Para un despliegue de un binario Go + una DB y ~500 Events/s en 2 vCPU / 4 GB (ingesta HTTP p99 < 50 ms, aceptar y encolar): ¿qué ofrecen Postgres y SQLite (modo WAL, driver puro Go) en throughput de escritura concurrente, payloads JSON, upserts atómicos de conteo de Issues, notificación y operación? **Hechos, no la decisión.**

Fuentes: documentación oficial de SQLite (sqlite.org), PostgreSQL (postgresql.org/docs/current, v19 al momento de la consulta), pkg.go.dev / repos de los drivers, y litestream.io. Cada afirmación lleva su fuente.

---

## 1. Throughput de escritura concurrente

### SQLite (WAL)

- **Un solo writer a la vez.** "since there is only one WAL file, there can only be one writer at a time." Readers y writer no se bloquean entre sí: "readers do not block writers and a writer does not block readers." — [wal.html](https://www.sqlite.org/wal.html)
- **Los writers hacen cola.** "a write transaction only takes milliseconds and so multiple writers can simply take turns. SQLite will handle more write concurrency than many people suspect." Si muchos hilos necesitan escribir "at the same instant (and they cannot queue up and take turns)", recomiendan un motor client/server. — [whentouse.html](https://www.sqlite.org/whentouse.html)
- **Costo dominado por transacciones, no por INSERTs.** La FAQ (actualizada 2024-11-19) dice que SQLite hace "far more than 50K inserts/second" y que "Putting multiple operations inside a single transaction can improve performance dramatically". — [faq.html #19](https://www.sqlite.org/faq.html#q19)
- **Durabilidad vs latencia.** Con `synchronous=NORMAL` en WAL no hay fsync por commit; "the checkpoint is the only operation to issue an I/O barrier". Contrapartida: "A transaction committed in WAL mode with synchronous=NORMAL might roll back following a power loss or system crash. Transactions are durable across application crashes". — [wal.html](https://www.sqlite.org/wal.html), [pragma.html#pragma_synchronous](https://www.sqlite.org/pragma.html#pragma_synchronous)
- **SQLITE_BUSY.** Transacción `DEFERRED` (default) que empieza leyendo y luego escribe puede fallar con `SQLITE_BUSY` si otro writer ya modificó la DB; `BEGIN IMMEDIATE` toma el lock de escritura al inicio. — [lang_transaction.html](https://www.sqlite.org/lang_transaction.html). `PRAGMA busy_timeout` hace que se espere en vez de fallar inmediatamente. — [pragma.html#pragma_busy_timeout](https://www.sqlite.org/pragma.html#pragma_busy_timeout)
- **Checkpoints.** Auto-checkpoint al llegar a 1000 páginas (~4 MB). Si siempre hay un reader activo, "the WAL file will grow without bound" (checkpoint starvation). — [wal.html](https://www.sqlite.org/wal.html)
- **Restricción de host.** "WAL does not work over a network filesystem"; todos los procesos deben estar en el mismo host. — [wal.html](https://www.sqlite.org/wal.html)
- **Uso server-side reconocido.** SQLite como storage engine detrás de un servidor de aplicación: "Database requests are serialized by the server, so concurrency is not an issue." — [whentouse.html](https://www.sqlite.org/whentouse.html)

### Postgres

- **Múltiples writers concurrentes** (MVCC, procesos por conexión) — modelo client/server que, según la propia doc de SQLite, "can usually handle far more write concurrency than SQLite ever will". — [whentouse.html](https://www.sqlite.org/whentouse.html)
- **Asynchronous commit** (`synchronous_commit`) configurable *por transacción*: riesgo de "loss of the last few transactions", "not data corruption"; ventana máxima = 3 × `wal_writer_delay`. La doc lo considera apropiado para "event logging". — [wal-async-commit.html](https://www.postgresql.org/docs/current/wal-async-commit.html)
- **Bulk insert desde Go.** pgx `CopyFrom` usa el protocolo COPY: "CopyFrom can be faster than an insert with as few as 5 rows"; `Batch` reduce round trips. — [pgx v5](https://pkg.go.dev/github.com/jackc/pgx/v5)
- **Memoria en 4 GB.** `shared_buffers` default típico 128 MB; para servidor dedicado ≥1 GB, "a reasonable starting value ... is 25% of the memory". `work_mem` default 4 MB, multiplicable por operación y sesión. — [runtime-config-resource.html](https://www.postgresql.org/docs/current/runtime-config-resource.html). En el despliegue objetivo, Postgres comparte los 4 GB con el binario Go (no es "dedicado").

### Sin benchmark first-party para este workload

Ninguna de las fuentes oficiales publica un benchmark equivalente a "500 Events/s con JSON + upsert de Issue en 2 vCPU". Dato derivado (aritmética, no medición): 500 Events/s con una transacción por Event deja ~2 ms por transacción en SQLite (writer serializado); agrupando N Events por transacción en los workers el presupuesto por transacción pasa a ~2N ms. Como la ingesta HTTP solo acepta y encola, la latencia de la DB no está en el camino del p99 < 50 ms, salvo backpressure de la cola. Validarlo requiere un prototipo/bench propio.

## 2. Payloads JSON

### SQLite

- JSON se guarda como texto ordinario; no existe tipo JSON. — [json1.html](https://www.sqlite.org/json1.html)
- **JSONB** (binario) desde 3.45.0 (2024-01-15), almacenado como BLOB; "intended for internal use by SQLite only". — [json1.html](https://www.sqlite.org/json1.html)
- Funciones JSON integradas por defecto desde 3.38.0; operadores `->` / `->>` desde 3.38.0. Funciones marcadas `SQLITE_DETERMINISTIC` (requisito para indexes on expressions). — [json1.html](https://www.sqlite.org/json1.html)
- No hay índice tipo GIN sobre documentos; la vía es indexar expresiones concretas (p.ej. `json_extract(payload,'$.level')`).

### Postgres

- `json` (texto exacto, se reparsea) vs `jsonb` (binario descompuesto, "significantly faster to process", soporta indexing). Recomendación oficial: "most applications should prefer to store JSON data as jsonb". `jsonb` no preserva orden de keys ni duplicados. — [datatype-json.html](https://www.postgresql.org/docs/current/datatype-json.html)
- **GIN**: `jsonb_ops` (default; `?`, `?|`, `?&`, `@>`, `@?`, `@@`) y `jsonb_path_ops` ("usually much smaller", sin key-exists). — [datatype-json.html](https://www.postgresql.org/docs/current/datatype-json.html)
- Valores grandes se almacenan fuera de línea vía TOAST (comportamiento estándar del storage de Postgres).

## 3. Upserts atómicos de conteo de Issues

### SQLite

- `INSERT ... ON CONFLICT(col) DO UPDATE SET count = count + 1` desde 3.24.0 (2018-06-04); `excluded.` para el valor propuesto; múltiples `ON CONFLICT` desde 3.35.0. No funciona en virtual tables. — [lang_upsert.html](https://www.sqlite.org/lang_upsert.html)
- `RETURNING` disponible desde 3.35.0 (2021-03-12); con UPSERT "reports both inserted and updated rows". — [lang_returning.html](https://www.sqlite.org/lang_returning.html)
- La atomicidad viene del writer único: no hay carrera entre upserts concurrentes porque las escrituras están serializadas.

### Postgres

- "ON CONFLICT DO UPDATE guarantees an atomic INSERT or UPDATE outcome; ... even under high concurrency." Requiere `conflict_target`; error de cardinalidad si el mismo comando toca una fila dos veces. Inserts sobre unique index "might block if concurrent sessions ... modify rows matching the unique index values" (contención en Issues "calientes"). — [sql-insert.html](https://www.postgresql.org/docs/current/sql-insert.html)
- `RETURNING` con `OLD`/`NEW` (permite saber si el Issue es nuevo: `old.*` es `NULL` sin conflicto). — [sql-insert.html](https://www.postgresql.org/docs/current/sql-insert.html)

## 4. Mecanismos de notificación (para SSE)

### Postgres: LISTEN/NOTIFY

- Entrega transaccional: "the notify events are not delivered until and unless the transaction is committed". Orden: por transacción y por orden de commit. — [sql-notify.html](https://www.postgresql.org/docs/current/sql-notify.html)
- Payload "shorter than 8000 bytes" (config por defecto). Cola de 8 GB; si se llena, "transactions calling NOTIFY will fail at commit". Una sesión con `LISTEN` y transacción larga bloquea la limpieza. — [sql-notify.html](https://www.postgresql.org/docs/current/sql-notify.html)
- Folding: mismo canal + mismo payload dentro de una transacción se entrega una sola vez. — [sql-notify.html](https://www.postgresql.org/docs/current/sql-notify.html)
- Sin listeners, no hay recepción ni almacenamiento (no es una cola durable; implícito en la doc).
- pgx: `Conn.WaitForNotification` bloquea hasta notificación o cancelación del context; requiere una conexión dedicada (no del pool compartido). — [pgx v5](https://pkg.go.dev/github.com/jackc/pgx/v5)

### SQLite

- No hay mecanismo cross-connection/cross-process equivalente a LISTEN/NOTIFY.
- `sqlite3_update_hook` es **por conexión**, solo rowid tables, sin garantía de si se invoca antes o después del cambio, y no debe modificar la conexión. — [c3ref/update_hook.html](https://www.sqlite.org/c3ref/update_hook.html)
- Hecho de arquitectura: con un único binario Go que es el único writer, la notificación puede ser in-process (después del commit del worker); no depende de la DB. Con más de un proceso (p.ej. escalado futuro) esto no funciona.

## 5. Operación

### Backups

- **SQLite**: `VACUUM INTO 'file'` produce "a consistent snapshot of the original database"; no es una operación de escritura (no falla por locks de writers); salida mínima. La backup API "uses fewer CPU cycles and can be executed incrementally". — [lang_vacuum.html](https://www.sqlite.org/lang_vacuum.html)
- **SQLite + Litestream** (third-party, proceso separado o librería Go): replica continuamente páginas WAL a un replica (p.ej. S3); replicación **asíncrona** (puede perder escrituras recientes); mantiene una read transaction larga y toma control del checkpointing. — [litestream.io/how-it-works](https://litestream.io/how-it-works/)
- **Postgres**: tres enfoques — SQL dump, file system level, continuous archiving/PITR. — [backup.html](https://www.postgresql.org/docs/current/backup.html). `pg_dump` "makes consistent exports even if the database is being used concurrently" y "does not block other users", pero "is generally not the right choice for taking regular backups of production databases". — [app-pgdump.html](https://www.postgresql.org/docs/current/app-pgdump.html)

### Migraciones

- **SQLite**: `ALTER TABLE` limitado a RENAME TABLE, RENAME COLUMN, ADD COLUMN (con restricciones: sin PK/UNIQUE, NOT NULL requiere default), DROP COLUMN (con restricciones), y `ALTER COLUMN SET/DROP NOT NULL` desde 3.53.0 (2026-04-09). Otros cambios requieren el procedimiento de 12 pasos (crear tabla nueva, copiar, drop, rename) dentro de una transacción. — [lang_altertable.html](https://www.sqlite.org/lang_altertable.html). `PRAGMA user_version` es un entero libre en el header, utilizable como versión de schema. — [pragma.html#pragma_user_version](https://www.sqlite.org/pragma.html#pragma_user_version)
- **Postgres**: `ALTER TABLE` completo (ALTER COLUMN TYPE, constraints, etc.) y DDL transaccional (conocimiento general; no verificado en una página concreta durante esta investigación).

### Despliegue

- **SQLite**: embebido en el binario; un archivo + `-wal` + `-shm` en disco local (no network filesystem). `journal_mode=WAL` es persistente en el archivo. — [pragma.html](https://www.sqlite.org/pragma.html), [wal.html](https://www.sqlite.org/wal.html)
- **Postgres**: un proceso servidor aparte (la "una DB" del despliegue), con su propio tuning de memoria, usuarios, upgrades de versión mayor.

## 6. Drivers Go sin cgo

| Driver | Mecanismo | Notas de la fuente |
|---|---|---|
| `modernc.org/sqlite` | Port transpilado de C a Go ("CGo-free") | Bundlea SQLite 3.54.x (v1.63.0, 2026-11-22). Su propia tabla: 1.3x–2.0x CPU time vs C nativo. Pragmas por DSN `_pragma=...`. Advierte que `database/sql` abre conexiones sin límite por defecto → usar `SetMaxOpenConns`. — [pkg.go.dev](https://pkg.go.dev/modernc.org/sqlite) |
| `github.com/ncruces/go-sqlite3` | Build Wasm de SQLite traducido a Go con wasm2go; VFS en Go puro | "competitive with alternatives"; "memory usage will be higher than alternatives" (sandbox Wasm por conexión). Una conexión no debe usarse concurrentemente desde varias goroutines. — [GitHub](https://github.com/ncruces/go-sqlite3) |
| `github.com/jackc/pgx/v5` | Driver Postgres en Go puro | `pgxpool`, `CopyFrom`, `Batch`, `json`/`jsonb`, `WaitForNotification`. `*pgx.Conn` no es concurrency-safe. — [pkg.go.dev](https://pkg.go.dev/github.com/jackc/pgx/v5) |

## 7. Trade-offs (hechos, sin decisión)

| Eje | SQLite (WAL, puro Go) | Postgres |
|---|---|---|
| Despliegue | Cero procesos extra; un archivo local | Proceso servidor aparte que comparte 2 vCPU / 4 GB |
| Escritura concurrente | Un writer; requiere serializar/batchear en workers | Writers concurrentes MVCC; contención solo por fila |
| Durabilidad barata | `synchronous=NORMAL` (pierde commits recientes en power loss, no en crash de app) | `synchronous_commit=off` por transacción (pierde ≤ 3×`wal_writer_delay`) |
| JSON | Texto o JSONB interno; índices por expresión | `jsonb` + GIN |
| Upsert de conteo | `ON CONFLICT DO UPDATE` + `RETURNING`; atómico por serialización | `ON CONFLICT DO UPDATE` atómico bajo concurrencia; `RETURNING old/new` |
| Notificación | Ninguna cross-process; in-process en el binario | LISTEN/NOTIFY transaccional, payload < 8000 B, no durable |
| Backups | `VACUUM INTO`, backup API, Litestream (async) | `pg_dump`, base backup + WAL archiving (PITR) |
| Migraciones | `ALTER TABLE` limitado; 12 pasos para el resto | `ALTER TABLE` completo |
| Overhead CPU del driver | modernc 1.3x–2.0x vs C; ncruces más memoria | pgx nativo; costo de red/IPC local |
| Escalado futuro multi-proceso | Bloqueado: mismo host, un writer | Posible |

## Preguntas abiertas

- Ningún benchmark first-party cubre este workload; se necesita un bench propio (prototipo) con payload típico de Event, batch size del worker y upsert de Issue en 2 vCPU.
- ¿Se acepta perder los últimos commits ante power loss (`synchronous=NORMAL` / `synchronous_commit=off`)? Afecta el throughput alcanzable en ambos.
- Interacción entre retención/purga (no especificada en el mapa) y el tamaño del archivo SQLite / VACUUM.
