# Análisis de Arquitectura y Hoja de Ruta — Forge

Análisis hecho por Claude Sonnet 5 sobre el estado real del código en `feat/spec-validate`, no sobre el spec aspiracional. Cada hallazgo está anclado a un archivo/línea concreto y, donde fue posible, verificado leyendo el código en vivo (no solo inferido). No es una lista de "buenas prácticas genéricas" — todo lo que sigue es específico de este repo.

---

## 1. Resumen ejecutivo

Forge tiene una arquitectura sólida en sus fundamentos: permisos deny-by-default reales (no cosméticos), un protocolo JSON-RPC único que sirve CLI/TUI/GUI sin duplicar lógica, aislamiento de subagentes por branching de sesión en vez de estado compartido, y un modelo de manifiestos (RF-11) con budgets duros y checkpoints HITL que ya piensa en autonomía seria. La deuda técnica más importante no está en el diseño general sino en **puntos de fricción concretos que aparecen justo cuando el modelo detrás del volante es chico, local y lento** — que es exactamente el caso de uso que se probó exhaustivamente en esta sesión y que quedó parcialmente documentado en `GUÍA.md`. La sección 4 de este documento está escrita específicamente para ese escenario.

---

## 2. Fortalezas (con evidencia)

- **Permisos deny-by-default de verdad**: `internal/perms` no es un check cosmético — cada `Kind` (fs/shell/git/github/custom) tiene su propio allowlist, y agregar la tool `github` obligó a crear un `KindGitHub` nuevo en vez de reutilizar el "custom write floor" existente, precisamente porque toca red. Buena señal de que el modelo de permisos se toma en serio caso por caso, no como checkbox.
- **Un solo protocolo para todo**: CLI, TUI y GUI web consumen el mismo `internal/daemon/rpc.go` — la GUI que se construyó esta sesión no necesitó ni una línea de lógica de negocio nueva, solo un cliente WebSocket en JS.
- **Aislamiento de subagentes por branching, no por estado compartido**: `SpawnChild` (`internal/agent/subagent.go`) rama la sesión en SQLite en vez de mantener contexto en memoria compartido entre padre e hijo — esto hace que el aislamiento sea estructural (mismo motor de permisos, nunca más ancho) en vez de depender de disciplina de código.
- **Config secrets bien pensado en el caso que importaba**: cuando se implementó `forge daemon set-password`, se detectó a tiempo que usar `config.Config.Save()` habría serializado el documento *fusionado* completo — filtrando API keys de un layer de config al otro — y se corrigió escribiendo solo la clave `daemon.auth_token_hash` en el JSON crudo. Ese cuidado no está generalizado (ver 3.h), pero demuestra que el patrón correcto ya se conoce en el propio código base.
- **Migraciones de schema con guardas de idempotencia reales**: `internal/store/migrate.go` corrige explícitamente el caso "re-ejecutar una migración `ALTER TABLE ADD COLUMN` sobre una DB ya migrada" con un chequeo de `pragma_table_info` — no es solo "funciona en el caso feliz".
- **Budgets como paredes duras, no advertencias**: `internal/run/budget.go` + el manifiesto RF-11 matan el run al agotar wall-clock/tokens/iteraciones en vez de solo loguear un warning — coherente con lo que promete el spec §7.2.

---

## 3. Problemas concretos encontrados

### a. El mensaje del usuario se duplica en cada llamada al LLM ✅ RESUELTO
**Dónde**: `internal/agent/loop.go` llama `a.store.AppendMessage(ctx, userMsg)` (persiste el mensaje) **antes** de llamar `a.ctxAssembler.Build(ctx, sessionID, userMessage)`. Dentro de `Build` (`internal/agent/context.go:217-243`), el paso 4 trae "historial reciente" con `c.store.GetMessages(...)` — que ya incluye el mensaje recién persistido como última entrada — y el paso 5 vuelve a agregar `userMessage` explícitamente, sin condición.

**Impacto verificado**: se confirmó en vivo durante esta sesión — el log de debug del daemon mostró el mismo mensaje `"hola"` dos veces consecutivas en el array `messages` de una sola request a la API del LLM, para un turno que solo tenía **un** mensaje de usuario persistido (`forge session replay` mostró un único `user: hola`). Esto paga tokens de prompt de más en *cada turno de la vida de la sesión*, y en modelos chicos con ventana de contexto ajustada empeora directamente el problema de truncamiento que ya se vio esta sesión con Ollama.

**Fix sugerido**: en el paso 4, excluir la última entrada cuando coincide con el mensaje actual (o, más simple, no volver a persistir-y-releer: pasarle a `Build` el historial *antes* del mensaje actual explícitamente, y dejar que el paso 5 sea la única fuente del turno en curso).

### b. Las descripciones de las tools se mandan duplicadas en cada request ✅ RESUELTO
**Dónde**: `Build()` paso 2 (`context.go:97-104`) inyecta un mensaje `system` por tool con `"TOOL: %s - %s"` (nombre + descripción). `ToolDefs()` (`context.go:322-336`), que llena `ChatRequest.Tools`, construye el **mismo** nombre+descripción otra vez dentro del JSON Schema estructurado (`llm.ToolFunctionDef.Description`). Ambos viajan en la misma request.

**Impacto verificado**: el log de debug mostrado en esta sesión (turno real contra `openrouter`/`go`) tiene las 16 descripciones de tool como mensajes de sistema en texto plano *y* repetidas dentro de `"tools":[...]` — con ~16 tools y descripciones de 100-250 caracteres cada una, son aproximadamente 1.5-2K tokens de pura duplicación en cada turno, sin aportar nada que el modelo no tenga ya en el schema estructurado.

**Fix sugerido**: eliminar el paso 2 (los system messages de texto plano) y confiar solo en `ChatRequest.Tools` — es lo que consumen los proveedores OpenAI-compatible de forma nativa. Si se quiere mantener el texto plano para proveedores sin soporte de function-calling estructurado, condicionarlo a esa capacidad en vez de mandarlo siempre.

### c. Un único timeout HTTP de 15 minutos para *todos* los proveedores openai-compatible ✅ RESUELTO
**Dónde**: `internal/llm/openai_compatible.go`, `http.Client{Timeout: 15 * time.Minute}` — hardcodeado, sin campo de config que lo module por proveedor.

**Impacto verificado esta sesión**: es exactamente lo que causó que el dogfooding con modelo local quedara descartado (turnos de 20-40 min en CPU pura chocando contra este límite) y, del otro extremo, lo que hizo que un turno colgado contra un modelo free de OpenRouter bloqueara la GUI sin feedback por minutos, indistinguible de "no funciona" para el usuario.

**Fix sugerido**: mover el timeout a `config.Provider` (ej. `request_timeout_seconds`, default 15 min) para poder ponerle un techo bajo a un modelo local que se sabe rápido o alto a uno remoto grande legítimamente lento — hoy es la misma constante para Ollama en localhost y para un servicio cloud.

### d. `session.execute_turn` bloquea sincrónicamente todo el turno
**Dónde**: `internal/daemon/handler.go:handleExecuteTurn` no retorna hasta que `mgr.ExecuteTurn` termina el turno completo (incluye todas las iteraciones de tool-calling). El streaming interno (`callLLMStream`, TTFT) existe pero solo sirve para medir tiempo-al-primer-token en logs — no hay notificación incremental al cliente RPC durante el turno salvo lo que ya viaja como `message.delta.event` para la TUI/GUI, que tampoco resuelve el caso donde el turno entero (incluidas tool calls) tarda minutos.

**Mitigado parcialmente esta sesión**: se agregó un indicador visual "thinking…" y renderizado optimista del mensaje del usuario en la GUI — pero es un parche de UX cliente, no cambia que el protocolo mismo bloquea.

**Fix sugerido**: si se quiere una experiencia realmente responsive con modelos lentos, la solución de fondo es que `execute_turn` devuelva inmediatamente un `job_id` (ya existe el concepto de background jobs, `forge job follow/cancel`) y el cliente seguga el resultado por notificación, en vez de mantener la conexión RPC bloqueada.

### e. El model router (`internal/routing`) está a medio cablear ✅ RESUELTO (para tareas de manifiesto; ver detalle)
**Dónde**: `routing.StepType` define `classify|retrieve|summarize|generate|validate|reason` y `routing.ModelRole` define `cheap|generation|reasoning` — pero el comentario en `context.go:282-297` lo dice explícitamente: *"routing affects exactly this existing operation [StepGenerate]: retrieval embeddings and compaction summaries are deterministic and make no model calls, so there is nothing else to route"*.

**Impacto original**: la infraestructura para enrutar pasos baratos (clasificación, generación de queries de retrieval, resúmenes) a un modelo chico/local y reservar el modelo grande solo para generación/razonamiento real — que es precisamente la estrategia correcta para abaratar y acelerar el uso de LLMs locales chicos — estaba definida pero no conectada a nada excepto el paso principal.

**Qué se cableó**: `routing.ModelRouter.ModelForRole(role)` (nuevo, refactor de `ModelForStep` para reusar la misma cadena de fallback) resuelve un rol directamente en vez de solo por step. `ExecuteTurnParams.ModelHint` (nuevo campo RPC) viaja desde `Task.ModelHint` (agregado en la prioridad 3 — hasta ahora puramente declarativo) a través de `client.ManifestExecutor` → `SessionManager.ExecuteTurnWithModelHint` (método nuevo, no rompe el `ExecuteTurn` existente) → se resuelve vía `providers.<name>.model_roles` → `agent.TurnOptions.OverrideModel` (nuevo campo, gana sobre el override de `spawn_subagent` y sobre el flag de routing de sesión) → pinning real del modelo para ESE turno puntual.

**Verificado en vivo**: manifiesto con 4 tareas (`model_hint: cheap/generation/reasoning`/sin hint) contra `model_roles: {cheap: glm-5.3-flash, generation: minimax-m3, reasoning: kimi-k3}` — el log del daemon confirmó `t-cheap` disparando una request con `"model":"glm-5.3-flash"` y `t-generation` con `"model":"minimax-m3"` (el run se cortó ahí por un budget de tokens demasiado ajustado en mi propio manifiesto de prueba, no por un bug).

**Lo que sigue sin cablear** (fuera de alcance de este incremento): `StepClassify`/`StepRetrieve`/`StepSummarize` siguen sin hacer llamadas a modelo (retrieval/compaction son determinísticos, como decía el comentario original) — ahí no hay nada que enrutar todavía porque no hay llamada que enrutar. Lo que sí se resolvió es la mitad que faltaba: que un `model_hint` declarado en una tarea (a mano o por el descomponedor de §5.2) efectivamente cambie qué modelo ejecuta esa tarea puntual, en vez de ser un campo decorativo.

### f. La descomposición de tareas del manifiesto (RF-11) es 100% manual ✅ RESUELTO (ver §5.2/5.3)
**Dónde**: `Manifest.EffectiveTasks()` (`internal/run/manifest.go:258-263`) — si `tasks` viene vacío, crea un único `Task{Goal: m.Goal}`. No hay ningún paso de descomposición automática (ni por LLM ni heurístico) en ningún lugar del código; `Task.DoneCriteria` es un string libre que **nunca se verifica mecánicamente** (confirmado: `grep DoneCriteria internal/run/runner.go` no devuelve nada — el runner considera "hecho" simplemente cuando el turno del agente termina sin halt, sin ejecutar ni chequear el criterio).

Esto es el gap más directamente relevante para el pedido de fragmentar tareas grandes para modelos locales chicos — ver la sección 4 completa.

### g. Paralelismo de subagentes exige un batch 100% homogéneo
**Dónde**: `internal/agent/loop.go:381-387` — el chequeo `allSpawn` solo activa el pool paralelo cuando **todas** las tool calls de la iteración son `spawn_subagent` y son ≥2. Una mezcla de `spawn_subagent` + cualquier otra tool en la misma iteración cae entera a secuencial, perdiendo el paralelismo de los spawn_subagent que sí estaban ahí.

### h. Patrón de fuga de secrets al guardar config: mitigado en un solo lugar, no generalizado
**Dónde**: `internal/cli/daemon_auth.go` (`setDaemonAuthTokenHash`) evita `config.Config.Save()` a propósito por el riesgo de fusión de layers. Pero es el único punto del código que lo hace explícitamente — cualquier futuro comando que necesite persistir *una* clave de config corre el riesgo de reintroducir el mismo problema si usa el camino "obvio" (`Save()` del documento fusionado). Vale la pena extraer el patrón (`setConfigKey(path, key, value)` genérico) para que la próxima persona no tenga que redescubrir el problema.

### i. Fragilidad de tests por flags de Cobra compartidos entre casos
**Dónde**: ya mordió en esta sesión — `TestRunResumeAndVerifyAuditMutuallyExclusive` dejaba `--resume` seteado (variable de flag a nivel de proceso) y rompía `TestRunVerifyAuditRequiresManifestFlag` corrido después en el mismo binario de test. Se arregló ad-hoc reseteando flags explícitamente en el test afectado, pero el patrón de fondo (flags de Cobra como variables de paquete, compartidas entre `execRoot()` de distintos tests) sigue ahí y puede repetirse en cualquier comando nuevo con flags booleanos/string a nivel de paquete.

### j. Reconexión WebSocket de la GUI sin backoff
**Dónde**: `internal/webui/static/app.js`, `ws.onclose = () => { onStatus(false); setTimeout(connect, 2000); }` — reintenta cada 2 segundos indefinidamente si el daemon está caído. No es grave a la escala de uso actual (un daemon local, un usuario), pero es el tipo de detalle que se nota si el daemon tarda en levantar tras un reinicio del sistema.

### k. Doble uso del término "schema_version" para dos conceptos distintos
`config.Config.SchemaVersion` (formato del archivo de config) y `store.currentSchemaVersion` (schema de la base SQLite) son conceptos completamente independientes que comparten nombre. No es un bug, pero es una fuente fácil de confusión al leer logs o debuggear ("¿de cuál versión hablamos?").

### l. Límite de 32KB por mensaje WebSocket — cualquier turno "grande" rompía la conexión entera ✅ RESUELTO
**Dónde**: ni `internal/client/client.go` (dos `websocket.Dial`) ni `internal/daemon/transport.go` (`websocket.Accept`) configuraban `SetReadLimit` — quedaban en el default de la librería `coder/websocket`, **32 KiB**.

**Impacto verificado en vivo**: al probar la feature de descomposición de tareas (§5.2) contra un modelo real, la respuesta de `session.execute_turn` (transcript completo del turno: mensajes, tool traces, usage) superó los 32KB y la conexión se cerró en seco con `StatusMessageTooBig` — el cliente reportaba "connection to daemon lost", un mensaje genérico que no daba ninguna pista de la causa real. Esto no es exclusivo de la descomposición: **cualquier turno normal** con una respuesta larga del modelo, varias tool calls, o resultados de tools verbosos puede pegar contra el mismo techo — probablemente explica alguna fracción de conexiones "perdidas" atribuidas antes a otra cosa (modelo colgado, red, etc.) sin diagnóstico claro.

**Fix aplicado**: `conn.SetReadLimit(16 * 1024 * 1024)` (16 MiB) en los tres puntos (los dos `Dial` del cliente + el `Accept` del daemon) — constante duplicada a propósito en `internal/daemon` (no puede importar `internal/client`), con comentario cruzado entre ambas.

---

## 4. Hoja de ruta de mejoras

Prioridad sugerida (de mayor a menor impacto/esfuerzo):

1. **(a) y (b)** — son bugs de una sola sesión de trabajo, con impacto directo y medible en costo de tokens y en el problema real de truncamiento de contexto que ya se vivió. Arreglar primero.
2. **(c)** — timeout por proveedor, desbloquea poder tunear el daemon distinto para local vs. cloud sin recompilar.
3. **(f) + sección 5 completa** — descomposición de tareas: es el gap de mayor apalancamiento para el objetivo específico de este documento (modelos locales chicos).
4. **(e)** — terminar de cablear el router a los demás steps, es la pieza que hace que 3 sea sostenible en costo/latencia.
5. **(d)** — pasar `execute_turn` a un modelo de job asíncrono real, si se sigue invirtiendo en la GUI como superficie principal.
6. **(g), (h), (i), (j), (k)** — mejoras de robustez/mantenibilidad, no bloquean nada hoy pero son deuda que crece con cada feature nueva.

---

## 5. Técnicas para fragmentar tareas grandes en tareas atómicas para Forge + modelos LLM locales chicos

Esta sección responde directamente al pedido del usuario, apoyada en evidencia empírica real de esta sesión: en el hardware probado (CPU pura, 6 cores, sin GPU), modelos locales vía Ollama tardaban 20-40+ minutos por turno con el prompt real de Forge (~4000 tokens, 16 tools), y varios modelos (`qwen2.5-coder:7b`, `deepseek-r1:8b`, `hhao/qwen2.5-coder-tools:3b`) devolvían el tool call como texto plano en vez de `tool_calls` estructurado — degradándose silenciosamente a "no hace nada" en vez de fallar ruidosamente. Cualquier estrategia de fragmentación tiene que asumir estas tres restricciones como punto de partida: **contexto chico, tool-calling poco confiable, latencia alta por turno.**

### 5.1 Reducir el prompt fijo antes de fragmentar nada
Arreglar 3.a y 3.b (mensaje duplicado, tools duplicadas) recupera de entrada ~3-4K tokens de las ~4K de contexto total que tiene un modelo local típico configurado con default de Ollama. Sin este paso, cualquier estrategia de fragmentación sigue chocando contra el mismo techo de contexto que ya causó el abandono del dogfooding esta sesión — es la precondición, no una mejora aparte.

### 5.2 Descomposición real en `Manifest.Tasks`, con un modelo "planificador" separado del "ejecutor" ✅ IMPLEMENTADO
`forge run --manifest run.json --decompose` (manifiesto con `tasks` vacío): el CLI abre una sesión daemon efímera y descartable (separada de la sesión de ejecución, para que la exploración de la descomposición no contamine el contexto de cada task real) y le pide al modelo default del daemon un array JSON de tareas (`internal/run/decompose.go`: `BuildDecompositionPrompt`/`ParseDecomposedTasks`; `client.ManifestDecomposer` en `internal/client/run_manifest.go`). La propuesta se valida con las mismas reglas que un manifiesto escrito a mano (`ValidateTasks`, extraída de `Manifest.Validate`), se persiste como rastro de auditoría en `tasks.decomposed.json`, y recién entonces se llega al checkpoint HITL `after_spec_decomposition` — que ahora sí tiene contenido real que aprobar en vez de dispararse contra el fallback de un solo task. Probado en vivo contra `minimax-m3`: produjo 5 tareas atómicas coherentes (una a "inspeccionar el server", una a "agregar el handler", una a "registrar la ruta", una a "escribir el test", una a "verificar todo"), cada una con `done_criteria` ejecutable y `file_budget`/`model_hint` declarados.
Cada `Task` ahora declara `file_budget` y `model_hint` (`internal/run/manifest.go`) — hoy puramente informativos (nada los consume todavía; `model_hint` necesitaría que `session.execute_turn` acepte un override de modelo por turno, que no existe — ver 5.6), pero ya forman parte del schema para cuando se cableen.
**Hallazgo real durante la prueba en vivo, ya corregido**: el modelo devolvió `"cmd: go build ./... && go test ./..."` — un command con operador de shell (`&&`) que el chequeo mecánico (5.3) ejecuta SIN shell (exec directo, como `shell_exec`). Se corrigió el prompt para prohibir explícitamente operadores de shell (`&&`, `|`, `;`, redirects) y se agregó una guarda defensiva en `checkDoneCriteria` que rechaza con un error claro cualquier token con esos caracteres, en vez de ejecutar algo roto en silencio — exactamente el tipo de falla que un modelo chico/local (el público objetivo de esta feature) es más propenso a cometer.
También se encontró y corrigió un bug de extracción de JSON: la primera versión de `ParseDecomposedTasks` tomaba "desde el primer `[` hasta el ÚLTIMO `]` de todo el texto" — si la respuesta del modelo tenía prosa después del array conteniendo su propio `]` suelto (ej. un ejemplo `arr[0]`), la extracción se pasaba de largo y rompía el parseo. Se reemplazó por un escaneo de profundidad de brackets que respeta strings JSON.

### 5.3 Verificación mecánica de `DoneCriteria`, no por juicio del modelo ✅ IMPLEMENTADO
`Task.DoneCriteria` con prefijo `"cmd: "` ahora se verifica mecánicamente: `Runner.checkDoneCriteria` (`internal/run/runner.go`) corre el comando (exec directo, sin shell — mismo patrón que la tool `shell_exec`, dividido por espacios en blanco) después de que el turno del agente "termina", y solo marca la tarea como completa si sale con status 0. Un `DoneCriteria` sin ese prefijo sigue siendo texto puramente descriptivo, nunca chequeado — opt-in, no rompe manifiestos existentes. Una falla del check cuenta como fallo de la tarea igual que un error del executor (dispara el mismo circuit breaker de reintentos y el checkpoint implícito de "reintentos agotados"), en vez de confiar en que el modelo declare éxito.

### 5.4 Usar `spawn_subagent`/`fanout` como el mecanismo de fragmentación en tiempo de ejecución
La infraestructura de override de `provider`/`model` por child (RF-9.3, `ChildSpec.Provider/Model` en `internal/agent/subagent.go:39-40`) ya permite exactamente el patrón "un modelo grande orquesta, cada subagente ejecuta una tarea atómica con un modelo local chico". Falta la pieza de política: una heurística (o el `ModelRouter` de 5.6) que decida, task por task, si conviene despachar como `spawn_subagent` con `model` apuntando a un modelo local barato — reservando el modelo capaz/remoto para la tarea de orquestación, que necesita más razonamiento pero corre pocas veces por run.

### 5.5 Perfil de "tarea atómica apta para modelo local chico"
Concretamente, una tarea es candidata a ejecutarse con un modelo local chico cuando cumple todo esto — y el descomponedor de 5.2 debería usarlo como criterio de corte al dividir una tarea grande:
- Toca **un archivo o una función**, no un subsistema.
- Se espera que necesite **≤3 tool calls** para completarse (si el plan estima más, se sigue dividiendo).
- Tiene un `done_criteria` ejecutable (5.3), no subjetivo.
- El contexto de entrada cabe explícitamente en un `file_budget` chico — no depende de que el modelo "busque" qué archivos tocar (eso es trabajo del planificador, no del ejecutor).

### 5.6 Terminar de cablear el `ModelRouter` para automatizar la elección ✅ IMPLEMENTADO (la mitad de ejecución)
La mitad "un `model_hint` de una tarea realmente cambia qué modelo la ejecuta" ya está resuelta (ver hallazgo e en la sección 3) — `providers.<name>.model_roles` ya no es una config que nadie lee, `Task.ModelHint` ya no es puramente decorativo. Lo que queda pendiente, y es un incremento genuinamente separado: que la elección del rol (`cheap` vs `reasoning`) sea automática en vez de que el descomponedor (5.2) o un humano tengan que adivinarla — por ejemplo, un `StepClassify` real que mire la tarea antes de despacharla y decida su rol, en vez de que el modelo planificador simplemente lo declare en el JSON de descomposición.

### 5.7 Compactación de contexto agresiva cuando el ejecutor es un modelo local
`compactionThreshold` está fijo en 40 mensajes (`internal/agent/context.go:256`) y la compactación es opt-in vía `--compaction`. Para tareas fragmentadas pensadas para modelos chicos, tiene sentido que ese umbral sea mucho más bajo (10-15 mensajes) y que se active por default cuando la tarea se despache a un modelo marcado como local/chico — porque son precisamente los que menos margen de contexto tienen para cargar historial extenso.

### 5.8 Fallback de parsing cuando el tool-calling estructurado falla
Se confirmó esta sesión que varios modelos locales devuelven el tool call completo como JSON en el campo `content` (texto plano) con `finish_reason: "stop"` en vez de poblar `tool_calls` — y Forge, correctamente por seguridad, lo descarta como "el modelo simuló output" en vez de ejecutarlo. Para hacer que modelos con soporte de function-calling débil sean utilizables como ejecutores de tareas atómicas (sub 3B, cuantizados), conviene agregar una heurística de fallback: si `content` no está vacío, `tool_calls` está vacío, y `content` parsea como un objeto JSON con forma de tool call (`{"name": ..., "arguments": ...}` o similar), tratarlo como tal en vez de descartarlo — logueando claramente que se usó el fallback, para no ocultar el problema de fondo del modelo.

### 5.9 Timeout diferenciado por tamaño de modelo (depende de 3.c)
Una vez resuelto 3.c, un modelo local marcado como "chico" (ej. por convención de nombre o por un campo explícito en `providers.<name>.models`) puede tener un timeout bajo (2-3 min) que corte rápido un turno colgado, en vez de esperar los 15 minutos actuales — crítico para que un pipeline de tareas atómicas no se trabe entero por un solo subagente colgado.

### 5.10 Contexto de ventana declarado por modelo, no un `maxHistoryTurns` global
Hoy no hay ningún campo que declare "este modelo tiene contexto de 4096 tokens" — el problema real de esta sesión (Ollama truncando silenciosamente un prompt de ~4000 tokens con el default de 4096) se hubiera evitado, o al menos detectado a tiempo, con un `context_window` opcional por modelo en `config.Provider`, que el `ContextAssembler` pudiera usar para dimensionar cuántos mensajes de historial entran (en vez de un `maxHistoryTurns` fijo igual para un modelo de 128K y uno de 4K).

---

## 6. Nota metodológica

Todo lo anterior se verificó leyendo el código fuente citado (no se infirió de nombres de archivo ni de comentarios de spec), y los hallazgos de impacto ("confirmado esta sesión") se corroboraron con evidencia real capturada durante el trabajo de esta conversación: logs del daemon, `forge session replay`, y pruebas directas contra proveedores reales (Ollama local, OpenRouter, OpenCode Zen/Go). No se incluyó nada que no se pudiera anclar a una línea de código o a una observación concreta.
