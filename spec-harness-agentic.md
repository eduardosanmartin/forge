# SPEC — Harness de Desarrollo Agentic a Medida

**Nombre de proyecto (working title):** `forge` *(placeholder — renombrar libremente)*
**Versión del documento:** 0.8
**Estado:** Borrador para validación de arquitectura
**Alcance:** Definición funcional, no funcional y arquitectónica de un harness de desarrollo con agentes IA, inspirado en OpenCode y Claude Code, optimizado para modelos locales, eficiencia de contexto/tokens, y extensibilidad total.

---

## 0. Visión y motivación

Herramientas como OpenCode y Claude Code resuelven bien el caso general, pero:
- No están optimizadas para el patrón de uso específico del operador (modelos locales, sesiones largas, cambios de dirección espontáneos).
- Su modelo de contexto es en gran medida "todo el historial, cada turno" — ineficiente en tokens.
- No son forkeables/adaptables a nivel de arquitectura interna sin asumir toda la complejidad del proyecto original.

Este proyecto no busca ser "mejor en todos los ejes" que herramientas con equipo detrás — busca ser **superior en el caso de uso propio**: eficiencia de contexto, control total del stack, y una arquitectura que crezca por plugins en vez de por reescritura.

**Principio rector — bootstrapping:** forge se construye a sí mismo. La única versión desarrollada con herramientas externas es v0; desde ahí, cada MVP se implementa usando el MVP anterior como herramienta principal de trabajo. No es solo filosofía de proceso: es criterio de salida verificable en cada versión (§6) y la prueba de honestidad del producto — un harness agéntico incapaz de sostener su propio desarrollo no cumple su razón de ser.

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

### RF-3. Gestión de contexto y memoria
- RF-3.1 Debe mantener memoria persistente entre sesiones (decisiones de arquitectura, convenciones del proyecto, hechos "anclados").
- RF-3.2 Debe implementar recuperación selectiva de contexto (retrieval) en vez de enviar el historial completo en cada turno.
- RF-3.3 Debe compactar/resumir sesiones largas de forma jerárquica y progresiva, preservando hechos ancla sin comprimir.
- RF-3.4 El usuario debe poder inspeccionar y editar manualmente qué hay en la memoria persistente (transparencia total, sin caja negra).
- RF-3.5 Debe existir un mecanismo de "anclaje" explícito: el usuario o el agente pueden marcar un hecho/decisión como permanente.

### RF-4. Skills y auto-aprendizaje
- RF-4.1 Debe soportar la creación, carga e instalación de "skills" (paquetes de instrucciones + scripts reutilizables), similar al patrón `SKILL.md`.
- RF-4.2 Las skills deben cargarse de forma perezosa (lazy-load): solo se inyectan en el contexto cuando son relevantes a la tarea detectada.
- RF-4.3 El sistema debe poder proponer la creación de una nueva skill a partir de una trayectoria de tarea exitosa repetida (minería de patrones).
- RF-4.4 Debe existir un flujo de aprobación humana antes de que una skill auto-generada quede activa (nunca auto-aprendizaje sin supervisión).

### RF-5. Plugins y extensibilidad
- RF-5.1 Debe soportar plugins de terceros que añadan: nuevas herramientas, nuevos proveedores de LLM, nuevos comandos de CLI, o paneles de GUI.
- RF-5.2 Los plugins deben ejecutarse en un entorno aislado (sandbox) del proceso principal.
- RF-5.3 Debe existir un manifiesto de plugin (metadatos, permisos solicitados, versión, dependencias).
- RF-5.4 El sistema debe permitir habilitar/deshabilitar plugins sin recompilar el binario principal.

### RF-6. CLI
- RF-6.1 CLI minimalista con comandos core: iniciar sesión, listar sesiones, adjuntar a sesión en curso, ejecutar tarea puntual (one-shot), gestionar plugins/skills, gestionar proveedores.
- RF-6.2 Debe soportar modo interactivo (TUI) y modo no interactivo (scriptable, para CI/CD o cron).
- RF-6.3 Salida en modo no interactivo debe soportar formato JSON para integración con otras herramientas.

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
- RF-9.2 Debe permitir fusionar (merge) el resultado de ramas alternativas o de ejecuciones multi-modelo.
- RF-9.3 Debe permitir ejecutar la misma tarea en paralelo contra varios modelos/proveedores y comparar resultados.

### RF-10. Integración con control de versiones y entorno
- RF-10.1 Debe integrarse con git: lectura de diffs, creación de commits, gestión de branches por tarea/worktree.
- RF-10.2 Debe soportar ejecución de comandos de shell dentro del workspace, con visibilidad completa de su salida para el agente.
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
- RNF-1.1 Tiempo de arranque en frío (cold start) del daemon/core: objetivo < 200ms.
- RNF-1.2 Latencia añadida por el harness (overhead sobre el tiempo de inferencia del modelo) debe ser despreciable (< 50ms por turno en condiciones normales).
- RNF-1.3 Uso de memoria del proceso core en reposo: objetivo < 100MB.
- RNF-1.4 Debe soportar sesiones de larga duración (horas/días) sin degradación de rendimiento ni fugas de memoria.
- RNF-1.5 **Concurrencia realista sobre el Perfil A (§5, §7.1):** en el hardware de referencia interactivo (CPU multinúcleo sin GPU discreta utilizable para inferencia), no existe ninguna ruta de paralelismo físico real — un único proceso de modelo consume los núcleos disponibles de forma serializada. Todo lo que RF-1.2/RF-1.3 llaman "concurrencia de agentes" se traduce, en este perfil, en una cola con prioridad hacia ese único proceso de inferencia — nunca en ejecución simultánea. El planificador de solicitudes con prioridad es, en este hardware, el único mecanismo real de "multiagente", y debe diseñarse asumiendo cero paralelismo físico como caso normal, no como degradación de un caso ideal.
- RNF-1.6 **Reserva de núcleos:** el proceso de inferencia local no debe reservar por defecto el 100% de los núcleos físicos disponibles — debe dejar margen (ej. 1-2 núcleos) para que el equipo siga siendo usable para otras tareas mientras el harness trabaja, salvo que el usuario indique explícitamente lo contrario (ej. una corrida autónoma nocturna sin uso concurrente del equipo).

### RNF-2. Eficiencia de contexto/tokens (requisito diferenciador del proyecto)
- RNF-2.1 El sistema debe medir y reportar tokens consumidos por turno, por sesión y por proveedor.
- RNF-2.2 El diseño de contexto debe maximizar hits de prompt-caching de cada proveedor (orden estable: sistema → herramientas → memoria → historial variable).
- RNF-2.3 Objetivo cuantitativo: reducción de ≥40% en tokens de contexto respecto a un enfoque naive de "historial completo" en sesiones de más de 20 turnos.
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
- RNF-4.5 Contenido de fuentes no confiables debe tratarse siempre como **datos, nunca como instrucciones** — cualquier acción que ese contenido "solicite" debe pasar por el mismo modelo de permisos que cualquier otra acción del agente; el sistema no ejecuta instrucciones encontradas dentro de archivos, páginas web, o salidas de herramientas.
- RNF-4.6 Plugins y skills de origen externo (no creados por el propio usuario) requieren verificación de procedencia (checksum/firma) antes de cargarse, y aprobación humana explícita antes de su primera ejecución — el registro de plugins (§3.5) debe distinguir "creado localmente" de "instalado de fuente externa".
- RNF-4.7 La ejecución de shell del propio core (no solo de plugins) debe correr con aislamiento adicional a nivel de sistema operativo — el modelo de permisos declara *qué* está autorizado; el aislamiento de SO es la segunda capa que contiene el daño si el modelo de permisos falla o es evadido. **Matiz por plataforma (§6):** en Linux, exigible desde v0 vía seccomp/Landlock (primitivas estables y documentadas). En macOS, `sandbox-exec` queda descartado por ser una API no documentada/en desuso de Apple — v0 en macOS se limita al modelo de permisos (RNF-4.1) sin aislamiento de SO, con el aislamiento duro diferido a v1 pendiente de un mecanismo estable.
- RNF-4.8 Debe existir una **parada de emergencia** accesible desde cualquier cliente (CLI/GUI) que detenga de inmediato cualquier ejecución en curso — interactiva o autónoma — dejando el estado en el último punto consistente conocido (ligado a RNF-8.4).
- RNF-4.9 Todo adaptador de proveedor (§3.1) debe operar con una allowlist de red explícita como comportamiento **por defecto del sistema en cualquier modo** — no solo durante ejecución autónoma (RF-11).
- RNF-4.10 El log/auditoría de una corrida (RNF-6.2, RF-11.8) debe ser a prueba de manipulación (append-only o con encadenamiento verificable) cuando el proyecto tenga clasificación `regulado` o `datos-sensibles` (RNF-9) — un log editable después de los hechos no sirve como evidencia de cumplimiento.
- RNF-4.11 El acceso remoto a la GUI web (RF-7.4) debe viajar sobre transporte cifrado por defecto (TLS o túnel cifrado) — una contraseña sobre una conexión sin cifrar es una traba mínima, no un control de acceso remoto aceptable.
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

### RNF-9. Clasificación de sensibilidad del proyecto — techo de autonomía
- RNF-9.1 Cada proyecto debe declarar, una única vez en su configuración versionada (no en cada run manifest individual), una clasificación de sensibilidad: `general`, `regulado`, o `datos-sensibles`.
- RNF-9.2 Esta clasificación actúa como un **techo** sobre el nivel de autonomía permitido (§7.2), independiente de lo que solicite un run manifest puntual:
  - `datos-sensibles` (ej. información de salud u otra especialmente protegida) → techo duro en `supervised`, sin excepción configurable.
  - `regulado` (ej. cumplimiento normativo, procesos con validez legal) → techo en `checkpoint`, con el checkpoint `pre-merge` fijo en `required: true`, no removible.
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

## 4. Stack tecnológico propuesto

| Componente | Elección | Razón |
|---|---|---|
| Lenguaje del core | **Go** | Cold-start rápido, bajo footprint de memoria, concurrencia nativa simple, sin curva de aprendizaje excesiva |
| API interna | JSON-RPC 2.0 sobre WebSocket | Contrato único para todos los clientes; streaming de eventos nativo |
| CLI/TUI | `cobra` + `bubbletea` (Go) | Estándar de facto en el ecosistema Go para CLIs modernas |
| Protocolo de herramientas | **MCP (Model Context Protocol)** | Reutiliza ecosistema existente de servidores MCP en vez de inventar uno propio |
| Sandbox de plugins | **WASM** (wasmtime o wasmer) | Aislamiento real, agnóstico de lenguaje de implementación del plugin |
| Memoria estructurada | **SQLite** | Embebido, cero dependencias externas, transaccional |
| Memoria semántica | **sqlite-vec** o **LanceDB embebido** | Vectorial local-first, sin servicio externo |
| Adaptadores de proveedor | Interfaz común + implementaciones: OpenAI-compatible (Ollama/llama.cpp/vLLM/LM Studio), Anthropic Messages API, Gemini API | Cobertura amplia sin acoplar el core a un solo proveedor |
| GUI web | SPA liviana (framework a definir — React/Svelte) consumiendo la misma API interna | Desacoplada del core; un cliente más |
| Observabilidad | Logging estructurado (`zerolog`/`zap`) + grabación de sesión en SQLite | Debug y minería de skills |
| Cuantización / backend de inferencia | GGUF **Q4_K_M** como default vía Ollama/llama.cpp, backend CPU en Perfil A (sin ruta de aceleración GPU utilizable en ese hardware, §5) | Balance memoria/velocidad/calidad razonable para 7-8B en 32GB de RAM sin GPU discreta |

---

## 5. Riesgos y supuestos abiertos

- **Riesgo de alcance**: la lista de RF es amplia; sin fases, el proyecto corre riesgo de no converger nunca a un MVP usable. Ver sección 6.
- **Supuesto**: "SDD" se interpreta como Spec-Driven Development — a confirmar.
- **Riesgo técnico**: MCP como estándar de tools está en evolución activa; el adaptador debe diseñarse con capa de compatibilidad para no romper ante cambios de protocolo.
- **Riesgo de mantenimiento**: proyecto de un solo desarrollador — mismo "bus factor" señalado para OpenChamber. Documentación interna exhaustiva es mitigación mínima, no solución completa.
- **Perfiles de hardware de referencia (resuelto):**
  - **Perfil A — interactivo/estándar:** MacBook Pro, Intel Core i9 de 8 núcleos @2.3GHz, 32GB DDR4-2667MHz, sin GPU discreta utilizable para inferencia (Intel UHD 630 integrada — sin ruta de aceleración real para LLMs). Inferencia 100% CPU. Dirige los objetivos de RNF-1/RNF-2 para el flujo interactivo — es el hardware "estándar" al que se refiere el RF principal del proyecto.
  - **Perfil B — batch/autónomo:** VM `llm` institucional (~62GiB RAM), también CPU-bound pero con más margen de RAM/núcleos. Reservado para corridas en segundo plano (RF-11) donde `budget.max_wall_clock` ya asume tolerancia de horas — permite modelos más grandes y contextos más largos que el Perfil A.
  - **Implicación concreta para modelos:** en el Perfil A, el objetivo son modelos de 1-3B parámetros (cuantizados, ej. GGUF Q4_K_M) para los pasos baratos del ruteo (RF-2.4/2.5 — clasificación, queries de retrieval, resúmenes) y de 7-8B para generación real. Modelos de 13B+ son usables pero notablemente más lentos en CPU; 30B+ se considera poco práctico para trabajo interactivo en este perfil y se reserva para el Perfil B. Estas cifras son una hipótesis de diseño razonable, no una medición — deben confirmarse con el banco de pruebas de RNF-10 antes de fijarse como objetivo definitivo.
- **Riesgo de autonomía plena (RF-11)**: es, con diferencia, el componente de mayor riesgo del sistema. Un ciclo de auto-corrección mal acotado puede degenerar en bucles infinitos, gasto descontrolado de tokens/costo, o "arreglos" que satisfacen la métrica (tests pasan) sin satisfacer la intención real de la SPEC. Las mitigaciones de diseño (circuit breaker en RF-11.5, piso de seguridad no configurable en RNF-8.2, aislamiento obligatorio en worktree en RNF-8.1) reducen el riesgo pero no lo eliminan — no reemplazan revisión humana real en los primeros usos de este modo, especialmente sobre proyectos/repos que importan.
- **Alcance de la seguridad por diseño (RNF-4, RNF-9)**: lo cubierto en este documento es una primera pasada de diseño — postura deny-por-defecto, tratamiento de contenido no confiable, aislamiento por capas, allowlist de red, log a prueba de manipulación. No reemplaza un modelado de amenazas formal ni una revisión de seguridad externa. Antes de habilitar v1.0 (ejecución autónoma, §6) sobre un proyecto `regulado` o `datos-sensibles` real, corresponde una revisión dedicada — no basta con que el diseño en papel luzca razonable.
- **Riesgo de velocidad del bootstrapping (§0)**: construir v1 usando v0 es hacerlo con la herramienta más débil del ciclo — modelo local chico, sin retrieval ni compactación (justo lo que se está construyendo), y con los bugs propios de una v0. El costo es tiempo puro frente a usar OpenCode/Claude Code para el mismo trabajo. *Mitigación:* aceptarlo como inversión de validación — si v0 no alcanza para construir v1, la tesis del producto se falsa temprano y barato — y convertir cada dolor concreto sufrido con v0 en requisito priorizado de v1. **Válvula de escape acordada — cambiar el modelo, no la herramienta:** si la velocidad de depuración llegara a ser inviable con modelos locales, v0 se conecta temporalmente a un modelo de frontera vía su adaptador OpenAI-compatible (RF-2.1/RF-2.3) hasta volver a ser viable el modelo local. El bootstrapping de herramienta queda intacto — el criterio "v1 se desarrolla usando v0" sigue cumpliéndose — bajo dos condiciones: toda excepción se registra con motivo y duración, y las métricas de RNF-10 se validan siempre contra el modelo local objetivo — optimizar compactación/retrieval medido contra un modelo de frontera puede sesgar el diseño hacia un perfil de contexto que no representa al Perfil A real.

---

## 6. Hoja de ruta de implementación (MVP v0 → producto completo)

**Advertencia honesta antes de la tabla:** esto es mucho trabajo para una sola persona. La única forma de que converja es tratar cada MVP como una versión realmente usable (aunque incompleta) — no una lista de checkboxes que solo tiene sentido al final. Cada corte de abajo debería poder usarse de verdad para trabajo real antes de pasar al siguiente. Y desde v1 ese trabajo real incluye, por el principio de bootstrapping (§0), el propio desarrollo del harness: cada versión se construye usando la anterior.

### MVP v0 — Núcleo interactivo mínimo
**Tema:** un agente conversacional real, con herramientas reales, sobre un workspace real. Nada elegante todavía.

- **RF cubiertos:** RF-1.1 (agente + tools sobre workspace, sin subagentes aún), RF-2.1 (un solo adaptador: OpenAI-compatible vía Ollama), RF-2.3 (cambiar modelo sin reiniciar), RF-3.1 (memoria persistente simple, SQLite sin vectorial), RF-6.1-6.3 (CLI completa), RF-10.1-10.2 (git básico + shell), RF-1.4 parcial (sesión sobrevive a un cierre de cliente, sin el resto de background jobs complejos).
- **RNF cubiertos — y esto es lo que quiero remarcarte:** RNF-1.1/1.2/1.3 (métricas base), RNF-3.1 (independencia de proveedor — decisión de arquitectura que no se puede corregir después sin reescritura), RNF-4.1 (deny-por-defecto), RNF-4.3/4.4 (local-first, secrets fuera de logs), **RNF-4.5 (tratar contenido no confiable como dato, no como instrucción) y RNF-4.7 (aislamiento de shell del propio core vía contenedor/seccomp)** — no son "seguridad para después": si el core ejecuta shell sin esto desde el día uno, cada mes que pasa hace más caro meterlo retroactivamente. **RNF-4.8** (parada de emergencia) y **RNF-4.9** (allowlist de red por defecto) también entran aquí — son baratos de construir ahora y muy caros de agregar después de que el hábito de "todo pasa" ya esté instalado. RNF-6.1 (logging estructurado), RNF-7.1/7.2 (cambios de dirección a mitad de tarea, config versionable).
- **Diferido explícitamente:** subagentes, retrieval, plugins, skills, GUI, SDD, branching de sesiones, ejecución autónoma, RNF-8/RNF-9 (no aplican sin modo autónomo), adaptadores remotos.
- **Criterio de salida:** podés sostener una conversación con herramientas reales (leer/escribir archivos, correr comandos, commitear) contra un modelo local, sin que se degrade en sesiones largas, y sin que el core pueda hacer nada fuera de lo que vos declaraste permitido. Es la única versión que se construye sin sí misma: herramientas externas (OpenCode/Claude Code u otras) hacen de andamio — este hito es la semilla del bootstrapping (§0).

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
- **Criterio de salida:** el banco de pruebas de RNF-10 confirma el ≥40% de reducción de tokens sobre el Perfil A real (§5) — no es un objetivo de diseño, es un número medido. Además: todo v1 se desarrolla usando v0 como herramienta principal de trabajo — primer ejercicio real de bootstrapping, con la herramienta más cruda del ciclo.

### MVP v2 — Se extiende sin tocar el core
**Tema:** extensibilidad real vía plugins y skills, y el primer adaptador remoto.

- **RF cubiertos:** RF-4.1-4.4 (skills con lazy-load y aprobación humana), RF-5.1-5.4 (plugins WASM), **RF-2.2 (adaptadores Anthropic/Gemini)** — entra aquí, no en v0: el primer adaptador remoto es, en los hechos, el primer "plugin real" que ejercita el sistema de extensibilidad recién construido, en vez de ser un caso especial cableado al core.
- **RNF cubiertos:** RNF-3.2 (extender vía plugin sin tocar el core — ahora se prueba de verdad), RNF-3.3 (tests de integración sobre la API interna), RNF-4.2 (mínimo privilegio en plugins), RNF-4.6 (procedencia/firma de plugins y skills externos), RNF-4.12 completo (incluye ahora la mitad de skills), RNF-6.2 (grabación/replay de sesiones — necesario para que la minería de skills tenga de dónde sacar trayectorias).
- **Criterio de salida:** instalás un plugin de terceros y una skill sin recompilar el binario, y ambos corren aislados con permisos mínimos declarados. Además: v2 se desarrolla usando v1.

### MVP v3 — Trabaja en equipo consigo mismo
**Tema:** orquestación multiagente.

- **RF cubiertos:** RF-1.2/1.3 (subagentes con contexto acotado), RF-9.1-9.3 (branching, merge, multi-run).
- **RNF cubiertos:** re-validación de RNF-1.5 con subagentes reales (no solo teórica como en v1) — la cola/scheduler ahora tiene contención de verdad que probar.
- **Diferido:** RF-10.3 (integración GitHub) es explícitamente "deseable, no v1" — entra acá o después, sin bloquear nada.
- **Criterio de salida:** el orquestador delega una tarea real a un subagente con contexto acotado, y el resultado consolidado vuelve sin inflar el contexto del orquestador (medible con el banco de RNF-10). Además: v3 se desarrolla usando v2 — todavía monoagente; los subagentes que introduce esta versión se estrenan construyendo lo que viene después.

### MVP v4 — Superficies adicionales
**Tema:** GUI web, SDD formal, auto-aprendizaje activo, portabilidad validada.

- **RF cubiertos:** RF-7.1-7.4 (GUI web), RF-8.1-8.4 (SDD formal — descomposición de spec en tareas trackeables), auto-aprendizaje activo (minería de patrones sobre las trayectorias grabadas desde v2).
- **RNF cubiertos:** RNF-5.1/5.2 (portabilidad — validación real en Linux/Windows, no solo "el lenguaje lo permite"), RNF-4.11 (transporte cifrado para acceso remoto a la GUI).
- **Nota de reutilización:** el motor de descomposición de RF-8.2 (spec → tareas) es el mismo que necesita RF-11.3 — construirlo bien acá evita reconstruirlo en la fase siguiente.
- **Criterio de salida:** la GUI muestra el mismo estado que la CLI en tiempo real, y una SPEC real se descompone y trackea automáticamente sin intervención manual. Además: v4 se desarrolla usando v3 — el desarrollo mismo del SDD y la GUI sirve como banco de pruebas de la orquestación multiagente sobre tareas reales.

### v1.0 — Producto completo: ejecución autónoma end-to-end
**Tema:** RF-11 completo — el modo de mayor riesgo, por eso va último.

- **RF cubiertos:** RF-11.1-11.10 completos, casos extraordinarios (ambigüedad Tier 3, §3.7).
- **RNF cubiertos:** RNF-8.1-8.4 completos (autonomía segura), RNF-9.1-9.4 completos (techo de sensibilidad de proyecto), RNF-4.8/4.9 ya existían desde v0 pero se validan bajo carga real, RNF-4.10 (log a prueba de manipulación — recién tiene sentido con RNF-9 ya activo).
- **Rollout dentro de esta versión, no como MVP aparte:** la progresión `dry_run → supervised → checkpoint → autonomous` (§7.2) se recorre sobre proyectos reales de sensibilidad `general` antes de considerar el modo maduro.
- **Criterio de salida:** un run manifest completo corre en modo `checkpoint` sobre un proyecto real de baja sensibilidad, sin intervención fuera de los checkpoints declarados, con reporte final coherente (RF-11.10) y sin que el piso de seguridad no configurable (RNF-8.2) haya tenido que intervenir. Además: v1.0 se desarrolla usando v4, y una vez validado el flujo, parte de sus tareas restantes puede ejecutarlas el propio harness en modo `checkpoint` — el cierre natural del bootstrapping.

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

Un único archivo Markdown con front-matter YAML — mismo patrón que `SKILL.md`, consistente con el resto del proyecto. Nombre sugerido: `RUN.md`, o `.forge/runs/<run_id>.md` si se versionan múltiples corridas.

```yaml
---
run_id: implementar-modulo-facturacion
mode: autonomous              # supervised | checkpoint | autonomous | dry_run
spec_ref: ./SPEC.md            # opcional — se puede omitir si la SPEC va embebida abajo

budget:
  max_wall_clock: "6h"
  max_tokens: 3000000
  max_cost_usd: 20.00
  max_retries_per_task: 4       # circuit breaker (RF-11.5)

git:
  isolation: worktree            # obligatorio si mode != supervised
  base_branch: main
  work_branch: "run/{run_id}"
  commit_per_task: true
  merge_to_base: manual           # manual | auto_if_all_hitl_passed

hitl:
  checkpoints:
    - id: post-decomposition
      trigger: after_spec_decomposition
      required: true
    - id: pre-merge
      trigger: before_merge_to_base_branch
      required: true
    - id: rutas-sensibles
      trigger: before_editing
      match: ["**/migrations/**", "**/.env*", "**/infra/**", "**/*secret*"]
      required: true
    - id: presupuesto-50
      trigger: budget_threshold
      threshold: 0.5
      required: false

  # Piso de seguridad NO configurable (RNF-8.2) — el sistema lo aplica
  # siempre, aparezca o no en este manifest. El manifest puede AÑADIR
  # checkpoints; nunca puede QUITAR estos:
  #   - operaciones git destructivas
  #   - acceso a credenciales fuera de lo declarado
  #   - red fuera de la allowlist
  #   - presupuesto excedido
---

# Objetivo (one-shot)

<instrucción de alto nivel, en una sola frase o párrafo>

# Especificación

<contenido de la SPEC embebido aquí, si no se usó spec_ref>

# Criterios de aceptación global

- Toda la suite de tests pasa
- Lint sin errores/warnings
- Build exitoso
- (cualquier criterio adicional específico del proyecto — evitar que
  "sin errores" sea el único criterio; ver RNF-8.3)
```

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

- **0.8** — Se hace explícito el principio rector de bootstrapping (§0) y se incorpora como criterio de salida verificable en cada versión de la hoja de ruta (§6): desde v1, cada MVP se desarrolla usando el anterior. Incluye el riesgo de velocidad asociado en §5, con válvula de escape por capacidad de modelo (frontera temporal vía RF-2.1/2.3) sin suspender el bootstrapping de herramienta.
- **0.7** — Borrador inicial para validación de arquitectura.

---

*Fin del documento. Este spec es un punto de partida para iteración — no una decisión de arquitectura cerrada.*
