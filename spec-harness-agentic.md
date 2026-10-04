# SPEC — Harness de Desarrollo Agentic a Medida

**Nombre de proyecto (working title):** `forge` *(placeholder — renombrar libremente)*
**Versión del documento:** 0.10
**Estado:** Borrador para validación de arquitectura
**Alcance:** Definición funcional, no funcional y arquitectónica de un harness de desarrollo con agentes IA, inspirado en OpenCode y Claude Code, optimizado para modelos locales, eficiencia de contexto/tokens, y extensibilidad total.

---

## Estado de cobertura de requerimientos

> Lista de verificación de los 102 requerimientos (RF + RNF) de este documento. **Protocolo de mantenimiento:** se actualiza a medida que los requerimientos quedan cubiertos por la implementación; cada actualización queda versionada en el historial de git de este archivo (RF-8.4). El estado lo marca la verificación humana; `forge spec validate` (RF-8.3) aporta las señales mecánicas de evidencia, pero no distingue "implementado sin documentar" de "no implementado" — esa distinción se registra aquí.
>
> Convenciones: `- [x]` cubierto · `- [ ]` pendiente · las anotaciones entre paréntesis precisan estados parciales o decisiones de alcance.
>
> Última actualización: 2026-10-03 — **97/102 cubiertos**, 3 parciales (`- [~]`), 2 pendientes (`- [ ]`). Un test (`internal/cli/spec_coverage_test.go`) verifica que estas cifras coincidan con la lista; el CI falla si se desalinean. Plan de cierre: `hojaDeRuta-mejoras-revision.md`.

**RF-1. Núcleo de ejecución**
- [x] RF-1.1 Agente conversacional con tool-calling sobre workspace
- [x] RF-1.2 Agentes concurrentes por sesión (scheduler acotado, `max_parallel_children`)
- [x] RF-1.3 Subagentes con contexto propio (`spawn_subagent`, sesiones branchadas)
- [x] RF-1.4 Background jobs sobreviven al cliente (daemon RPC + `forge jobs`)

**RF-2. Conectividad con proveedores de LLM**
- [x] RF-2.1 Proveedores OpenAI-compatibles (Ollama, llama.cpp, vLLM, LM Studio)
- [x] RF-2.2 Adaptadores propios: Anthropic y Gemini
- [x] RF-2.3 Cambio de proveedor/modelo sin reiniciar sesión
- [~] RF-2.4 Ruteo por costo/complejidad (`model_roles`) (parcial: enruta la generación y, desde 2026-10-03, los resúmenes de compactación al rol `cheap`; la clasificación de intención y las queries de retrieval aún no usan modelo chico)
- [x] RF-2.5 Ruteo por tipo de paso (`ModelForStepSelector`)
- [x] RF-2.6 Streaming opt-in (default OFF, TTFT, falla vinculante)
- [x] RF-2.7 Cadena de respaldo entre proveedores/modelos (`fallback_chain`, opt-in) (agregado 2026-10-03)

**RF-3. Gestión de contexto y memoria**
- [x] RF-3.1 Memoria persistente entre sesiones (anchors)
- [x] RF-3.2 Recuperación selectiva de contexto (retrieval) (2026-10-03: índice por sesión, incremental, sin repetir lo que ya está en la ventana)
- [x] RF-3.3 Compactación jerárquica progresiva (2026-10-03: árbol fijo de resúmenes LLM del rol `cheap` — uno por bloque, uno por cada 4 — persistidos en SQLite e inmutables, generados entre turnos y cancelados al empezar un turno; los anchors van aparte y nunca se compactan. En vivo, 40 turnos con qwen2.5-coder:1.5b en Perfil A: vista compactada ~2.582 → ~300 tokens, prompt completo ~4.912 → ~2.630 (−46%), 26 resúmenes en 6 min 52 s en segundo plano. Límite medido: el modelo 1.5B confunde a veces "sugerido" con "hecho", por eso la vista se rotula como aproximada)
- [x] RF-3.4 Inspección/edición manual de memoria (`forge memory`)
- [x] RF-3.5 Anclaje explícito (tools + write floor RNF-4.12)
- [x] RF-3.6 Mapa del repositorio + `code_symbols` (agregado 2026-10-03; opt-in `agent.repo_map_tokens`)

**RF-4. Skills y auto-aprendizaje**
- [x] RF-4.1 Creación, carga e instalación de skills
- [x] RF-4.1.1 Wizard interactivo `forge skill new`
- [~] RF-4.2 Lazy-load por relevancia a la tarea (parcial: default manual, embedding hash; corregido 2026-10-03 que las skills desaparecían tras la primera iteración con herramientas)
- [x] RF-4.3 Propuesta por minería de patrones (`forge skill mine`)
- [x] RF-4.4 Aprobación humana antes de activar skills auto-generadas

**RF-5. Plugins y extensibilidad**
- [x] RF-5.1 Plugins de terceros (tools, providers, comandos, paneles)
- [x] RF-5.2 Ejecución en sandbox aislado (WASM)
- [x] RF-5.3 Manifiesto de plugin
- [x] RF-5.3.1 Wizard interactivo `forge plugin new`
- [x] RF-5.4 Habilitar/deshabilitar sin recompilar
- [x] RF-5.5 Integración MCP: cliente (servidores externos aprobados por hash, permisos `mcp` por servidor/herramienta) y servidor (`forge mcp serve`) — agregado 2026-10-03

**RF-6. CLI**
- [x] RF-6.1 Comandos core (sesiones, plugins, skills, proveedores)
- [x] RF-6.2 Modo interactivo (TUI) y no interactivo (one-shot)
- [x] RF-6.3 Salida JSON con envelope estable

**RF-7. GUI web (opcional, desacoplada)**
- [x] RF-7.1 Modo servidor exponiendo API para GUI web (`forge serve`, JSON-RPC 2.0 sobre WebSocket, `internal/daemon`)
- [x] RF-7.2 GUI como cliente de la misma API que el CLI (`internal/webui`, sin lógica propia — consume `session.*` como el CLI/TUI)
- [x] RF-7.3 Visualización de diffs, árbol de sesión, estado de agentes (2026-10-03: pestaña Runs con estado, checkpoints y diff por tarea vía `run.task_diff`; existe además: timeline de mensajes/tool-calls en vivo + `session.compare` para divergencia entre ramas)
- [x] RF-7.4 Acceso remoto protegible con autenticación (token compartido: header Bearer para el CLI, cookie de sesión vía `POST /auth/login` para la GUI — `forge daemon set-password`)

**RF-8. Desarrollo guiado por especificación**
- [x] RF-8.1 Spec como artefacto de primera clase (`forge spec`)
- [x] RF-8.2 Descomposición de spec en tareas trackeables (Runner)
- [x] RF-8.3 Señales de divergencia implementación vs spec (`forge spec validate`)
- [x] RF-8.4 Versionado de cambios de spec (git-native, `forge spec log/diff`)

**RF-9. Gestión de sesiones**
- [x] RF-9.1 Branching de sesiones
- [x] RF-9.2 Fusión de resultados de ramas (merge) (semántica append-tail, sin resolución de conflictos)
- [x] RF-9.3 Misma tarea en paralelo multi-modelo + comparación (`forge fanout`)

**RF-10. Integración con control de versiones y entorno**
- [x] RF-10.1 Integración git (diffs, commits, branches por tarea, worktrees)
- [x] RF-10.2 Shell dentro del workspace con visibilidad completa (perm-gated)
- [x] RF-10.3 Issues/PRs GitHub — solo lectura (tool `github`, envuelve el `gh` CLI del usuario, permission-gated como `git`)
- [x] RF-10.4 Deshacer un turno (`forge undo`, instantáneas en repo git sombra) (agregado 2026-10-03)

**RF-11. Ejecución autónoma de principio a fin**
- [x] RF-11.1 Run manifest como único punto de entrada
- [x] RF-11.2 Declaración de objetivo/SPEC/checkpoints HITL en el manifest
- [x] RF-11.3 Descomposición en tareas atómicas verificables
- [x] RF-11.4 Ciclo de auto-corrección acotado por tarea
- [x] RF-11.5 Agotar reintentos = checkpoint HITL implícito
- [x] RF-11.6 Continuar solo con criterio de "hecho" cumplido
- [x] RF-11.7 Checkpoint HITL: detener, resumir, esperar input humano
- [x] RF-11.8 Log/auditoría reanudable (2026-10-03: el daemon redescubre corridas interrumpidas/pausadas al reiniciar y las reanuda por run_id; existe: `RunState` persiste en `.forge/runs/`; `forge run --manifest ... --resume` reanuda desde el último estado consistente, salta tareas ya completadas y preserva la sesión/ventana de wall-clock original)
- [x] RF-11.9 Niveles de autonomía configurables
- [x] RF-11.10 Reporte final de corrida

**RNF-1. Rendimiento**
- [x] RNF-1.1 Cold start < 200ms (bench in-process: ~19-21ms mediana; binario real con embeddings, lanzamiento→puerto aceptando: mediana 196 ms tras arrancar el backend de embeddings en segundo plano — antes 5,3 s, 2026-10-03)
- [x] RNF-1.2 Overhead < 50ms por turno (bench: p50 1ms)
- [x] RNF-1.3 Memoria en reposo < 100MB (bench: ~3.3MB heap+stack)
- [x] RNF-1.4 Sesiones de larga duración sin degradación (2026-10-03: el crecimiento O(n²) del índice de retrieval quedó corregido; existe: `internal/perf/longsession_test.go`: 200 turnos reales, overhead/heap tardío comparado contra temprano — ver limitación de alcance documentada ahí: proxy de una sesión larga, no soak test literal de horas)
- [x] RNF-1.5 Concurrencia realista Perfil A (scheduler asumiendo cero paralelismo físico)
- [x] RNF-1.6 Reserva de núcleos (`llm.cores`, default NumCPU-2)

**RNF-2. Eficiencia de contexto/tokens**
- [x] RNF-2.1 Medición y reporte de tokens por turno/sesión/proveedor (TurnMetrics)
- [x] RNF-2.2 Orden estable para maximizar prompt-caching (2026-10-03: material por turno después del historial, ventana por pasos, compactación por bloques estables, `cache_control` en Anthropic; medido offline: tokens re-procesados 102,7k → 42,0k en 40 turnos)
- [x] RNF-2.3 Reducción ≥40% vs naive en sesiones >20 turnos (bench)
- [x] RNF-2.4 Reutilización KV-cache local (prefijo estable por sesión) (2026-10-03: ídem RNF-2.2; banco en vivo Perfil A: mismo prefijo de ~2k tokens, TTFT 164,9 s → 0,42 s)
- [x] RNF-2.5 Techo de contexto objetivo 4-8k tokens (2026-10-03: `fs_read` paginado a 16 KB, shell cabeza 4 KB + cola 8 KB, turno actual completo + historia en el resto del presupuesto; validación en vivo pendiente de RNF-10)

**RNF-3. Modularidad y mantenibilidad**
- [x] RNF-3.1 Core independiente de proveedor de LLM
- [x] RNF-3.2 Nueva funcionalidad vía plugin sin tocar el core
- [x] RNF-3.3 Tests de integración sobre el contrato de la API interna

**RNF-4. Seguridad**
- [x] RNF-4.1 Permisos deny-by-default para shell/fs/git (+ custom write floor + shell floor: `done_criteria` vía shell_exec, `workdir` confinado, ejecutables del workspace solo por ruta exacta, git floor también vía shell — 2026-10-03)
- [x] RNF-4.2 Plugins con privilegios mínimos y permisos declarados
- [x] RNF-4.3 Datos no salen del entorno local sin acción explícita
- [x] RNF-4.4 Secrets redactados antes de logs/store/contexto LLM (2026-10-03: claves JSON/YAML entre comillas, `*_API_KEY=`, bloque PEM completo, `AIza…`, Slack, JWT)
- [x] RNF-4.5 Contenido no confiable tratado como datos, nunca instrucciones (2026-10-03: también el texto derivado — retrieval y resúmenes de compactación iban como mensaje de sistema sin cercar; ahora van cercados y escapados, y los resúmenes LLM pasan por el detector de inyección)
- [x] RNF-4.6 Procedencia verificada (checksum/firma) para plugins/skills externos
- [x] RNF-4.7 Aislamiento de SO para shell del core (Linux: Landlock + seccomp; verificado en CI ubuntu-latest 2026-10-03 tras corregir dos bugs que impedían que se aplicara — acción por defecto de seccomp rechazada por la librería y falta de `pread64`/`pwrite64` en la allowlist, que rompía todo binario dinámico. Tests: TestAssembleFilter, TestApplyAndExecSmoke, TestApplyAndExecDeniesWriteOutsideWorkspace. Windows/macOS: solo modelo de permisos, como acepta la spec)
- [x] RNF-4.8 Parada de emergencia desde cualquier cliente
- [x] RNF-4.9 Allowlist de red explícita por defecto en adaptadores
- [x] RNF-4.10 Log/auditoría a prueba de manipulación (hash-chain append-only en `.forge/runs/<run_id>/audit.jsonl`, activo con sensibilidad `regulado`/`datos-sensibles`; `forge run --manifest ... --verify-audit` verifica la cadena)
- [x] RNF-4.11 Transporte cifrado para GUI remota (TLS obligatorio para bind no-loopback — `--tls-cert/--tls-key` o `--tls-self-signed`; safety floor en `internal/daemon` rechaza bindear fuera de loopback sin auth+TLS)
- [x] RNF-4.12 Anclaje derivado de contenido no confiable nunca automático (custom write floor; pendiente como item propio: checkpoint de aprobación para el opt-in)
- [x] RNF-4.13 Reglas `ask` con confirmación en el cliente; sin respuesta, deniega (agregado 2026-10-03)

**RNF-5. Portabilidad**
- [x] RNF-5.1 Linux, macOS y Windows
- [x] RNF-5.2 Sin dependencia de servicios de infraestructura externos

**RNF-6. Observabilidad**
- [x] RNF-6.1 Logging estructurado JSON con niveles configurables
- [x] RNF-6.2 Grabación y replay de sesiones completas
- [x] RNF-6.3 Métricas de costo estimado por sesión/proveedor (`forge session cost <id>` / `forge cost summary`; atribuido al proveedor default del daemon — ver limitación documentada en `internal/cost`, forge no registra qué proveedor produjo cada mensaje)

**RNF-7. Usabilidad / adaptabilidad**
- [x] RNF-7.1 Cambio de dirección a mitad de tarea sin perder estado
- [x] RNF-7.2 Configuración por proyecto versionable junto al código

**RNF-8. Autonomía segura (ligado a RF-11)**
- [x] RNF-8.1 Worktree/branch aislado en modo autónomo (2026-10-03: una sola corrida aislada por workspace y solo sus sesiones pueden escribir mientras la tiene; límite conocido: ediciones hechas fuera de forge (un editor) entran al siguiente commit de tarea; `git.isolation: branch` real — rama propia, árbol limpio, merge solo tras checkpoint `before_merge` aprobado; `worktree` se rechaza explícitamente hasta que las tools soporten otra raíz)
- [~] RNF-8.2 Piso de seguridad no configurable (git floor, budget walls) (parcial: tiempo, tokens e iteraciones sí; el presupuesto de costo `max_cost_usd` se declara pero no se mide ni se aplica)
- [x] RNF-8.3 Criterio de "tarea completada" con verificación positiva (2026-10-03: `cmd:` vía shell con permisos; criterios descriptivos verificados por un turno con veredicto JSON; tareas sin verificación positiva quedan listadas en el reporte)
- [x] RNF-8.4 Commits atómicos y reversibles por tarea (2026-10-03: un commit por tarea tras su criterio de hecho; merge `--no-ff`)
- [x] RNF-8.5 Workspace bloqueado durante una corrida aislada (agregado 2026-10-03, N1)

**RNF-9. Clasificación de sensibilidad (techo de autonomía)**
- [x] RNF-9.1 Clasificación única en config versionada (`general`/`regulado`/`datos-sensibles`)
- [x] RNF-9.2 Clasificación como techo sobre la autonomía
- [x] RNF-9.3 Manifest sobre el techo = rechazado, nunca degradado
- [x] RNF-9.4 Cambio de clasificación requiere acción humana explícita y registrada

**RNF-10. Validación empírica de rendimiento**
- [x] RNF-10.1 Banco de pruebas repetible (2026-10-03: `forge bench live` — tokens/s, TTFT, prefill por tamaño, reuso de KV-cache y tiempo de pared; línea base en `docs/bench/`)
- [ ] RNF-10.2 Corrida sobre los dos perfiles de hardware de referencia, métricas separadas (parcial: medido 2026-10-03 en una máquina por debajo del Perfil A — Ryzen 7 PRO 3700U, 4C/8T, ~14 GB —; faltan el Perfil A propiamente tal y el Perfil B)
- [ ] RNF-10.3 Objetivos cuantitativos de RNF-1/RNF-2 validados contra el banco en vivo (parcial: el banco existe; la reducción ≥40% sigue medida offline, y el Perfil A muestra que 4-8k tokens de contexto frío son minutos de prefill en CPU)

---

## 0. Visión y motivación

Herramientas como OpenCode y Claude Code resuelven bien el caso general, pero:
- No están optimizadas para el patrón de uso específico del operador (modelos locales, sesiones largas, cambios de dirección espontáneos).
- Su modelo de contexto es en gran medida "todo el historial, cada turno" — ineficiente en tokens.
- No son forkeables/adaptables a nivel de arquitectura interna sin asumir toda la complejidad del proyecto original.

Este proyecto no busca ser "mejor en todos los ejes" que herramientas con equipo detrás — busca ser **superior en el caso de uso propio**: eficiencia de contexto, control total del stack, y una arquitectura que crezca por plugins en vez de por reescritura.

**Principio rector — eficiencia de contexto y tokens:** el diferencial del proyecto es sostener sesiones agénticas reales minimizando los tokens de contexto por turno (RNF-2.1-2.5). Toda decisión de diseño se valida contra ese objetivo medido en el banco de pruebas de RNF-10 (§6).

**Bootstrapping (requerimiento deseable, diferido):** forge se construye a sí mismo. Implementar cada MVP usando el MVP anterior como herramienta principal de trabajo ya no es principio rector ni criterio de salida: queda registrado como requerimiento deseable, a abordar en algún momento del desarrollo cuando el core y su arquitectura estén más depurados — posiblemente hacia el final (v1.0 o posterior). Hasta entonces, el desarrollo usa herramientas externas sin restricción. Cuando se retome, recupera su condición de honestidad original: un harness agéntico que no puede sostener su propio desarrollo no cumple del todo su razón de ser.

**No-objetivos explícitos (v1):**
- No busca paridad de features con Claude Code/OpenCode desde el día uno.
- No busca fine-tuning ni entrenamiento de modelos propios en esta fase.
- No busca ser un producto multiusuario/SaaS en la primera iteración (aunque la arquitectura no debe cerrarse esa puerta).

---

## 1. Requerimientos funcionales (RF)

### RF-1. Núcleo de ejecución de agentes
- RF-1.1 El sistema debe poder ejecutar un agente conversacional con acceso a herramientas (tool-calling) sobre un directorio de trabajo (workspace).
- RF-1.2 Debe soportar múltiples agentes concurrentes dentro de una misma sesión de proyecto (orquestador + subagentes).
- RF-1.3 El agente orquestador debe poder invocar subagentes especializados, delegando una tarea acotada con su propio contexto y set reducido de herramientas.
- RF-1.4 Debe soportar ejecución de tareas en segundo plano (background jobs) que continúan aunque el cliente (CLI/GUI) se desconecte.

### RF-2. Conectividad con proveedores de LLM
- RF-2.1 Debe soportar cualquier proveedor compatible con la API estándar de OpenAI (`/v1/chat/completions` o `/v1/responses`), incluyendo Ollama, llama.cpp server, vLLM, LM Studio.
- RF-2.2 Debe soportar proveedores con protocolos propios (Anthropic Messages API, Google Gemini) vía adaptadores dedicados.
- RF-2.3 Debe permitir cambiar de proveedor/modelo sin reiniciar sesión, incluso a mitad de una tarea.
- RF-2.4 Debe soportar ruteo por costo/complejidad de la tarea — **esto no es solo "local vs. remoto"**: incluye usar un modelo local pequeño y rápido (orientativamente 1-3B parámetros cuantizados en el Perfil A, §5) para pasos baratos (clasificación de intención, generación de queries de retrieval, resúmenes de compactación) y reservar un modelo más capaz (7-8B en Perfil A; mayor en Perfil B o remoto) para la generación real. Gastar cómputo de un modelo grande en un paso que uno chico resuelve igual de bien es directamente contrario al objetivo de velocidad máxima en hardware estándar.
- RF-2.5 El ruteo debe ser configurable por tipo de paso del ciclo de un turno (§3.2) — no solo por "tarea" en general — de forma que cada paso (clasificación, retrieval, generación, validación) pueda apuntar a un modelo distinto.
- RF-2.7 (agregado 2026-10-03) Ante una falla reintentable de un proveedor o modelo (red, sobrecarga, límite de tasa), debe poder reintentar con la siguiente opción de una cadena de respaldo configurada (`fallback_chain`, opt-in), registrando el cambio — nunca ante errores no reintentables, y nunca en silencio a mitad de un stream ya mostrado (RF-2.6).
- RF-2.6 **Streaming de tokens (opcional, opt-in)**: los adaptadores deben implementar `ChatStream` (SSE) además de `Chat`, gobernado por el flag de configuración `llm.streaming` (default OFF). Semántica de falla vinculante: una falla a mitad de stream **FALLA el turno** — no hay fallback silencioso a no-streaming (un fallback oculto implicaría costo doble invisible, una respuesta distinta que reemplaza a la ya mostrada en vivo, y enmascaramiento de problemas del proveedor). Única excepción: el sentinel `ErrStreamingNotSupported` (capacidad ausente, detectada antes de emitir tokens) degrada automáticamente a `Chat`. Los deltas de texto se publican a los clientes como notificación `message.delta.event` (payload aditivo; no altera los shapes de `message.event`); la entrega es best-effort (broadcast no bloqueante, descarta con warn ante backpressure). El valor del flag se lee al iniciar el daemon — sin hot-reload: un turno en vuelo no cambia de comportamiento a mitad de camino.

### RF-3. Gestión de contexto y memoria
- RF-3.1 Debe mantener memoria persistente entre sesiones (decisiones de arquitectura, convenciones del proyecto, hechos "anclados").
- RF-3.2 Debe implementar recuperación selectiva de contexto (retrieval) en vez de enviar el historial completo en cada turno.
- RF-3.3 Debe compactar/resumir sesiones largas de forma jerárquica y progresiva, preservando hechos ancla sin comprimir. Los resúmenes son **aproximados por definición** y se presentan así al modelo: nunca son fuente de verdad — ante un detalle verificable (contenido de un archivo, un commit, el resultado de un comando) prevalece la verificación. Medido: un modelo de 1.5B confunde a veces lo sugerido con lo hecho.
- RF-3.4 El usuario debe poder inspeccionar y editar manualmente qué hay en la memoria persistente (transparencia total, sin caja negra).
- RF-3.6 (agregado 2026-10-03) Debe ofrecer un mapa del repositorio (símbolos declarados y su ubicación) acotado por presupuesto de tokens, y una herramienta de búsqueda de símbolos, para que el agente ubique código sin explorar el árbol de archivos a ciegas.
- RF-3.5 Debe existir un mecanismo de "anclaje" explícito: el usuario o el agente pueden marcar un hecho/decisión como permanente.

### RF-4. Skills y auto-aprendizaje
- RF-4.1 Debe soportar la creación, carga e instalación de "skills" (paquetes de instrucciones + scripts reutilizables), similar al patrón `SKILL.md`.
- **RF-4.1.1 Debe proporcionar un wizard CLI interactivo (`forge skill new`) que guíe al usuario paso a paso para crear una skill nueva: nombre, descripción, categoría, frontmatter YAML, plantilla de instrucciones, scripts opcionales, y validación del `SKILL.md` resultante.**
- RF-4.2 Las skills deben cargarse de forma perezosa (lazy-load): solo se inyectan en el contexto cuando son relevantes a la tarea detectada.
- RF-4.3 El sistema debe poder proponer la creación de una nueva skill a partir de una trayectoria de tarea exitosa repetida (minería de patrones).
- RF-4.4 Debe existir un flujo de aprobación humana antes de que una skill auto-generada quede activa (nunca auto-aprendizaje sin supervisión).

### RF-5. Plugins y extensibilidad
- RF-5.1 Debe soportar plugins de terceros que añadan: nuevas herramientas, nuevos proveedores de LLM, nuevos comandos de CLI, o paneles de GUI.
- RF-5.2 Los plugins deben ejecutarse en un entorno aislado (sandbox) del proceso principal.
- RF-5.3 Debe existir un manifiesto de plugin (metadatos, permisos solicitados, versión, dependencias).
- **RF-5.3.1 Debe proporcionar un wizard CLI interactivo (`forge plugin new`) que guíe al usuario paso a paso para crear un plugin nuevo: nombre, versión, descripción, permisos solicitados (FS/shell/git/red), punto de entrada WASM, dependencias, y generación del `manifest.toml` y estructura de directorios inicial.**
- RF-5.4 El sistema debe permitir habilitar/deshabilitar plugins sin recompilar el binario principal.
- RF-5.5 (agregado 2026-10-03) Debe integrarse con el Model Context Protocol en ambos sentidos: usar herramientas de servidores MCP externos — sujetas a verificación de procedencia (RNF-4.6) y al modelo de permisos (RNF-4.1) — y exponer sus propias capacidades como servidor MCP. Es una segunda vía de extensión junto a los plugins WASM (RNF-3.2): MCP cubre integraciones que requieren procesos o red, que el sandbox WASM no permite.

### RF-6. CLI
- RF-6.1 CLI minimalista con comandos core: iniciar sesión, listar sesiones, adjuntar a sesión en curso, ejecutar tarea puntual (one-shot), **gestionar plugins/skills (`forge plugin new`, `forge skill new`, `forge plugin list`, `forge skill list`, `forge plugin enable/disable`, `forge skill enable/disable`)**, gestionar proveedores.
- RF-6.2 Debe soportar modo interactivo (TUI) y modo no interactivo (scriptable, para CI/CD o cron).
- RF-6.3 Salida en modo no interactivo debe soportar formato JSON para integración con otras herramientas.

### RF-6 (detalle). Wizards CLI para creación de plugins y skills

Para reducir la fricción de crear extensiones válidas, forge incluye wizards interactivos:

#### `forge plugin new`

Wizard paso a paso que genera la estructura completa de un plugin:

```
$ forge plugin new
? Nombre del plugin: mi-plugin
? Versión inicial: 0.1.0
? Descripción: Plugin para integración con API externa
? Punto de entrada WASM: ./target/wasm32-wasi/release/plugin.wasm
? Permisos requeridos:
  [x] fs.read
  [ ] fs.write
  [x] shell.exec (comandos: curl, jq)
  [ ] git
  [ ] net (hosts: api.ejemplo.com)
? Dependencias: (opcional, separadas por comas)
? Directorio destino: ./forge-plugins/mi-plugin
? Confirmar creación? (Y/n)
```

**Genera:**
```
forge-plugins/mi-plugin/
├── manifest.toml
├── src/
│   └── lib.rs (o main.go, main.py, etc.)
├── Cargo.toml (o go.mod, pyproject.toml, etc.)
├── README.md
└── .gitignore
```

#### `forge skill new`

Wizard para crear skills siguiendo el patrón `SKILL.md`:

```
$ forge skill new
? Nombre de la skill: code-review-style
? Categoría: review / testing / docs / refactor / custom
? Descripción breve: Guía de estilo para code reviews
? Descripción detallada (para embedding/semántico): ...
? Palabras clave de activación (comma-separated): code review, style, PR
? Incluir script de validación? (Y/n)
? Script de validación (opcional): ./scripts/check-style.sh
? Directorio destino: .forge/skills/code-review-style/
? Confirmar creación? (Y/n)
```

**Genera:**
```
.forge/skills/code-review-style/
├── SKILL.md
├── instructions.md
├── scripts/
│   └── check-style.sh
└── examples/
    ├── good-example.md
    └── bad-example.md
```

#### Validaciones automáticas

Ambos wizards ejecutan validaciones al final:
- **Plugin:** `manifest.toml` válido (TOML sintáctico, campos requeridos, permisos conocidos, entrypoint existe)
- **Skill:** `SKILL.md` válido (frontmatter YAML completo, campos requeridos, descripción no vacía)
- Estructura de directorios creada correctamente
- Sin conflictos de nombres en el registro local

#### Integración con aprobación (RNF-4.4, RNF-4.6)

- Plugins/skills **creados localmente** se marcan como `source: local` → no requieren firma/checksum para cargarse
- Plugins/skills **instalados de fuente externa** requieren verificación de checksum/firma antes de primera carga (RNF-4.6)
- Skills auto-generadas (RF-4.3) pasan por el mismo flujo de aprobación humana (RF-4.4)

#### Complementos CLI relacionados

| Comando | Descripción |
|---------|-------------|
| `forge plugin list` | Lista plugins instalados (local + externos) con estado |
| `forge skill list` | Lista skills disponibles con categoría y estado |
| `forge plugin enable <name>` | Habilita plugin (agrega a config activo) |
| `forge plugin disable <name>` | Deshabilita plugin |
| `forge plugin remove <name>` | Elimina plugin (con confirmación) |
| `forge skill enable <name>` | Activa skill para lazy-load |
| `forge skill disable <name>` | Desactiva skill |
| `forge plugin validate <path>` | Valida manifest.toml y estructura |
| `forge skill validate <path>` | Valida SKILL.md y estructura |

---

### RF-7. GUI web (opcional, desacoplada)
- RF-7.1 Debe existir un modo servidor que exponga una API sobre la cual una GUI web pueda conectarse (local o remota).
- RF-7.2 La GUI web debe ser un cliente más de la misma API que usa el CLI — no un sistema paralelo con lógica propia.
- RF-7.3 Debe soportar visualización de diffs, árbol de conversación/sesión, y estado de agentes en ejecución.
- RF-7.4 Acceso remoto protegible con autenticación (password de UI como mínimo viable).

### RF-8. Desarrollo guiado por especificación (SDD)
- RF-8.1 El usuario debe poder definir una especificación (documento de spec) como artefacto de primera clase del proyecto.
- RF-8.2 El agente debe poder descomponer una spec en tareas ejecutables y trackeables.
- RF-8.3 El sistema debe poder validar (o al menos señalar divergencias) entre la implementación actual y la especificación vigente.
- RF-8.4 Los cambios de spec deben quedar versionados (historial de decisiones, no solo el estado final).

### RF-9. Gestión de sesiones
- RF-9.1 Debe soportar branching de sesiones (ramificar una conversación en un punto dado para explorar caminos alternativos).
- RF-9.2 Debe permitir fusionar (merge) el resultado de ramas alternativas o de ejecuciones multi-modelo. **Semántica (v1): append-tail** — los mensajes de la rama posteriores a su punto de bifurcación se agregan, en orden, al final de la sesión destino; no hay resolución de conflictos (dos ramas que se contradicen quedan ambas en el historial, y es el usuario o el modelo quien decide). El destino registra origen, fecha y cantidad de mensajes fusionados. Los cambios de archivos de cada rama se fusionan con git, no con este mecanismo.
- RF-9.3 Debe permitir ejecutar la misma tarea en paralelo contra varios modelos/proveedores y comparar resultados.

### RF-10. Integración con control de versiones y entorno
- RF-10.1 Debe integrarse con git: lectura de diffs, creación de commits, gestión de branches por tarea/worktree.
- RF-10.2 Debe soportar ejecución de comandos de shell dentro del workspace, con visibilidad completa de su salida para el agente.
- RF-10.4 (agregado 2026-10-03) Debe poder deshacer los cambios de archivos de un turno del agente (`forge undo`), con instantáneas tomadas antes de la primera herramienta que modifica archivos, sin tocar el historial git del proyecto.
- RF-10.3 (Deseable, no v1) Integración con issues/PRs de GitHub u otro forge.

### RF-11. Ejecución autónoma de principio a fin (One-shot + SPEC + HITL)

**Objetivo:** dado un único artefacto de arranque (un archivo de "run manifest") que combine (a) una instrucción one-shot, (b) una SPEC del proyecto/tarea, y (c) una configuración de puntos de intervención humana (HITL), el sistema debe poder ejecutar el proyecto completo — descomponer, implementar, testear, depurar y corregir — sin detenerse, deteniéndose **únicamente** en los checkpoints HITL definidos o en condiciones extraordinarias de seguridad/ambigüedad.

- RF-11.1 El sistema debe aceptar un **run manifest** como único punto de entrada de una ejecución autónoma (ver formato propuesto en §7).
- RF-11.2 El run manifest debe permitir declarar, como mínimo:
  - la instrucción/objetivo de alto nivel (one-shot),
  - la referencia o contenido de la SPEC a cumplir,
  - los checkpoints HITL (momentos explícitos donde el sistema debe pausar y esperar aprobación/input humano),
  - los límites de autonomía (reintentos máximos, presupuesto de tokens/costo, tiempo máximo, alcance de archivos/directorios permitidos).
- RF-11.3 El sistema debe descomponer la SPEC en una lista de tareas atómicas y verificables (cada una con un criterio de "hecho" explícito: tests que pasan, lint limpio, build exitoso, o el criterio que la SPEC declare).
- RF-11.4 Para cada tarea, el sistema debe ejecutar un **ciclo de auto-corrección acotado**: implementar → validar → si falla, analizar el error → corregir → volver a validar, hasta un límite de reintentos configurable por tarea (no reintentos infinitos).
- RF-11.5 Si una tarea agota sus reintentos sin éxito, el sistema debe tratarlo como un **checkpoint HITL implícito** (caso extraordinario) y pausar, en vez de continuar en un estado roto o marcar la tarea como completada sin estarlo.
- RF-11.6 El sistema debe continuar automáticamente a la siguiente tarea de la SPEC solo cuando la tarea actual cumple su criterio de "hecho" — o cuando un HITL explícito la aprobó pese a no cumplirlo.
- RF-11.7 Al alcanzar un checkpoint HITL (explícito o extraordinario), el sistema debe: detener la ejecución, presentar un resumen claro del estado (qué se hizo, qué falta, diffs relevantes, motivo de la pausa), y esperar input humano antes de continuar — nunca debe inferir una aprobación implícita.
- RF-11.8 El sistema debe registrar un log/auditoría completo y reanudable de la corrida: si el proceso se interrumpe (crash, corte, cierre de cliente), debe poder reanudarse desde el último estado consistente sin reprocesar tareas ya completadas.
- RF-11.9 El sistema debe soportar niveles de autonomía configurables por proyecto o por tarea (ver tabla en §7.2), desde "pausa después de cada tarea" hasta "solo pausa en casos extraordinarios".
- RF-11.10 Al finalizar todas las tareas de la SPEC, el sistema debe generar un reporte final: tareas completadas, tareas que requirieron intervención, **supuestos asumidos para resolver ambigüedad Tier 2 (§3.7) sin pausar**, desviaciones respecto a la SPEC original (si las hubo y por qué), y estado de validación global (todos los tests pasan, build limpio, etc.).

**Casos extraordinarios que deben pausar la ejecución aunque no sean un HITL declarado explícitamente:**
- Acción irreversible o destructiva fuera del alcance declarado (borrado masivo, force-push, migración de datos sin reversión posible).
- Ambigüedad genuina en la SPEC (Tier 3 — ver §3.7) que no puede resolverse sin asumir una decisión de producto/negocio no delegada al agente.
- Detección de una acción que requeriría credenciales, permisos o alcance no autorizados en el manifest.
- Reintentos agotados en una tarea (ver RF-11.5).
- Presupuesto de tiempo, tokens o costo excedido respecto al límite declarado en el manifest.
- Cualquier operación que el modelo de permisos (RNF-4.1) clasifique como fuera de la lista allow.
- Contenido no confiable (RNF-4.5) que contenga instrucciones dirigidas al agente (posible inyección de prompt) — se trata como dato sospechoso, nunca se actúa sobre lo que "pide", y se marca para revisión.

---

## 2. Requerimientos no funcionales (RNF)

### RNF-1. Rendimiento
- RNF-1.1 Tiempo de arranque en frío (cold start) del daemon/core: objetivo < 200ms. **Se mide** desde el lanzamiento del proceso hasta que el daemon acepta conexiones TCP (mediana de ≥10 arranques, sondeo de conexión cada ≤5 ms — no con un cliente HTTP, que en Windows añade ~2 s por conexión rechazada). Los backends opcionales (embeddings, servidores MCP) pueden seguir calentando en segundo plano y no cuentan, siempre que el daemon atienda con un respaldo funcional mientras tanto.
- RNF-1.2 Latencia añadida por el harness (overhead sobre el tiempo de inferencia del modelo) debe ser despreciable (< 50ms por turno en condiciones normales). **Se mide** como duración del turno menos el tiempo de las llamadas al modelo, en mediana, con herramientas de ejecución instantánea — el tiempo propio de un comando de shell lento no es overhead del harness. Incluye armado de contexto, retrieval, permisos, persistencia y snapshot.
- RNF-1.3 Uso de memoria del proceso core en reposo: objetivo < 100MB.
- RNF-1.4 Debe soportar sesiones de larga duración (horas/días) sin degradación de rendimiento ni fugas de memoria. **Criterio verificable:** en una sesión de ≥200 turnos, la mediana del overhead (RNF-1.2) de los últimos turnos no supera 4× la de los primeros ni los 50 ms, y el heap en uso no crece más de 5× (salvo que se mantenga bajo 20 MB).
- RNF-1.5 **Concurrencia realista sobre el Perfil A (§5):** en el hardware de referencia interactivo (CPU multinúcleo sin GPU discreta utilizable para inferencia), no existe ninguna ruta de paralelismo físico real — un único proceso de modelo consume los núcleos disponibles de forma serializada. Todo lo que RF-1.2/RF-1.3 llaman "concurrencia de agentes" se traduce, en este perfil, en una cola con prioridad hacia ese único proceso de inferencia — nunca en ejecución simultánea. El planificador de solicitudes con prioridad es, en este hardware, el único mecanismo real de "multiagente", y debe diseñarse asumiendo cero paralelismo físico como caso normal, no como degradación de un caso ideal.
- RNF-1.6 **Reserva de núcleos:** el proceso de inferencia local no debe reservar por defecto el 100% de los núcleos físicos disponibles — debe dejar margen (ej. 1-2 núcleos) para que el equipo siga siendo usable para otras tareas mientras el harness trabaja, salvo que el usuario indique explícitamente lo contrario (ej. una corrida autónoma nocturna sin uso concurrente del equipo).

### RNF-2. Eficiencia de contexto/tokens (requisito diferenciador del proyecto)
- RNF-2.1 El sistema debe medir y reportar tokens consumidos por turno, por sesión y por proveedor.
- RNF-2.2 El diseño de contexto debe maximizar hits de prompt-caching de cada proveedor (orden estable: sistema → herramientas → memoria → historial variable).
- RNF-2.3 Objetivo cuantitativo: reducción de ≥40% en tokens de contexto respecto a un enfoque naive de "historial completo" en sesiones de más de 20 turnos. **Línea base naive:** el mismo system prompt y las mismas herramientas, con todos los mensajes de la sesión en cada turno — sin ventana, sin retrieval, sin compactación, sin anclas —, sobre la misma secuencia de turnos que el brazo medido. Se mide sobre el payload del request (tokens de prompt), independiente del modelo.
- RNF-2.4 **Reutilización de KV-cache en inferencia local** (el equivalente al prompt-caching remoto de RNF-2.2, pero a nivel del propio servidor de inferencia — ej. cache de prefijos en llama.cpp/vLLM): el diseño de contexto debe mantener un prefijo estable (mismo system prompt + mismo orden de herramientas) por sesión/tarea. Cambiar el prefijo entre turnos de una misma sesión anula la ganancia de velocidad más importante disponible en modelos locales — más relevante aún que el prompt-caching remoto, porque en hardware estándar no hay margen de sobra que absorba el recálculo.
- RNF-2.5 **Techo de contexto objetivo, no máximo técnico:** en el Perfil A (CPU-only), el objetivo es mantener el contexto de trabajo de turnos interactivos en el orden de **4.000-8.000 tokens** — no el máximo que el modelo declare soportar. El tiempo de prefill en CPU escala de forma mucho más perceptible con el tamaño del contexto que en APIs con aceleración dedicada; un contexto que "cabe" pero está cerca del límite puede ser la diferencia entre segundos y minutos de espera. Contextos mayores (ej. 16k+) quedan reservados para el Perfil B / corridas en segundo plano (RF-11), donde la tolerancia a tiempo de espera ya es explícita (`budget.max_wall_clock`).

### RNF-3. Modularidad y mantenibilidad
- RNF-3.1 El core debe ser independiente de cualquier proveedor de LLM específico (sin acoplamiento a un SDK propietario en el núcleo).
- RNF-3.2 Toda funcionalidad nueva debe poder añadirse vía plugin sin modificar el core, salvo que amplíe el contrato de la API interna.
- RNF-3.3 Cobertura de tests de integración sobre el contrato de la API interna (no solo unitarios).

### RNF-4. Seguridad

**Modelo de confianza (superficies no confiables):** el proveedor de LLM se asume semi-confiable — recibe contexto para operar, pero nunca debe recibir secretos ni datos fuera de lo declarado. Todo contenido que el sistema ingiere desde fuera del propio operador — archivos de terceros en el repo, resultados de búsqueda web, salidas de herramientas MCP, plugins/skills de origen externo — se asume **no confiable** y puede contener instrucciones adversariales dirigidas al agente (inyección de prompt), incluyendo texto que reclame autoridad del usuario, de "sistema", o del proveedor del modelo.

- RNF-4.1 Ejecución de shell y acceso a filesystem deben pasar por un modelo de permisos explícito con **postura por defecto deny** (nada permitido salvo lo declarado) — no acceso irrestricto por defecto.
- RNF-4.2 Plugins deben ejecutarse con privilegios mínimos y declarar permisos requeridos.
- RNF-4.3 Ningún dato de sesión/proyecto debe salir del entorno local sin acción explícita del usuario (por defecto: local-first).
- RNF-4.4 Secrets (API keys, tokens) nunca en texto plano en logs ni en el store de memoria persistente **ni en el contexto enviado a un proveedor de LLM** — deben redactarse antes de que la salida de una herramienta (comando de shell, respuesta HTTP, etc.) entre al contexto del modelo.
- RNF-4.5 Contenido de fuentes no confiables debe tratarse siempre como **datos, nunca como instrucciones** — cualquier acción que ese contenido "solicite" debe pasar por el mismo modelo de permisos que cualquier otra acción del agente; el sistema no ejecuta instrucciones encontradas dentro de archivos, páginas web, o salidas de herramientas. Esto incluye el texto **derivado** de esas fuentes que el propio harness vuelve a inyectar en el contexto (fragmentos de retrieval, resúmenes de compactación, memoria propuesta): entra cercado y escapado como dato, bajo un encabezado escrito por el harness — nunca como texto libre con autoridad de instrucción (p. ej. un mensaje de sistema sin cercar). Un resumen generado por modelo que el detector de inyección marque no se almacena tal cual.
- RNF-4.6 Plugins y skills de origen externo (no creados por el propio usuario) requieren verificación de procedencia (checksum/firma) antes de cargarse, y aprobación humana explícita antes de su primera ejecución — el registro de plugins (§3.5) debe distinguir "creado localmente" de "instalado de fuente externa".
- RNF-4.7 La ejecución de shell del propio core (no solo de plugins) debe correr con aislamiento adicional a nivel de sistema operativo — el modelo de permisos declara *qué* está autorizado; el aislamiento de SO es la segunda capa que contiene el daño si el modelo de permisos falla o es evadido. **Matiz por plataforma (§6):** en Linux, exigible desde v0 vía seccomp/Landlock (primitivas estables y documentadas). En macOS, `sandbox-exec` queda descartado por ser una API no documentada/en desuso de Apple — v0 en macOS se limita al modelo de permisos (RNF-4.1) sin aislamiento de SO, con el aislamiento duro diferido a v1 pendiente de un mecanismo estable.
- RNF-4.8 Debe existir una **parada de emergencia** accesible desde cualquier cliente (CLI/GUI) que detenga de inmediato cualquier ejecución en curso — interactiva o autónoma — dejando el estado en el último punto consistente conocido (ligado a RNF-8.4).
- RNF-4.9 Todo adaptador de proveedor (§3.1) debe operar con una allowlist de red explícita como comportamiento **por defecto del sistema en cualquier modo** — no solo durante ejecución autónoma (RF-11).
- RNF-4.10 El log/auditoría de una corrida (RNF-6.2, RF-11.8) debe ser a prueba de manipulación (append-only o con encadenamiento verificable) cuando el proyecto tenga clasificación `regulado` o `datos-sensibles` (RNF-9) — un log editable después de los hechos no sirve como evidencia de cumplimiento.
- RNF-4.11 El acceso remoto a la GUI web (RF-7.4) debe viajar sobre transporte cifrado por defecto (TLS o túnel cifrado) — una contraseña sobre una conexión sin cifrar es una traba mínima, no un control de acceso remoto aceptable.
- RNF-4.13 (agregado 2026-10-03) Además de permitir/denegar, el modelo de permisos debe soportar reglas **ask**: la acción se pausa y se pregunta al usuario en el cliente conectado; sin cliente que responda, se deniega al vencer el plazo (nunca se aprueba por omisión).
- RNF-4.12 Un hecho derivado de contenido no confiable que el sistema proponga anclar en memoria persistente (RF-3.5) o destilar en una skill (RF-4.3) debe pasar por el mismo flujo de aprobación humana que ya exige RF-4.4 para skills auto-generadas — nunca se ancla ni se promueve automáticamente solo porque "funcionó una vez".

### RNF-5. Portabilidad
- RNF-5.1 Debe correr en Linux, macOS (Intel y Apple Silicon) y, deseable, Windows.
- RNF-5.2 No debe depender de servicios de infraestructura externos obligatorios (todo debe poder correr 100% local).

### RNF-6. Observabilidad
- RNF-6.1 Logging estructurado (JSON) con niveles configurables.
- RNF-6.2 Grabación y replay de sesiones completas (para debugging y para minería de skills).
- RNF-6.3 Métricas de costo estimado por sesión/proveedor cuando aplique (modelos de pago).

### RNF-7. Usabilidad / adaptabilidad al operador
- RNF-7.1 Debe soportar cambios de dirección a mitad de tarea sin perder el estado ya construido (no exige reiniciar sesión ante un cambio de idea).
- RNF-7.2 Configuración por proyecto debe ser versionable junto al código (archivo de config en el repo).

### RNF-8. Autonomía segura (ejecución desatendida) — ligado a RF-11
- RNF-8.1 Toda ejecución en modo autónomo debe operar sobre un worktree/branch aislado — nunca directamente sobre la rama base sin una aprobación HITL explícita de merge.
- RNF-8.2 Debe existir un **piso de seguridad no configurable** (no deshabilitable desde el run manifest, sin excepción) para: operaciones git destructivas (force-push, `reset --hard`, borrado de branch), acceso a credenciales/secrets fuera de lo declarado, llamadas de red fuera de la allowlist, y exceso de presupuesto (tiempo/tokens/costo). Esto existe independientemente de lo que el usuario configure en HITL — el manifest puede *añadir* checkpoints, nunca *quitar* estos.
- RNF-8.3 El criterio de "tarea completada" no puede reducirse a "el proceso no arrojó error". Debe incluir verificación positiva del comportamiento esperado (tests que ejercitan el criterio declarado en la SPEC, no solo ausencia de excepción) — de lo contrario el agente puede optimizar hacia la métrica equivocada (p. ej., debilitar o comentar un test para que "pase").
- RNF-8.4 Cada tarea completada en modo autónomo debe quedar como un commit atómico y reversible de forma independiente — nunca un commit gigante al final de la corrida.

- RNF-8.5 (agregado 2026-10-03) Mientras una corrida aislada (RNF-8.1) usa el workspace, ninguna otra sesión puede modificarlo (escrituras, shell, git que cambie estado); las lecturas siguen permitidas. El bloqueo se libera al terminar, cancelar o fusionar la corrida, y se restaura tras reiniciar el daemon si la corrida quedó pendiente.

### RNF-9. Clasificación de sensibilidad del proyecto — techo de autonomía
- RNF-9.1 Cada proyecto debe declarar, una única vez en su configuración versionada (no en cada run manifest individual), una clasificación de sensibilidad: `general`, `regulado`, o `datos-sensibles`.
- RNF-9.2 Esta clasificación actúa como un **techo** sobre el nivel de autonomía permitido (§7.2), independiente de lo que solicite un run manifest puntual:
  - `datos-sensibles` (ej. información de salud u otra especialmente protegida) → techo duro en `supervised`, sin excepción configurable.
  - `regulado` (ej. cumplimiento normativo, procesos con validez legal) â†’ techo en `checkpoint`, con el checkpoint `pre-merge` fijo en `required: true`, no removible.
  - `general` → sin techo adicional; sigue la progresión normal hasta `autonomous` (§7.2).
- RNF-9.3 Un run manifest que solicite un nivel de autonomía por encima del techo de su proyecto debe ser **rechazado** en la fase de validación (paso 1 del ciclo, §3.6) — nunca degradado silenciosamente ni ejecutado con una advertencia ignorable.
- RNF-9.4 Cambiar la clasificación de sensibilidad de un proyecto debe requerir una acción humana explícita, registrada (quién, cuándo, por qué) — nunca una decisión que el propio agente pueda tomar o proponer como "hecha".

### RNF-10. Validación empírica de rendimiento
- RNF-10.1 Debe existir un banco de pruebas repetible que mida, como mínimo: tokens/segundo de generación, latencia al primer token (TTFT), tiempo de prefill por tamaño de contexto, y tiempo total de pared para un conjunto de tareas representativas.
- RNF-10.2 El banco de pruebas debe correr sobre los **dos perfiles de hardware de referencia definidos en §5** (Perfil A — interactivo/estándar; Perfil B — batch/autónomo), reportando métricas por separado para cada uno — no un promedio combinado que oculte la brecha real entre ambos.
- RNF-10.3 Los objetivos cuantitativos de RNF-1 y RNF-2 (≥40% de reducción de tokens, cold-start <200ms, etc.) deben validarse contra este banco de pruebas antes de darse por cumplidos — son métricas verificables, no afirmaciones de diseño.

---

## 3. Arquitectura general

### 3.1 Vista de alto nivel

```
                         ┌───────────────────────────┐
                         │         CLIENTES           │
                         │                            │
                         │  ┌──────┐  ┌─────────────┐ │
                         │  │  CLI │  │  GUI Web    │ │
                         │  │(TUI) │  │ (browser)   │ │
                         │  └───┬──┘  └──────┬──────┘ │
                         │      │            │        │
                         │      │  ┌─────────┘        │
                         │      │  │  (futuro: móvil,  │
                         │      │  │   VS Code ext.)   │
                         └──────┼──┼───────────────────┘
                                │  │
                                ▼  ▼
                    ┌─────────────────────────┐
                    │   API INTERNA (RPC)     │   ← contrato único,
                    │  JSON-RPC / WebSocket   │     todos los clientes
                    │  eventos en streaming   │     hablan lo mismo
                    └───────────┬─────────────┘
                                │
                                ▼
        ┌───────────────────────────────────────────────────┐
        │                    CORE (daemon)                   │
        │                                                      │
        │  ┌───────────────┐   ┌────────────────────────┐    │
        │  │ Orquestador de │   │  Gestor de Contexto     │    │
        │  │    Agentes     │◄─►│  (retrieval, compact.,  │    │
        │  │ (supervisor +  │   │   anclaje, resumen)     │    │
        │  │  subagentes)   │   └───────────┬────────────┘    │
        │  └───────┬────────┘               │                 │
        │          │                        ▼                 │
        │          │              ┌──────────────────┐        │
        │          │              │  Memoria Persist. │        │
        │          │              │  SQLite + vector   │        │
        │          │              └──────────────────┘        │
        │          ▼                                          │
        │  ┌────────────────┐   ┌────────────────────────┐    │
        │  │ Motor de Tools  │   │  Registro de Skills     │    │
        │  │ (MCP + nativas) │◄─►│  (lazy-load, manifest)  │    │
        │  └───────┬────────┘   └────────────────────────┘    │
        │          │                                          │
        │          ▼                                          │
        │  ┌────────────────┐   ┌────────────────────────┐    │
        │  │ Sandbox/Permisos│   │  Registro de Plugins    │    │
        │  │  (WASM runtime) │◄─►│  (manifest, versiones)  │    │
        │  └────────────────┘   └────────────────────────┘    │
        │                                                      │
        └───────────────────────┬──────────────────────────────┘
                                 │
                                 ▼
                 ┌───────────────────────────────┐
                 │   ADAPTADORES DE PROVEEDOR      │
                 │                                 │
                 │  ┌──────────┐  ┌─────────────┐ │
                 │  │ OpenAI-  │  │  Anthropic  │ │
                 │  │compatible│  │  Messages   │ │
                 │  │(Ollama,  │  │  API        │ │
                 │  │ llama.cpp│  │             │ │
                 │  │ vLLM,LM  │  │             │ │
                 │  │ Studio)  │  │             │ │
                 │  └──────────┘  └─────────────┘ │
                 │  ┌──────────┐                   │
                 │  │  Gemini  │   (+ futuros)      │
                 │  └──────────┘                   │
                 └───────────────────────────────┘
```

### 3.2 Flujo de un turno de conversación

```
Usuario escribe mensaje
        │
        ▼
┌───────────────────┐
│ 1. Clasificación   │  → detecta tipo de tarea, complejidad,
│    de intención    │     decide si delega a subagente
└─────────┬──────────┘
          ▼
┌───────────────────┐
│ 2. Ensamblado de   │  → recupera SOLO fragmentos relevantes
│    contexto        │     (retrieval vectorial + resumen
│    (NO historial   │     rodante + hechos anclados)
│    completo)        │
└─────────┬──────────┘
          ▼
┌───────────────────┐
│ 3. Carga de skills  │  → solo las skills activadas por la
│    y tools           │     tarea detectada (lazy-load)
│    relevantes        │
└─────────┬──────────┘
          ▼
┌───────────────────┐
│ 4. Orden de layout  │  → sistema fijo → tools → memoria →
│    optimizado para  │     historial variable (maximiza
│    prompt-caching   │     cache hits del proveedor)
└─────────┬──────────┘
          ▼
┌───────────────────┐
│ 5. Llamada al       │
│    proveedor LLM    │
│    (streaming)      │
└─────────┬──────────┘
          ▼
┌───────────────────┐
│ 6. Tool-calling     │  → ejecuta vía Motor de Tools
│    (si aplica)      │     (sandbox WASM si es plugin)
└─────────┬──────────┘
          ▼
┌───────────────────┐
│ 7. Persistencia     │  → guarda turno, actualiza memoria,
│    incremental       │     evalúa si corresponde compactar
└─────────┬──────────┘
          ▼
┌───────────────────┐
│ 8. Streaming de     │
│    respuesta al     │
│    cliente          │
└────────────────────┘
```

### 3.3 Modelo de compactación jerárquica de contexto

```
┌─────────────────────────────────────────────────────────┐
│                  CONTEXTO DE UNA SESIÓN                   │
│                                                             │
│  ┌───────────────────────────────────────────────────┐   │
│  │ NIVEL 0 — Hechos anclados (nunca se comprimen)      │   │
│  │  · decisiones de arquitectura                       │   │
│  │  · convenciones de código del proyecto               │   │
│  │  · specs vigentes                                    │   │
│  └───────────────────────────────────────────────────┘   │
│  ┌───────────────────────────────────────────────────┐   │
│  │ NIVEL 1 — Resumen de proyecto (rodante, se          │   │
│  │  actualiza incrementalmente turno a turno)           │   │
│  └───────────────────────────────────────────────────┘   │
│  ┌───────────────────────────────────────────────────┐   │
│  │ NIVEL 2 — Resumen de sesión actual (se recompacta   │   │
│  │  cada N turnos o al superar umbral de tokens)        │   │
│  └───────────────────────────────────────────────────┘   │
│  ┌───────────────────────────────────────────────────┐   │
│  │ NIVEL 3 — Turnos recientes en crudo (ventana         │   │
│  │  deslizante, sin comprimir)                          │   │
│  └───────────────────────────────────────────────────┘   │
│                                                             │
│  Retrieval selectivo: en cada turno, se recuperan          │
│  fragmentos de niveles 0-2 SOLO si son semánticamente       │
│  relevantes a la tarea actual (embedding query contra       │
│  el store vectorial) — no se inyectan completos por         │
│  defecto.                                                   │
└─────────────────────────────────────────────────────────┘
```

### 3.4 Orquestación de agentes (supervisor / subagentes)

```
                    ┌───────────────────────┐
                    │  Agente Orquestador     │
                    │  (contexto completo del │
                    │   proyecto + spec)      │
                    └───────────┬────────────┘
                                 │  delega tarea acotada
              ┌──────────────────┼──────────────────┐
              ▼                  ▼                  ▼
     ┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
     │  Subagente A      │ │  Subagente B      │ │  Subagente C      │
     │  (ctx acotado:    │ │  (ctx acotado:    │ │  (ctx acotado:    │
     │   solo archivos    │ │   solo tests       │ │   solo docs/spec  │
     │   del módulo X)    │ │   relevantes)      │ │   afectada)       │
     │  tools: {read,     │ │  tools: {run_test, │ │  tools: {read,    │
     │   write, grep}     │ │   read}            │ │   write}          │
     └────────┬─────────┘ └────────┬─────────┘ └────────┬─────────┘
              │                    │                    │
              └────────────────────┼────────────────────┘
                                   ▼
                    ┌───────────────────────┐
                    │  Resultado consolidado  │
                    │  vuelve al orquestador  │
                    │  (solo el resumen, no    │
                    │   el contexto completo   │
                    │   del subagente)         │
                    └───────────────────────┘
```

Cada subagente recibe **solo** el contexto necesario para su sub-tarea — esto es en sí mismo un mecanismo de eficiencia de tokens: el orquestador nunca carga en su propio contexto el detalle completo de lo que hizo cada subagente, solo el resultado consolidado.

### 3.5 Plugins y skills (extensibilidad)

```
┌───────────────────────────────────────────────────────┐
│                    REGISTRO DE PLUGINS                  │
│                                                          │
│  manifest.toml (por plugin):                            │
│    name, version, permissions[], entrypoint.wasm         │
│                                                          │
│  ┌────────────┐   ┌────────────┐   ┌────────────┐      │
│  │ Plugin: git│   │Plugin: docker│  │Plugin: jira│      │
│  │  extendido  │   │  management  │  │ integration │      │
│  │  (WASM)     │   │  (WASM)      │  │  (WASM)     │      │
│  └────────────┘   └────────────┘   └────────────┘      │
│                                                          │
│  Cada plugin corre en runtime WASM aislado (wasmtime/    │
│  wasmer) — no accede a filesystem/red salvo lo que el     │
│  manifest declara y el usuario aprueba.                   │
└───────────────────────────────────────────────────────┘

┌───────────────────────────────────────────────────────┐
│                    REGISTRO DE SKILLS                    │
│                                                          │
│  .forge/skills/                                          │
│    ├── deploy-checklist/SKILL.md                          │
│    ├── code-review-style/SKILL.md                         │
│    └── db-migration-pattern/SKILL.md                      │
│                                                          │
│  Cada SKILL.md se indexa (embedding del frontmatter/      │
│  descripción) → se activa solo si la tarea detectada       │
│  matchea semánticamente con la descripción de la skill.    │
│                                                          │
│  Minería de skills: trayectorias exitosas repetidas se     │
│  destilan periódicamente en propuestas de nuevas skills,   │
│  presentadas al usuario para aprobación antes de activarse.│
└───────────────────────────────────────────────────────┘
```

### 3.6 Ciclo de ejecución autónoma (Run Manifest — RF-11)

```
   [Run Manifest: one-shot + SPEC + config HITL]
                    │
                    ▼
        ┌───────────────────────┐
        │ 1. Parseo y validación  │  → valida schema, resuelve
        │    del manifest          │     spec_ref, verifica límites
        └───────────┬─────────────┘
                    ▼
        ┌───────────────────────┐
        │ 2. Aislamiento de       │  → crea worktree/branch dedicado
        │    entorno (git)        │     (NUNCA sobre base_branch)
        └───────────┬─────────────┘
                    ▼
        ┌───────────────────────┐
        │ 3. Descomposición de    │  → SPEC → lista de tareas
        │    la SPEC en tareas    │     atómicas + criterio "hecho"
        └───────────┬─────────────┘
                    ▼
         ¿HITL "post-decomposition"?──Sí──► [PAUSA: revisión humana
                    │                         del plan de tareas]
                    No
                    ▼
     ┌──────────────────────────────────────────────┐
     │           LOOP por cada tarea de la SPEC         │
     │                                                   │
     │   a. Implementar                                  │
     │        │                                          │
     │        ▼                                          │
     │   b. Validar (tests/lint/build/criterio de la SPEC)│
     │        │                                          │
     │   ¿Pasa? ─Sí──────────────────┐                   │
     │        │No                    │                   │
     │        ▼                      │                   │
     │   c. Diagnosticar + corregir   │                   │
     │      (reintento N de MAX_N)    │                   │
     │        │                       │                   │
     │   ¿N > MAX_N? ─Sí─► [PAUSA: circuit breaker —       │
     │        │No           caso extraordinario, RF-11.5]  │
     │        └──────► volver a (b)   │                   │
     │                                ▼                   │
     │                    d. Commit atómico de la tarea    │
     │                       (en el worktree aislado)       │
     │                                │                    │
     │              ¿HITL para esta tarea/archivo/patrón?    │
     │                Sí │                    No             │
     │                   ▼                     │             │
     │        [PAUSA: revisión humana]         │             │
     │                   │                     │             │
     │                   └──────────┬──────────┘             │
     │                              ▼                        │
     │                   ¿Quedan tareas? ─Sí─► volver a (a)    │
     └──────────────────────────────┼─────────────────────────┘
                                    No
                                    ▼
        ┌───────────────────────┐
        │ 4. Validación global    │  → toda la SPEC cumplida,
        │    (todas las tareas)   │     suite completa de tests
        └───────────┬─────────────┘
                    ▼
         ¿HITL "pre-merge"? ─Sí──► [PAUSA: aprobar merge a base_branch]
                    │No
                    ▼
        ┌───────────────────────┐
        │ 5. Merge a base_branch  │  (solo si fue aprobado o el nivel
        │    (si aplica)          │   de autonomía lo permite, §7.2)
        └───────────┬─────────────┘
                    ▼
        ┌───────────────────────┐
        │ 6. Reporte final        │  → tareas completadas, HITLs
        │    (RF-11.10)           │     activados, desviaciones,
        │                          │     estado de validación
        └───────────────────────┘

  Disparadores de pausa que NO dependen del manifest — piso de
  seguridad no configurable (RNF-8.2), siempre activo:
    · operación git destructiva (force-push, reset --hard, borrado de branch)
    · acceso a credenciales/secrets fuera de lo declarado
    · llamada de red fuera de la allowlist
    · presupuesto (tiempo/tokens/costo) excedido
```

### 3.7 Detección de ambigüedad genuina en la SPEC (Tier 1/2/3)

**El problema con dejarlo en "el agente pausa si algo es ambiguo":** no es un criterio operable — un LLM puede reportar "ambigüedad" ante cualquier dificultad de implementación, o no reportarla nunca porque siempre encuentra *alguna* interpretación razonable. Hace falta una prueba explícita, no un juicio abierto.

**Prueba de multiplicidad (correlato del test real):** antes de implementar una tarea, el agente debe poder articular explícitamente si existen **dos o más interpretaciones válidas** que satisfacen la letra del criterio "hecho" de la SPEC, pero que producen comportamiento observable distinto (no solo detalle interno de implementación). Si no puede articular una segunda interpretación divergente, no hay ambigüedad — hay una tarea normal.

```
                    ¿Existen ≥2 interpretaciones válidas
                     con comportamiento OBSERVABLE distinto?
                              │
                 No ──────────┼────────── Sí
                  │                        │
                  ▼                        ▼
         TIER 1 — No es ambiguo    ¿La divergencia ya está resuelta
         (detalle interno de       por una convención anclada del
         implementación: libre-    proyecto (Nivel 0, §3.3) o por
         ría, nombres internos,    un default documentado del
         organización de          harness?
         archivos, etc.)                   │
                  │              Sí ───────┼─────── No
                  ▼               │                  │
         Proceder sin              ▼                  ▼
         pausar, sin       TIER 2 — Ambigüedad     ¿La divergencia cae en:
         registrar          resuelta por          producto/negocio, seguridad/
         nada especial.     convención/default     privacidad, cumplimiento
                             existente               legal, costo significativo,
                                  │                  o irreversibilidad de datos?
                                  ▼                          │
                          Proceder, PERO             Sí ─────┼───── No
                          registrar el                │              │
                          supuesto asumido             ▼              ▼
                          en el reporte final    TIER 3 — PAUSA   Aplicar heurística
                          (RF-11.10)             HITL obligatoria  conservadora por
                                                  Presentar:        defecto (opción
                                                  · pasaje exacto    más reversible /
                                                    de la SPEC que   más restrictiva),
                                                    dispara el caso  tratar como TIER 2
                                                  · interpretaciones (registrar supuesto)
                                                    candidatas
                                                    enumeradas
                                                  · categoría que lo
                                                    clasifica como
                                                    Tier 3 y por qué
```

**Heurísticas de detección (primera pasada, durante RF-11.3 — descomposición):**
- *Palabras-bandera léxicas*: escanear la SPEC en busca de términos que históricamente correlacionan con sub-especificación — "según corresponda", "de forma adecuada", "razonable", "rápido"/"seguro" sin umbral numérico, "similar a", "etc.", "entre otros", "TBD", "TODO", "a definir". No dispara Tier 3 por sí solo — marca la sección para escrutinio explícito de multiplicidad en la descomposición.
- *Referencias no resueltas*: toda entidad que la SPEC asume como existente (archivo, endpoint, config key, servicio externo, tabla) debe resolverse contra el repo/documentación real. Lo que no resuelve, se marca como candidato a Tier 3 (no se asume su existencia ni su forma).
- *Chequeo de conflictos entre tareas*: al construir el grafo de tareas, comparar restricciones declaradas en distintas secciones/tareas sobre el mismo sujeto (ej. "el sistema debe X" en una sección y "el sistema no debe X" o algo incompatible en otra) — contradicción directa es Tier 3 automático, no pasa por la prueba de multiplicidad.

**Sesgo deliberado del diseño:** ante la duda sobre si algo es Tier 2 o Tier 3, el sistema debe **sesgar hacia Tier 3 (pausar)**. Un falso positivo cuesta una interrupción HITL; un falso negativo significa que el agente tomó una decisión de producto/negocio/seguridad por su cuenta sin que nadie se enterara hasta el reporte final — asimetría de costo que justifica el sesgo conservador.

---

## 4. Stack tecnológico

Tabla actualizada 2026-10-03 con las decisiones implementadas (la propuesta original queda en el historial de git, RF-8.4).

| Componente | Elección | Razón |
|---|---|---|
| Lenguaje del core | **Go** | Cold-start rápido, bajo footprint de memoria, concurrencia nativa simple |
| API interna | JSON-RPC 2.0 sobre WebSocket (`coder/websocket`) | Contrato único para todos los clientes; streaming de eventos nativo |
| CLI/TUI | `cobra` + `bubbletea` v2 | Estándar de facto en el ecosistema Go |
| Herramientas | Nativas (fs, shell, git, …) con interfaz de forma MCP + **MCP real** (`modelcontextprotocol/go-sdk`) como cliente y servidor (RF-5.5) | Ecosistema MCP sin acoplar el core |
| Sandbox de plugins | **WASM vía wazero** (Go puro, sin cgo) | Aislamiento real sin dependencias nativas, compila en todas las plataformas |
| Memoria estructurada | **SQLite** (`modernc.org/sqlite`, Go puro) | Embebido, transaccional, sin cgo |
| Memoria semántica | Embeddings guardados en SQLite: backend llama.cpp (`bge-m3`) cuando está disponible, embedding hash como respaldo inmediato | Local-first, sin servicio externo; el arranque no espera al backend |
| Aislamiento de shell | Landlock + seccomp (`elastic/go-seccomp-bpf`) en Linux; solo modelo de permisos en Windows/macOS | RNF-4.7 con su matiz por plataforma |
| Adaptadores de proveedor | Interfaz común + OpenAI-compatible (Ollama/llama.cpp/vLLM/LM Studio), Anthropic Messages, Gemini | Sin acoplar el core a un proveedor |
| GUI web | Estática (HTML/CSS/JS) embebida en el binario y servida por el daemon | Un cliente más de la misma API, sin toolchain de frontend |
| Observabilidad | `log/slog` (JSON) + grabación de sesión en SQLite | Biblioteca estándar; debug y minería de skills |
| Cuantización / backend de inferencia | GGUF **Q4_K_M** como default vía Ollama/llama.cpp, backend CPU en Perfil A | Balance memoria/velocidad/calidad para 7-8B sin GPU |

---

## 5. Riesgos y supuestos abiertos

- **Riesgo de alcance**: la lista de RF es amplia; sin fases, el proyecto corre riesgo de no converger nunca a un MVP usable. Ver sección 6.
- **Supuesto (resuelto)**: "SDD" es Spec-Driven Development (RF-8).
- **Riesgo técnico**: MCP como estándar de tools está en evolución activa; el adaptador debe diseñarse con capa de compatibilidad para no romper ante cambios de protocolo.
- **Riesgo de mantenimiento**: proyecto de un solo desarrollador — mismo "bus factor" señalado para OpenChamber. Documentación interna exhaustiva es mitigación mínima, no solución completa.
- **Perfiles de hardware de referencia (resuelto):**
  - **Perfil A — interactivo/estándar:** definido por sus características, no por una máquina: **8 núcleos físicos, 32 GB de RAM, inferencia 100% CPU, sin GPU utilizable**. Máquina de referencia original: MacBook Pro, Intel Core i9 de 8 núcleos @2.3GHz, 32GB DDR4-2667MHz, sin GPU discreta utilizable para inferencia (Intel UHD 630 integrada — sin ruta de aceleración real para LLMs). Inferencia 100% CPU. Dirige los objetivos de RNF-1/RNF-2 para el flujo interactivo — es el hardware "estándar" al que se refiere el RF principal del proyecto.
  - **Registro de la máquina de medición (obligatorio en cada resultado del banco, RNF-10):** cada medición anota CPU, núcleos/hilos, RAM y SO reales. Las mediciones "Perfil A" registradas hasta 2026-10-03 se tomaron en un AMD Ryzen 7 PRO 3700U (4 núcleos / 8 hilos, ~14 GB, Windows) — **por debajo** del Perfil A — y deben leerse como cota pesimista, no como el Perfil A.
  - **Perfil B — batch/autónomo:** VM `llm` institucional (~62GiB RAM), también CPU-bound pero con más margen de RAM/núcleos. Reservado para corridas en segundo plano (RF-11) donde `budget.max_wall_clock` ya asume tolerancia de horas — permite modelos más grandes y contextos más largos que el Perfil A.
  - **Implicación concreta para modelos:** en el Perfil A, el objetivo son modelos de 1-3B parámetros (cuantizados, ej. GGUF Q4_K_M) para los pasos baratos del ruteo (RF-2.4/2.5 — clasificación, queries de retrieval, resúmenes) y de 7-8B para generación real. Modelos de 13B+ son usables pero notablemente más lentos en CPU; 30B+ se considera poco práctico para trabajo interactivo en este perfil y se reserva para el Perfil B. Estas cifras son una hipótesis de diseño razonable, no una medición — deben confirmarse con el banco de pruebas de RNF-10 antes de fijarse como objetivo definitivo.
- **Riesgo de autonomía plena (RF-11)**: es, con diferencia, el componente de mayor riesgo del sistema. Un ciclo de auto-corrección mal acotado puede degenerar en bucles infinitos, gasto descontrolado de tokens/costo, o "arreglos" que satisfacen la métrica (tests pasan) sin satisfacer la intención real de la SPEC. Las mitigaciones de diseño (circuit breaker en RF-11.5, piso de seguridad no configurable en RNF-8.2, aislamiento obligatorio en worktree en RNF-8.1) reducen el riesgo pero no lo eliminan — no reemplazan revisión humana real en los primeros usos de este modo, especialmente sobre proyectos/repos que importan.
- **Alcance de la seguridad por diseño (RNF-4, RNF-9)**: lo cubierto en este documento es una primera pasada de diseño — postura deny-por-defecto, tratamiento de contenido no confiable, aislamiento por capas, allowlist de red, log a prueba de manipulación. No reemplaza un modelado de amenazas formal ni una revisión de seguridad externa. Antes de habilitar v1.0 (ejecución autónoma, §6) sobre un proyecto `regulado` o `datos-sensibles` real, corresponde una revisión dedicada — no basta con que el diseño en papel luzca razonable.
- **Riesgo de velocidad del desarrollo con modelos locales (§5)**: construir contra herramientas débiles o modelos chicos cuesta tiempo puro frente a OpenCode/Claude Code. *Mitigación vigente:* el desarrollo del core y su arquitectura se hace con herramientas externas y modelos de frontera vía el adaptador OpenAI-compatible (RF-2.1/RF-2.3), sin ruido de bugs ni límites de rendimiento de modelos locales. Las métricas de eficiencia de contexto (RNF-2.x) se validan con el banco determinístico model-free de RNF-10, que mide el payload del request y no depende del modelo — neutralizando el sesgo de diseño hacia perfiles de contexto de frontera. La validación de rendimiento con el modelo local objetivo (tokens/seg, TTFT, prefill, Perfil A/B) queda diferida hasta que la arquitectura esté afinada; el bootstrapping de herramienta es un requerimiento deseable diferido (§0).

---

## 6. Hoja de ruta de implementación (MVP v0 → producto completo)

**Advertencia honesta antes de la tabla:** esto es mucho trabajo para una sola persona. La única forma de que converja es tratar cada MVP como una versión realmente usable (aunque incompleta) — no una lista de checkboxes que solo tiene sentido al final. Cada corte de abajo debería poder usarse de verdad para trabajo real antes de pasar al siguiente. El desarrollo de cada versión se hace con herramientas externas; el bootstrapping queda como requerimiento deseable diferido (§0).

### MVP v0 — Núcleo interactivo mínimo
**Tema:** un agente conversacional real, con herramientas reales, sobre un workspace real. Nada elegante todavía.

- **RF cubiertos:** RF-1.1 (agente + tools sobre workspace, sin subagentes aún), RF-2.1 (un solo adaptador: OpenAI-compatible vía Ollama), RF-2.3 (cambiar modelo sin reiniciar), RF-3.1 (memoria persistente simple, SQLite sin vectorial), RF-6.1-6.3 (CLI completa), RF-10.1-10.2 (git básico + shell), RF-1.4 parcial (sesión sobrevive a un cierre de cliente, sin el resto de background jobs complejos).
- **RNF cubiertos — y esto es lo que quiero remarcarte:** RNF-1.1/1.2/1.3 (métricas base), RNF-3.1 (independencia de proveedor — decisión de arquitectura que no se puede corregir después sin reescritura), RNF-4.1 (deny-por-defecto), RNF-4.3/4.4 (local-first, secrets fuera de logs), **RNF-4.5 (tratar contenido no confiable como dato, no como instrucción) y RNF-4.7 (aislamiento de shell del propio core vía contenedor/seccomp)** — no son "seguridad para después": si el core ejecuta shell sin esto desde el día uno, cada mes que pasa hace más caro meterlo retroactivamente. **RNF-4.8** (parada de emergencia) y **RNF-4.9** (allowlist de red por defecto) también entran aquí — son baratos de construir ahora y muy caros de agregar después de que el hábito de "todo pasa" ya esté instalado. RNF-6.1 (logging estructurado), RNF-7.1/7.2 (cambios de dirección a mitad de tarea, config versionable).
- **Diferido explícitamente:** subagentes, retrieval, plugins, skills, GUI, SDD, branching de sesiones, ejecución autónoma, RNF-8/RNF-9 (no aplican sin modo autónomo), adaptadores remotos.
- **Criterio de salida:** podés sostener una conversación con herramientas reales (leer/escribir archivos, correr comandos, commitear) contra un modelo local, sin que se degrade en sesiones largas, y sin que el core pueda hacer nada fuera de lo que vos declaraste permitido. Es la única versión que se construye sin sí misma: herramientas externas (OpenCode/Claude Code u otras) hacen de andamio — históricamente la semilla del bootstrapping (§0), hoy requerimiento deseable diferido.

**Aclaraciones de implementación — v0 (decididas durante exploración agéntica del proyecto):**
1. **MCP:** las tools nativas (file read/write, shell, git) se implementan como funciones Go en proceso, no como servidores MCP separados — pero con la misma forma de interfaz (nombre, descripción, JSON schema, dispatch) que un tool de MCP, para que v2 solo agregue transporte/cliente MCP sobre la abstracción existente, sin reescribir el contrato.
2. **Aislamiento de shell (RNF-4.7), con matiz por plataforma:** `sandbox-exec` (macOS) es una API no documentada/en desuso — no es base aceptable para el requisito. En **Linux** (Perfil B), aislamiento completo vía seccomp/Landlock desde v0 (primitivas estables). En **macOS** (Perfil A), v0 se limita al modelo de permisos deny-by-default (RNF-4.1) sin sandbox de SO; el aislamiento duro en macOS queda para v1 con un mecanismo a evaluar entonces — defendible porque el uso interactivo en Perfil A siempre tiene un humano mirando.
3. **Modelo de referencia para tool-calling:** `qwen2.5-coder:7b` (Q4_K_M vía Ollama) como target principal para optimizar prompt layout y manejo de tool-calling; `llama3.1:8b-instruct` como referencia secundaria para el banco de pruebas de v1 (RNF-10).
4. **CLI vs. TUI:** v0 es solo CLI con REPL interactivo básico (`cobra`) y modo no interactivo. `bubbletea`/TUI con paneles se justifica recién con retrieval (v1) o subagentes (v3) — antes no hay estado rico que visualizar.
5. **Persistencia de sesión (RF-1.4 parcial):** el daemon sigue corriendo en background y el cliente se reconecta a la sesión en curso (ya implícito en la arquitectura de §3.1). SQLite (RF-3.1) resuelve un problema complementario distinto — sobrevivir a que el propio daemon se reinicie — no el de "seguir mientras el cliente está desconectado".

### MVP v1 — Rápido de verdad (el diferencial del proyecto)
**Tema:** eficiencia de contexto y tokens con modelos locales — la razón de ser del proyecto.

- **RF cubiertos:** RF-3.2 (retrieval selectivo), RF-3.3 (compactación jerárquica), RF-3.4 (memoria inspeccionable/editable), RF-3.5 (anclaje), RF-2.4/2.5 (ruteo por costo de paso).
- **RNF cubiertos:** RNF-2.1-2.5 completos (medición de tokens, prompt-caching, objetivo ≥40%, KV-cache local, techo de contexto), RNF-1.4 (validado, no solo asumido), RNF-1.5/1.6 (concurrencia realista, reserva de núcleos), **RNF-10 completo (banco de pruebas)** — deliberadamente aquí y no al final: sin medir desde este punto, "eficiencia de contexto" es una afirmación de fe, no un requisito cumplido. RNF-6.3 (métricas de costo, junto con RNF-2.1).
- **Nota de dependencia:** RNF-4.12 (aprobación humana para anclar contenido no confiable) empieza a aplicar aquí en su mitad de memoria (RF-3.5) — la otra mitad (skills, RF-4.3) llega en v2.
- **Criterio de salida:** el banco de pruebas de RNF-10 confirma el ≥40% de reducción de tokens sobre el Perfil A real (§5) — no es un objetivo de diseño, es un número medido.

### MVP v2 — Se extiende sin tocar el core
**Tema:** extensibilidad real vía plugins y skills, y el primer adaptador remoto.

- **RF cubiertos:** RF-4.1-4.4 (skills con lazy-load y aprobación humana), RF-5.1-5.4 (plugins WASM), **RF-2.2 (adaptadores Anthropic/Gemini)** — entra aquí, no en v0: el primer adaptador remoto es, en los hechos, el primer "plugin real" que ejercita el sistema de extensibilidad recién construido, en vez de ser un caso especial cableado al core.
- **RNF cubiertos:** RNF-3.2 (extender vía plugin sin tocar el core — ahora se prueba de verdad), RNF-3.3 (tests de integración sobre la API interna), RNF-4.2 (mínimo privilegio en plugins), RNF-4.6 (procedencia/firma de plugins y skills externos), RNF-4.12 completo (incluye ahora la mitad de skills), RNF-6.2 (grabación/replay de sesiones — necesario para que la minería de skills tenga de dónde sacar trayectorias).
- **Criterio de salida:** instalás un plugin de terceros y una skill sin recompilar el binario, y ambos corren aislados con permisos mínimos declarados. **Además: el wizard CLI (orge plugin new, orge skill new) permite crear plugins y skills válidos desde cero sin editar archivos a mano.**

### MVP v3 — Multiagente (subagentes y ramas)
**Tema:** orquestación multiagente con contexto acotado, y ramas de ejecución por sesión.

- **RF cubiertos:** RF-1.2/1.3 (subagentes con contexto acotado), RF-9.1-9.3 (branching, merge, multi-run).
- **RNF cubiertos:** re-validación de RNF-1.5 con subagentes reales (no solo teórica como en v1) — la cola/scheduler ahora tiene contención de verdad que probar.
- **Diferido:** RF-10.3 (integración GitHub) es explícitamente "deseable, no v1" — entra acá o después, sin bloquear nada.
- **Criterio de salida:** el orquestador delega una tarea real a un subagente con contexto acotado, y el resultado consolidado vuelve sin inflar el contexto del orquestador (medible con el banco de RNF-10).

### MVP v4 — Superficies adicionales
**Tema:** GUI web, SDD formal, auto-aprendizaje activo, portabilidad validada.

- **RF cubiertos:** RF-7.1-7.4 (GUI web), RF-8.1-8.4 (SDD formal — descomposición de spec en tareas trackeables), auto-aprendizaje activo (minería de patrones sobre las trayectorias grabadas desde v2).
- **RNF cubiertos:** RNF-5.1/5.2 (portabilidad — validación real en Linux/Windows, no solo "el lenguaje lo permite"), RNF-4.11 (transporte cifrado para acceso remoto a la GUI).
- **Nota de reutilización:** el motor de descomposición de RF-8.2 (spec → tareas) es el mismo que necesita RF-11.3 — construirlo bien acá evita reconstruirlo en la fase siguiente.
- **Criterio de salida:** la GUI muestra el mismo estado que la CLI en tiempo real, y una SPEC real se descompone y trackea automáticamente sin intervención manual.

### v1.0 — Producto completo: ejecución autónoma end-to-end
**Tema:** RF-11 completo — el modo de mayor riesgo, por eso va último.

- **RF cubiertos:** RF-11.1-11.10 completos, casos extraordinarios (ambigüedad Tier 3, §3.7).
- **RNF cubiertos:** RNF-8.1-8.4 completos (autonomía segura), RNF-9.1-9.4 completos (techo de sensibilidad de proyecto), RNF-4.8/4.9 ya existían desde v0 pero se validan bajo carga real, RNF-4.10 (log a prueba de manipulación — recién tiene sentido con RNF-9 ya activo).
- **Rollout dentro de esta versión, no como MVP aparte:** la progresión `dry_run → supervised → checkpoint → autonomous` (§7.2) se recorre sobre proyectos reales de sensibilidad `general` antes de considerar el modo maduro.
- **Criterio de salida:** un run manifest completo corre en modo `checkpoint` sobre un proyecto real de baja sensibilidad, sin intervención fuera de los checkpoints declarados, con reporte final coherente (RF-11.10) y sin que el piso de seguridad no configurable (RNF-8.2) haya tenido que intervenir. Como requerimiento deseable diferido (§0): una vez validado el flujo, parte de las tareas restantes puede ejecutarlas el propio harness en modo `checkpoint` — primer paso natural del bootstrapping cuando se decida retomarlo.

---

### Tabla de trazabilidad (todos los RF/RNF por versión)

| Versión | RF | RNF |
|---|---|---|
| v0 | 1.1, 1.4(parcial), 2.1, 2.3, 3.1, 6.1-6.3, 10.1-10.2 | 1.1-1.3, 3.1, 4.1, 4.3-4.5, 4.7-4.9, 6.1, 7.1-7.2 |
| v1 | 2.4-2.5, 3.2-3.5 | 1.4-1.6, 2.1-2.5, 6.3, 10.1-10.3 |
| v2 | 2.2, 4.1-4.4, 5.1-5.4 | 3.2-3.3, 4.2, 4.6, 4.12, 6.2 |
| v3 | 1.2-1.3, 9.1-9.3 | (re-validación de 1.5) |
| v4 | 7.1-7.4, 8.1-8.4, 10.3 (opcional) | 4.11, 5.1-5.2 |
| v1.0 | 11.1-11.10 | 8.1-8.4, 9.1-9.4, 4.10 |

---

## 7. Formato del Run Manifest (propuesta — RF-11)

### 7.1 Estructura del archivo

Un archivo JSON (`run.json`; `forge init` genera uno mínimo y válido). La SPEC puede ir embebida (`spec`) o referenciada (`spec_ref`). Las tareas pueden declararse a mano (`tasks`) o derivarse de la SPEC con `forge run --manifest run.json --decompose`.

```json
{
  "run_id": "implementar-modulo-facturacion",
  "mode": "checkpoint",
  "goal": "<instrucción de alto nivel, en una frase o párrafo>",
  "spec_ref": "./SPEC.md",
  "budget": {
    "max_wall_clock": "6h",
    "max_tokens": 3000000,
    "max_iterations": 200,
    "max_retries_per_task": 4
  },
  "git": {
    "isolation": "branch",
    "base_branch": "main",
    "work_branch": "run/implementar-modulo-facturacion",
    "commit_per_task": true,
    "merge_to_base": "manual"
  },
  "hitl": {
    "checkpoints": [
      {"id": "post-decomposition", "trigger": "after_spec_decomposition", "required": true},
      {"id": "pre-merge", "trigger": "before_merge", "required": true},
      {"id": "rutas-sensibles", "trigger": "before_editing", "required": true,
       "match": ["**/migrations/**", "**/.env*", "**/infra/**", "**/*secret*"]},
      {"id": "presupuesto-50", "trigger": "budget_threshold", "threshold": 0.5, "required": false}
    ]
  },
  "tasks": [
    {"id": "t1", "goal": "Modelo de factura con validaciones",
     "done_criteria": "cmd: go test ./internal/billing/...",
     "file_budget": "internal/billing/**", "model_hint": "generation"}
  ]
}
```

- **`mode`:** `supervised | checkpoint | autonomous | dry_run` (§7.2).
- **Disparadores HITL:** `after_spec_decomposition`, `before_task`, `after_task`, `before_merge`, `budget_threshold`, `before_editing`.
- **`done_criteria`:** texto libre es descriptivo (lo evalúa un verificador); con prefijo `cmd:` es un chequeo mecánico — la tarea solo se da por hecha si el comando sale con código 0 (RNF-8.3).
- **`git.isolation`:** en v1 el aislamiento es **`branch`** — rama propia, un commit por tarea, merge solo tras el checkpoint `before_merge` aprobado, y el workspace bloqueado para otras sesiones mientras la corrida lo usa (RNF-8.5). `worktree` queda **diferido**: exige que fs, shell, git y permisos operen sobre un directorio por corrida, y el motor lo rechaza en vez de "aislar" sobre el workspace compartido. `none` solo se admite en `supervised`/`dry_run`.
- **Presupuesto de costo:** `max_cost_usd` se acepta pero **no se aplica** todavía (no hay medición de costo por llamada); el piso de RNF-8.2 cubre hoy tiempo, tokens e iteraciones.
- **Piso de seguridad no configurable (RNF-8.2):** se aplica aparezca o no en el manifest. El manifest puede *añadir* checkpoints; nunca *quitar* los del piso.

### 7.2 Niveles de autonomía

| Nivel | Comportamiento | Cuándo usarlo |
|---|---|---|
| `supervised` | Pausa después de **cada** tarea completada, sin excepción | Primeras corridas, tareas de alto riesgo, o mientras no confías aún en el harness |
| `checkpoint` | Pausa solo en los HITL declarados explícitamente + piso de seguridad | Uso normal, una vez validado el flujo en `supervised` |
| `autonomous` | Igual que `checkpoint`, pero permite `merge_to_base: auto_if_all_hitl_passed` | Tareas acotadas y de bajo riesgo, con buena cobertura de tests existente |
| `dry_run` | Ejecuta todo el ciclo de decisión (incluida la descomposición y los diffs propuestos) pero no escribe nada — solo reporta qué haría | Validar el plan antes de comprometerse a ejecutar |

**Recomendación de uso:** ningún proyecto debería arrancar en `autonomous`. La progresión natural es `dry_run` → `supervised` → `checkpoint` → `autonomous`, ganando confianza en el comportamiento del harness sobre ese repo/tipo de tarea específico antes de soltarle más autonomía.

---

## Registro de revisiones

- **0.11** — Ajustes tras la revisión de 2026-10-03: recodificación UTF-8 del documento (496 líneas con texto mal codificado, que impedía la detección léxica de §3.7); RNF-4.5 extendido al texto derivado; perfiles de hardware por características y registro de la máquina de medición; formato real del Run Manifest (`run.json`, aislamiento `branch`, `worktree` diferido, costo no aplicado); stack actualizado; definiciones medibles para RNF-1.1, RNF-1.2, RNF-1.4 y RNF-2.3; fidelidad de resúmenes en RF-3.3; semántica de RF-9.2; requisitos nuevos para funcionalidad existente (RF-2.7, RF-3.6, RF-10.4, RNF-4.13, RNF-8.5); orden de secciones; conteo de cobertura verificado por test.
- **0.10** — Semántica de streaming formalizada (implementación WU3, 2026-09-02): se añade RF-2.6 — streaming SSE opcional para adaptadores (Anthropic/Gemini) gobernado por `llm.streaming` (default OFF). Semántica de falla: una falla a mitad de stream falla el turno, sin fallback silencioso; el sentinel `ErrStreamingNotSupported` degrada automático a `Chat`. Deltas publicados vía `message.delta.event` (best-effort, broadcast no bloqueante). Flag leído al inicio del daemon, sin hot-reload.
- **0.9** — Gobernanza (decisión del owner, 2026-08-31): el bootstrapping (§0) deja de ser principio rector y criterio de salida; pasa a requerimiento deseable diferido — a abordar cuando el core esté más depurado, posiblemente al final (v1.0 o posterior). El principio rector pasa a ser la eficiencia de contexto y tokens (RNF-2.x). La validación de rendimiento con modelos locales queda diferida hasta tener la arquitectura afinada; el banco de RNF-10 opera model-free (determinístico). Reparación estructural: se restaura el encabezado de MVP v3 (había quedado fusionado con la sección v2).
- **0.8** — Se hace explícito el principio rector de bootstrapping (§0) y se incorpora como criterio de salida verificable en cada versión de la hoja de ruta (§6): desde v1, cada MVP se desarrolla usando el anterior. Incluye el riesgo de velocidad asociado en §5, con válvula de escape por capacidad de modelo (frontera temporal vía RF-2.1/2.3) sin suspender el bootstrapping de herramienta.
- **0.7** — Borrador inicial para validación de arquitectura.

---

*Fin del documento. Este spec es un punto de partida para iteración — no una decisión de arquitectura cerrada.*
