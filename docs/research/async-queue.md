# Research: cola async — respaldada por la DB vs en memoria

Ticket: joansebgarcia/rec#3 (hijo del mapa #1). Fecha: 2026-11-24.

## Pregunta

¿Qué opciones existen en Go para desacoplar ingesta y procesamiento sin infraestructura extra (canal en memoria con workers, cola en la DB con `SELECT … FOR UPDATE SKIP LOCKED`, librerías tipo River/goqite, WAL local)? Durabilidad ante crash, backpressure, throughput y complejidad de cada una.

Restricciones del mapa: un binario Go + una DB, sin infra extra; ~500 Events/s en 2 vCPU / 4 GB; ingesta HTTP p99 < 50 ms (aceptar y encolar).

Este documento recoge hechos de fuentes primarias; no toma la decisión.

## 1. Canal en memoria + pool de workers

- **Semántica**: un canal con buffer "succeeds without blocking if the buffer is not full (sends)"; con capacidad 0 la comunicación solo ocurre cuando emisor y receptor están listos. [Go spec, Channel types](https://go.dev/ref/spec#Channel_types)
- **Backpressure**: un `select` con `default` elige el `default` si ninguna comunicación puede proceder; sin `default`, se bloquea. Eso da un envío no bloqueante: si el buffer está lleno, el handler HTTP puede responder de inmediato (p. ej. 429/503) en vez de esperar. [Go spec, Select statements](https://go.dev/ref/spec#Select_statements)
- **Durabilidad**: ninguna. El contenido del buffer vive en el heap del proceso; un crash, OOM o `kill -9` pierde todo lo aceptado y aún no persistido. (Consecuencia directa de la semántica anterior; no hay fuente que lo "garantice", es la ausencia de persistencia.)
- **Throughput / latencia**: no añade I/O en la ruta de ingesta; el coste de encolar es una operación de canal. El límite real lo pone la escritura de Events en la DB por parte de los workers.
- **Complejidad**: mínima, solo stdlib. Hay que diseñar a mano el graceful shutdown (drenar el canal al recibir SIGTERM), el tamaño del buffer y la política cuando está lleno.

## 2. Cola en la DB "a mano" con `FOR UPDATE SKIP LOCKED` (Postgres)

- **Docs oficiales**: "With `SKIP LOCKED`, any selected rows that cannot be immediately locked are skipped. Skipping locked rows provides an inconsistent view of the data, so this is not suitable for general purpose work, but can be used to avoid lock contention with multiple consumers accessing a queue-like table." `SKIP LOCKED` solo afecta a locks de fila; el lock de tabla `ROW SHARE` se toma igual. [PostgreSQL, SELECT — The Locking Clause](https://www.postgresql.org/docs/current/sql-select.html#SQL-FOR-UPDATE-SHARE)
- **Durabilidad**: la de un commit de Postgres. Con commit síncrono (default) el servidor espera a que el WAL esté en disco antes de reportar éxito.
- **Palanca latencia/durabilidad**: `synchronous_commit = off` devuelve éxito antes del flush del WAL; la ventana de pérdida máxima es "three times `wal_writer_delay`"; "The risk ... is of data loss, not data corruption"; se puede fijar **por transacción** (`SET LOCAL`), así que se puede aplicar solo al insert de ingesta. Distinto de `fsync = off`, que sí puede corromper. [PostgreSQL, Asynchronous Commit](https://www.postgresql.org/docs/current/wal-async-commit.html)
- **Backpressure**: implícita (la tabla crece); no hay rechazo automático. Habría que medir profundidad de cola (`count` o contador) y rechazar por umbral.
- **Notificación a workers**: polling o `LISTEN/NOTIFY` de Postgres. [PostgreSQL, NOTIFY](https://www.postgresql.org/docs/current/sql-notify.html)
- **Coste operativo**: cada `UPDATE`/`DELETE` de un job deja tuplas muertas que debe recuperar `VACUUM`; una tabla de cola con alto churn depende de que autovacuum vaya al día. [PostgreSQL, Routine Vacuuming](https://www.postgresql.org/docs/current/routine-vacuuming.html)
- **Complejidad**: media. Hay que escribir a mano: schema, fetch con lock, reintentos, rescate de jobs atascados (worker muerto a mitad), limpieza de filas completadas.

## 3. River (`github.com/riverqueue/river`)

- **Qué es**: job queue para Go "backed by Postgres or SQLite", con reintentos, scheduling, unique jobs y UI web. Licencia MPL-2.0. Última release: v0.51.2 (2026-11-09) — aún pre-1.0. [README](https://github.com/riverqueue/river), [releases](https://github.com/riverqueue/river/releases)
- **Enqueue transaccional**: `InsertTx` dentro de la transacción de la app; el job solo existe/es visible si la transacción hace commit. Permite persistir el Event y su job de forma atómica. [README](https://github.com/riverqueue/river)
- **Batch**: `InsertMany`/`InsertManyTx` y `InsertManyFast`/`InsertManyFastTx`; este último usa `COPY FROM` de Postgres ("quite fast and memory efficient"), pero no maneja con gracia conflictos de unique jobs. [client.go](https://github.com/riverqueue/river/blob/master/client.go)
- **Mecanismo**: el fetch usa `SKIP LOCKED` en sus queries SQL ([river_job.sql](https://github.com/riverqueue/river/blob/master/riverdriver/riverpgxv5/internal/dbsqlc/river_job.sql)). Los workers se despiertan por `LISTEN/NOTIFY`; `FetchPollInterval` (default 1 s) es el fallback; `FetchCooldown` (default 100 ms) es el mínimo entre fetches y "Throughput is limited by this value"; `PollOnly` evita `LISTEN` (p. ej. detrás de PgBouncer en transaction pooling). `MaxWorkers` por cola controla el paralelismo. [client.go](https://github.com/riverqueue/river/blob/master/client.go)
- **Throughput reportado**: ~46k jobs/s en un MacBook Air M2 de 8 núcleos con 2000 goroutines worker, jobs no-op, modo burn-down (todo insertado antes de trabajar). La propia página advierte que depende de I/O, tamaño de DB y distancia a la DB, y que no debe tomarse como definitivo. Herramienta: `river bench`. [River, Benchmarks](https://riverqueue.com/docs/benchmarks)
- **SQLite**: driver `riversqlite`, desde v0.23.0, marcado **experimental** (schema puede cambiar). ~10k jobs/s en benchmark informal (≈ un cuarto de Postgres); batch fila a fila; riesgo de `SQLITE_BUSY` por un solo escritor; recomiendan WAL mode, un único pool y transacciones immediate. Probado con `modernc.org/sqlite` (pure Go) y libSQL. [River, SQLite](https://riverqueue.com/docs/sqlite)
- **Complejidad**: baja en código propio (reintentos, rescate, limpieza y UI incluidos); a cambio, dependencia pre-1.0 y sus migraciones de schema propias.

## 4. goqite (`github.com/maragudk/goqite`)

- **Qué es**: cola persistente "inspired by AWS SQS (but much simpler)", sobre SQLite; también soporta PostgreSQL vía opción `SQLFlavor`. Sin dependencias no-test; el driver SQL lo pone el usuario. Licencia MIT. Última release: v0.4.0 (2026-02-09). [README](https://github.com/maragudk/goqite)
- **Semántica**: tabla única con múltiples colas con nombre; un mensaje recibido no se reentrega hasta que vence su timeout (extensible); el consumidor debe borrarlo tras procesar (si no, se reentrega → at-least-once en la práctica); max receive configurable. En Postgres, `ReceiveTx` mantiene el lock de fila hasta fin de la transacción; prioridad best-effort con consumidores concurrentes (se saltan filas bloqueadas). Incluye un job runner. [README](https://github.com/maragudk/goqite)
- **Throughput reportado (SQLite)**: ~18.500 msg/s send+receive+delete secuencial con 1 productor/consumidor; ~12.500 msg/s con 16 en paralelo (cae por el único escritor de SQLite). Hardware: MacBook Pro M3 Max, SSD. [README](https://github.com/maragudk/goqite)
- **Complejidad**: baja; API pequeña. Menos funciones que River (sin UI, scheduling ni unique jobs según el README).

## 5. WAL local en disco (p. ej. `github.com/tidwall/wal`)

- **Qué es**: "A simple and fast write ahead log for Go": `Write(index, data)`, `Read(index)`, `WriteBatch`, `TruncateFront/TruncateBack`, segmentos (default 20 MB). Licencia MIT. Último push: 2025-08-31; sin releases publicadas. [README](https://github.com/tidwall/wal)
- **Durabilidad**: por defecto hace `fsync` tras cada escritura (`NoSync: false`); `NoSync: true` "puts the log at risk of data loss when there's a server crash"; existe `Sync()` manual para agrupar. [wal.go](https://github.com/tidwall/wal/blob/master/wal.go)
- **Throughput**: el README no publica números.
- **Complejidad**: alta en código propio: es solo un log, no una cola. Hay que construir encima offsets de consumo persistidos, reintentos, truncado tras confirmar escritura en la DB y recuperación al arrancar. Además añade un segundo almacén de estado (disco local) junto a la DB, que hay que respaldar/montar como volumen.

## 6. Hechos de SQLite relevantes si la "una DB" es SQLite

- WAL mode: lectores no bloquean al escritor y viceversa, pero "there can only be one writer at a time". [SQLite, WAL](https://www.sqlite.org/wal.html)
- `synchronous=FULL` hace sync del WAL en cada commit; con `NORMAL` se omite y "transactions are no longer durable and might rollback following a power failure or hard reset" (no por crash del proceso, sí por corte de energía/reset). [SQLite, WAL §2.3](https://www.sqlite.org/wal.html)
- Checkpoint automático al llegar a 1000 páginas de WAL. Todos los procesos deben estar en el mismo host; no funciona sobre network filesystem. [SQLite, WAL](https://www.sqlite.org/wal.html)

## Tabla resumen

| Opción | Durabilidad ante crash del proceso | Backpressure | Throughput publicado (fuente primaria) | Código propio |
|---|---|---|---|---|
| Canal en memoria | Ninguna | Nativo (`select`/`default` → rechazo inmediato) | N/A (sin I/O en ruta de ingesta) | Bajo |
| Postgres `SKIP LOCKED` a mano | Commit de Postgres (ajustable con `synchronous_commit` por transacción) | Manual (por profundidad) | Sin cifra oficial | Medio-alto |
| River (Postgres) | Commit de Postgres; enqueue atómico con el Event vía `InsertTx` | Manual | ~46k jobs/s (M2, no-op, burn-down) | Bajo |
| River (SQLite) | Commit SQLite; **experimental** | Manual | ~10k jobs/s (informal) | Bajo |
| goqite (SQLite/Postgres) | Commit de la DB; at-least-once con timeout | Manual | ~12.5k–18.5k msg/s (M3 Max, SQLite) | Bajo |
| WAL local (`tidwall/wal`) | `fsync` por escritura (default) | Manual | Sin cifra | Alto + segundo almacén |

## Observaciones para la decisión (no son la decisión)

- Todas las cifras publicadas superan ~500/s en uno o dos órdenes de magnitud, pero se midieron en portátiles de 8+ núcleos con jobs no-op; **no hay fuente primaria para 2 vCPU / 4 GB** ni para la latencia p99 de un insert en esa máquina. Haría falta un prototipo/benchmark (`river bench` o equivalente) en el hardware objetivo.
- Con commit síncrono, el p99 de ingesta incluye al menos un `fsync` del WAL de la DB; el batching (`InsertManyFast` / `COPY`) o `synchronous_commit = off` por transacción son las palancas documentadas para bajarlo, cada una con su coste (latencia de agrupación, o ventana de pérdida ≤ 3 × `wal_writer_delay`).
- Un híbrido canal en memoria → insert por lotes en DB combina backpressure nativo con durabilidad tras el flush, pero reabre una ventana de pérdida (lo que está en el canal). Es una combinación de los hechos anteriores, no un patrón documentado por una de las fuentes.
- La elección queda acoplada a la decisión de motor de DB (Postgres vs SQLite): River y goqite cubren ambos, pero el soporte SQLite de River es experimental.
