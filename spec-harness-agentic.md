# SPEC â€” Harness de Desarrollo Agentic a Medida

**Nombre de proyecto (working title):** `forge` *(placeholder â€” renombrar libremente)*
**VersiÃ³n del documento:** 0.8
**Estado:** Borrador para validaciÃ³n de arquitectura
**Alcance:** DefiniciÃ³n funcional, no funcional y arquitectÃ³nica de un harness de desarrollo con agentes IA, inspirado en OpenCode y Claude Code, optimizado para modelos locales, eficiencia de contexto/tokens, y extensibilidad total.

---

## 0. VisiÃ³n y motivaciÃ³n

Herramientas como OpenCode y Claude Code resuelven bien el caso general, pero:
- No estÃ¡n optimizadas para el patrÃ³n de uso especÃ­fico del operador (modelos locales, sesiones largas, cambios de direcciÃ³n espontÃ¡neos).
- Su modelo de contexto es en gran medida "todo el historial, cada turno" â€” ineficiente en tokens.
- No son forkeables/adaptables a nivel de arquitectura interna sin asumir toda la complejidad del proyecto original.

Este proyecto no busca ser "mejor en todos los ejes" que herramientas con equipo detrÃ¡s â€” busca ser **superior en el caso de uso propio**: eficiencia de contexto, control total del stack, y una arquitectura que crezca por plugins en vez de por reescritura.

**Principio rector â€” bootstrapping:** forge se construye a sÃ­ mismo. La Ãºnica versiÃ³n desarrollada con herramientas externas es v0; desde ahÃ­, cada MVP se implementa usando el MVP anterior como herramienta principal de trabajo. No es solo filosofÃ­a de proceso: es criterio de salida verificable en cada versiÃ³n (Â§6) y la prueba de honestidad del producto â€” un harness agÃ©ntico incapaz de sostener su propio desarrollo no cumple su razÃ³n de ser.

**No-objetivos explÃ­citos (v1):**
- No busca paridad de features con Claude Code/OpenCode desde el dÃ­a uno.
- No busca fine-tuning ni entrenamiento de modelos propios en esta fase.
- No busca ser un producto multiusuario/SaaS en la primera iteraciÃ³n (aunque la arquitectura no debe cerrarse esa puerta).

---

## 1. Requerimientos funcionales (RF)

### RF-1. NÃºcleo de ejecuciÃ³n de agentes
- RF-1.1 El sistema debe poder ejecutar un agente conversacional con acceso a herramientas (tool-calling) sobre un directorio de trabajo (workspace).
- RF-1.2 Debe soportar mÃºltiples agentes concurrentes dentro de una misma sesiÃ³n de proyecto (orquestador + subagentes).
- RF-1.3 El agente orquestador debe poder invocar subagentes especializados, delegando una tarea acotada con su propio contexto y set reducido de herramientas.
- RF-1.4 Debe soportar ejecuciÃ³n de tareas en segundo plano (background jobs) que continÃºan aunque el cliente (CLI/GUI) se desconecte.

### RF-2. Conectividad con proveedores de LLM
- RF-2.1 Debe soportar cualquier proveedor compatible con la API estÃ¡ndar de OpenAI (`/v1/chat/completions` o `/v1/responses`), incluyendo Ollama, llama.cpp server, vLLM, LM Studio.
- RF-2.2 Debe soportar proveedores con protocolos propios (Anthropic Messages API, Google Gemini) vÃ­a adaptadores dedicados.
- RF-2.3 Debe permitir cambiar de proveedor/modelo sin reiniciar sesiÃ³n, incluso a mitad de una tarea.
- RF-2.4 Debe soportar ruteo por costo/complejidad de la tarea â€” **esto no es solo "local vs. remoto"**: incluye usar un modelo local pequeÃ±o y rÃ¡pido (orientativamente 1-3B parÃ¡metros cuantizados en el Perfil A, Â§5) para pasos baratos (clasificaciÃ³n de intenciÃ³n, generaciÃ³n de queries de retrieval, resÃºmenes de compactaciÃ³n) y reservar un modelo mÃ¡s capaz (7-8B en Perfil A; mayor en Perfil B o remoto) para la generaciÃ³n real. Gastar cÃ³mputo de un modelo grande en un paso que uno chico resuelve igual de bien es directamente contrario al objetivo de velocidad mÃ¡xima en hardware estÃ¡ndar.
- RF-2.5 El ruteo debe ser configurable por tipo de paso del ciclo de un turno (Â§3.2) â€” no solo por "tarea" en general â€” de forma que cada paso (clasificaciÃ³n, retrieval, generaciÃ³n, validaciÃ³n) pueda apuntar a un modelo distinto.

### RF-3. GestiÃ³n de contexto y memoria
- RF-3.1 Debe mantener memoria persistente entre sesiones (decisiones de arquitectura, convenciones del proyecto, hechos "anclados").
- RF-3.2 Debe implementar recuperaciÃ³n selectiva de contexto (retrieval) en vez de enviar el historial completo en cada turno.
- RF-3.3 Debe compactar/resumir sesiones largas de forma jerÃ¡rquica y progresiva, preservando hechos ancla sin comprimir.
- RF-3.4 El usuario debe poder inspeccionar y editar manualmente quÃ© hay en la memoria persistente (transparencia total, sin caja negra).
- RF-3.5 Debe existir un mecanismo de "anclaje" explÃ­cito: el usuario o el agente pueden marcar un hecho/decisiÃ³n como permanente.

### RF-4. Skills y auto-aprendizaje
- RF-4.1 Debe soportar la creaciÃ³n, carga e instalaciÃ³n de "skills" (paquetes de instrucciones + scripts reutilizables), similar al patrÃ³n `SKILL.md`.
- RF-4.2 Las skills deben cargarse de forma perezosa (lazy-load): solo se inyectan en el contexto cuando son relevantes a la tarea detectada.
- RF-4.3 El sistema debe poder proponer la creaciÃ³n de una nueva skill a partir de una trayectoria de tarea exitosa repetida (minerÃ­a de patrones).
- RF-4.4 Debe existir un flujo de aprobaciÃ³n humana antes de que una skill auto-generada quede activa (nunca auto-aprendizaje sin supervisiÃ³n).

### RF-5. Plugins y extensibilidad
- RF-5.1 Debe soportar plugins de terceros que aÃ±adan: nuevas herramientas, nuevos proveedores de LLM, nuevos comandos de CLI, o paneles de GUI.
- RF-5.2 Los plugins deben ejecutarse en un entorno aislado (sandbox) del proceso principal.
- RF-5.3 Debe existir un manifiesto de plugin (metadatos, permisos solicitados, versiÃ³n, dependencias).
- RF-5.4 El sistema debe permitir habilitar/deshabilitar plugins sin recompilar el binario principal.

### RF-6. CLI
- RF-6.1 CLI minimalista con comandos core: iniciar sesiÃ³n, listar sesiones, adjuntar a sesiÃ³n en curso, ejecutar tarea puntual (one-shot), gestionar plugins/skills, gestionar proveedores.
- RF-6.2 Debe soportar modo interactivo (TUI) y modo no interactivo (scriptable, para CI/CD o cron).
- RF-6.3 Salida en modo no interactivo debe soportar formato JSON para integraciÃ³n con otras herramientas.

### RF-7. GUI web (opcional, desacoplada)
- RF-7.1 Debe existir un modo servidor que exponga una API sobre la cual una GUI web pueda conectarse (local o remota).
- RF-7.2 La GUI web debe ser un cliente mÃ¡s de la misma API que usa el CLI â€” no un sistema paralelo con lÃ³gica propia.
- RF-7.3 Debe soportar visualizaciÃ³n de diffs, Ã¡rbol de conversaciÃ³n/sesiÃ³n, y estado de agentes en ejecuciÃ³n.
- RF-7.4 Acceso remoto protegible con autenticaciÃ³n (password de UI como mÃ­nimo viable).

### RF-8. Desarrollo guiado por especificaciÃ³n (SDD)
- RF-8.1 El usuario debe poder definir una especificaciÃ³n (documento de spec) como artefacto de primera clase del proyecto.
- RF-8.2 El agente debe poder descomponer una spec en tareas ejecutables y trackeables.
- RF-8.3 El sistema debe poder validar (o al menos seÃ±alar divergencias) entre la implementaciÃ³n actual y la especificaciÃ³n vigente.
- RF-8.4 Los cambios de spec deben quedar versionados (historial de decisiones, no solo el estado final).

### RF-9. GestiÃ³n de sesiones
- RF-9.1 Debe soportar branching de sesiones (ramificar una conversaciÃ³n en un punto dado para explorar caminos alternativos).
- RF-9.2 Debe permitir fusionar (merge) el resultado de ramas alternativas o de ejecuciones multi-modelo.
- RF-9.3 Debe permitir ejecutar la misma tarea en paralelo contra varios modelos/proveedores y comparar resultados.

### RF-10. IntegraciÃ³n con control de versiones y entorno
- RF-10.1 Debe integrarse con git: lectura de diffs, creaciÃ³n de commits, gestiÃ³n de branches por tarea/worktree.
- RF-10.2 Debe soportar ejecuciÃ³n de comandos de shell dentro del workspace, con visibilidad completa de su salida para el agente.
- RF-10.3 (Deseable, no v1) IntegraciÃ³n con issues/PRs de GitHub u otro forge.

### RF-11. EjecuciÃ³n autÃ³noma de principio a fin (One-shot + SPEC + HITL)

**Objetivo:** dado un Ãºnico artefacto de arranque (un archivo de "run manifest") que combine (a) una instrucciÃ³n one-shot, (b) una SPEC del proyecto/tarea, y (c) una configuraciÃ³n de puntos de intervenciÃ³n humana (HITL), el sistema debe poder ejecutar el proyecto completo â€” descomponer, implementar, testear, depurar y corregir â€” sin detenerse, deteniÃ©ndose **Ãºnicamente** en los checkpoints HITL definidos o en condiciones extraordinarias de seguridad/ambigÃ¼edad.

- RF-11.1 El sistema debe aceptar un **run manifest** como Ãºnico punto de entrada de una ejecuciÃ³n autÃ³noma (ver formato propuesto en Â§7).
- RF-11.2 El run manifest debe permitir declarar, como mÃ­nimo:
  - la instrucciÃ³n/objetivo de alto nivel (one-shot),
  - la referencia o contenido de la SPEC a cumplir,
  - los checkpoints HITL (momentos explÃ­citos donde el sistema debe pausar y esperar aprobaciÃ³n/input humano),
  - los lÃ­mites de autonomÃ­a (reintentos mÃ¡ximos, presupuesto de tokens/costo, tiempo mÃ¡ximo, alcance de archivos/directorios permitidos).
- RF-11.3 El sistema debe descomponer la SPEC en una lista de tareas atÃ³micas y verificables (cada una con un criterio de "hecho" explÃ­cito: tests que pasan, lint limpio, build exitoso, o el criterio que la SPEC declare).
- RF-11.4 Para cada tarea, el sistema debe ejecutar un **ciclo de auto-correcciÃ³n acotado**: implementar â†’ validar â†’ si falla, analizar el error â†’ corregir â†’ volver a validar, hasta un lÃ­mite de reintentos configurable por tarea (no reintentos infinitos).
- RF-11.5 Si una tarea agota sus reintentos sin Ã©xito, el sistema debe tratarlo como un **checkpoint HITL implÃ­cito** (caso extraordinario) y pausar, en vez de continuar en un estado roto o marcar la tarea como completada sin estarlo.
- RF-11.6 El sistema debe continuar automÃ¡ticamente a la siguiente tarea de la SPEC solo cuando la tarea actual cumple su criterio de "hecho" â€” o cuando un HITL explÃ­cito la aprobÃ³ pese a no cumplirlo.
- RF-11.7 Al alcanzar un checkpoint HITL (explÃ­cito o extraordinario), el sistema debe: detener la ejecuciÃ³n, presentar un resumen claro del estado (quÃ© se hizo, quÃ© falta, diffs relevantes, motivo de la pausa), y esperar input humano antes de continuar â€” nunca debe inferir una aprobaciÃ³n implÃ­cita.
- RF-11.8 El sistema debe registrar un log/auditorÃ­a completo y reanudable de la corrida: si el proceso se interrumpe (crash, corte, cierre de cliente), debe poder reanudarse desde el Ãºltimo estado consistente sin reprocesar tareas ya completadas.
- RF-11.9 El sistema debe soportar niveles de autonomÃ­a configurables por proyecto o por tarea (ver tabla en Â§7.2), desde "pausa despuÃ©s de cada tarea" hasta "solo pausa en casos extraordinarios".
- RF-11.10 Al finalizar todas las tareas de la SPEC, el sistema debe generar un reporte final: tareas completadas, tareas que requirieron intervenciÃ³n, **supuestos asumidos para resolver ambigÃ¼edad Tier 2 (Â§3.7) sin pausar**, desviaciones respecto a la SPEC original (si las hubo y por quÃ©), y estado de validaciÃ³n global (todos los tests pasan, build limpio, etc.).

**Casos extraordinarios que deben pausar la ejecuciÃ³n aunque no sean un HITL declarado explÃ­citamente:**
- AcciÃ³n irreversible o destructiva fuera del alcance declarado (borrado masivo, force-push, migraciÃ³n de datos sin reversiÃ³n posible).
- AmbigÃ¼edad genuina en la SPEC (Tier 3 â€” ver Â§3.7) que no puede resolverse sin asumir una decisiÃ³n de producto/negocio no delegada al agente.
- DetecciÃ³n de una acciÃ³n que requerirÃ­a credenciales, permisos o alcance no autorizados en el manifest.
- Reintentos agotados en una tarea (ver RF-11.5).
- Presupuesto de tiempo, tokens o costo excedido respecto al lÃ­mite declarado en el manifest.
- Cualquier operaciÃ³n que el modelo de permisos (RNF-4.1) clasifique como fuera de la lista allow.
- Contenido no confiable (RNF-4.5) que contenga instrucciones dirigidas al agente (posible inyecciÃ³n de prompt) â€” se trata como dato sospechoso, nunca se actÃºa sobre lo que "pide", y se marca para revisiÃ³n.

---

## 2. Requerimientos no funcionales (RNF)

### RNF-1. Rendimiento
- RNF-1.1 Tiempo de arranque en frÃ­o (cold start) del daemon/core: objetivo < 200ms.
- RNF-1.2 Latencia aÃ±adida por el harness (overhead sobre el tiempo de inferencia del modelo) debe ser despreciable (< 50ms por turno en condiciones normales).
- RNF-1.3 Uso de memoria del proceso core en reposo: objetivo < 100MB.
- RNF-1.4 Debe soportar sesiones de larga duraciÃ³n (horas/dÃ­as) sin degradaciÃ³n de rendimiento ni fugas de memoria.
- RNF-1.5 **Concurrencia realista sobre el Perfil A (Â§5, Â§7.1):** en el hardware de referencia interactivo (CPU multinÃºcleo sin GPU discreta utilizable para inferencia), no existe ninguna ruta de paralelismo fÃ­sico real â€” un Ãºnico proceso de modelo consume los nÃºcleos disponibles de forma serializada. Todo lo que RF-1.2/RF-1.3 llaman "concurrencia de agentes" se traduce, en este perfil, en una cola con prioridad hacia ese Ãºnico proceso de inferencia â€” nunca en ejecuciÃ³n simultÃ¡nea. El planificador de solicitudes con prioridad es, en este hardware, el Ãºnico mecanismo real de "multiagente", y debe diseÃ±arse asumiendo cero paralelismo fÃ­sico como caso normal, no como degradaciÃ³n de un caso ideal.
- RNF-1.6 **Reserva de nÃºcleos:** el proceso de inferencia local no debe reservar por defecto el 100% de los nÃºcleos fÃ­sicos disponibles â€” debe dejar margen (ej. 1-2 nÃºcleos) para que el equipo siga siendo usable para otras tareas mientras el harness trabaja, salvo que el usuario indique explÃ­citamente lo contrario (ej. una corrida autÃ³noma nocturna sin uso concurrente del equipo).

### RNF-2. Eficiencia de contexto/tokens (requisito diferenciador del proyecto)
- RNF-2.1 El sistema debe medir y reportar tokens consumidos por turno, por sesiÃ³n y por proveedor.
- RNF-2.2 El diseÃ±o de contexto debe maximizar hits de prompt-caching de cada proveedor (orden estable: sistema â†’ herramientas â†’ memoria â†’ historial variable).
- RNF-2.3 Objetivo cuantitativo: reducciÃ³n de â‰¥40% en tokens de contexto respecto a un enfoque naive de "historial completo" en sesiones de mÃ¡s de 20 turnos.
- RNF-2.4 **ReutilizaciÃ³n de KV-cache en inferencia local** (el equivalente al prompt-caching remoto de RNF-2.2, pero a nivel del propio servidor de inferencia â€” ej. cache de prefijos en llama.cpp/vLLM): el diseÃ±o de contexto debe mantener un prefijo estable (mismo system prompt + mismo orden de herramientas) por sesiÃ³n/tarea. Cambiar el prefijo entre turnos de una misma sesiÃ³n anula la ganancia de velocidad mÃ¡s importante disponible en modelos locales â€” mÃ¡s relevante aÃºn que el prompt-caching remoto, porque en hardware estÃ¡ndar no hay margen de sobra que absorba el recÃ¡lculo.
- RNF-2.5 **Techo de contexto objetivo, no mÃ¡ximo tÃ©cnico:** en el Perfil A (CPU-only), el objetivo es mantener el contexto de trabajo de turnos interactivos en el orden de **4.000-8.000 tokens** â€” no el mÃ¡ximo que el modelo declare soportar. El tiempo de prefill en CPU escala de forma mucho mÃ¡s perceptible con el tamaÃ±o del contexto que en APIs con aceleraciÃ³n dedicada; un contexto que "cabe" pero estÃ¡ cerca del lÃ­mite puede ser la diferencia entre segundos y minutos de espera. Contextos mayores (ej. 16k+) quedan reservados para el Perfil B / corridas en segundo plano (RF-11), donde la tolerancia a tiempo de espera ya es explÃ­cita (`budget.max_wall_clock`).

### RNF-3. Modularidad y mantenibilidad
- RNF-3.1 El core debe ser independiente de cualquier proveedor de LLM especÃ­fico (sin acoplamiento a un SDK propietario en el nÃºcleo).
- RNF-3.2 Toda funcionalidad nueva debe poder aÃ±adirse vÃ­a plugin sin modificar el core, salvo que amplÃ­e el contrato de la API interna.
- RNF-3.3 Cobertura de tests de integraciÃ³n sobre el contrato de la API interna (no solo unitarios).

### RNF-4. Seguridad

**Modelo de confianza (superficies no confiables):** el proveedor de LLM se asume semi-confiable â€” recibe contexto para operar, pero nunca debe recibir secretos ni datos fuera de lo declarado. Todo contenido que el sistema ingiere desde fuera del propio operador â€” archivos de terceros en el repo, resultados de bÃºsqueda web, salidas de herramientas MCP, plugins/skills de origen externo â€” se asume **no confiable** y puede contener instrucciones adversariales dirigidas al agente (inyecciÃ³n de prompt), incluyendo texto que reclame autoridad del usuario, de "sistema", o del proveedor del modelo.

- RNF-4.1 EjecuciÃ³n de shell y acceso a filesystem deben pasar por un modelo de permisos explÃ­cito con **postura por defecto deny** (nada permitido salvo lo declarado) â€” no acceso irrestricto por defecto.
- RNF-4.2 Plugins deben ejecutarse con privilegios mÃ­nimos y declarar permisos requeridos.
- RNF-4.3 NingÃºn dato de sesiÃ³n/proyecto debe salir del entorno local sin acciÃ³n explÃ­cita del usuario (por defecto: local-first).
- RNF-4.4 Secrets (API keys, tokens) nunca en texto plano en logs ni en el store de memoria persistente **ni en el contexto enviado a un proveedor de LLM** â€” deben redactarse antes de que la salida de una herramienta (comando de shell, respuesta HTTP, etc.) entre al contexto del modelo.
- RNF-4.5 Contenido de fuentes no confiables debe tratarse siempre como **datos, nunca como instrucciones** â€” cualquier acciÃ³n que ese contenido "solicite" debe pasar por el mismo modelo de permisos que cualquier otra acciÃ³n del agente; el sistema no ejecuta instrucciones encontradas dentro de archivos, pÃ¡ginas web, o salidas de herramientas.
- RNF-4.6 Plugins y skills de origen externo (no creados por el propio usuario) requieren verificaciÃ³n de procedencia (checksum/firma) antes de cargarse, y aprobaciÃ³n humana explÃ­cita antes de su primera ejecuciÃ³n â€” el registro de plugins (Â§3.5) debe distinguir "creado localmente" de "instalado de fuente externa".
- RNF-4.7 La ejecuciÃ³n de shell del propio core (no solo de plugins) debe correr con aislamiento adicional a nivel de sistema operativo â€” el modelo de permisos declara *quÃ©* estÃ¡ autorizado; el aislamiento de SO es la segunda capa que contiene el daÃ±o si el modelo de permisos falla o es evadido. **Matiz por plataforma (Â§6):** en Linux, exigible desde v0 vÃ­a seccomp/Landlock (primitivas estables y documentadas). En macOS, `sandbox-exec` queda descartado por ser una API no documentada/en desuso de Apple â€” v0 en macOS se limita al modelo de permisos (RNF-4.1) sin aislamiento de SO, con el aislamiento duro diferido a v1 pendiente de un mecanismo estable.
- RNF-4.8 Debe existir una **parada de emergencia** accesible desde cualquier cliente (CLI/GUI) que detenga de inmediato cualquier ejecuciÃ³n en curso â€” interactiva o autÃ³noma â€” dejando el estado en el Ãºltimo punto consistente conocido (ligado a RNF-8.4).
- RNF-4.9 Todo adaptador de proveedor (Â§3.1) debe operar con una allowlist de red explÃ­cita como comportamiento **por defecto del sistema en cualquier modo** â€” no solo durante ejecuciÃ³n autÃ³noma (RF-11).
- RNF-4.10 El log/auditorÃ­a de una corrida (RNF-6.2, RF-11.8) debe ser a prueba de manipulaciÃ³n (append-only o con encadenamiento verificable) cuando el proyecto tenga clasificaciÃ³n `regulado` o `datos-sensibles` (RNF-9) â€” un log editable despuÃ©s de los hechos no sirve como evidencia de cumplimiento.
- RNF-4.11 El acceso remoto a la GUI web (RF-7.4) debe viajar sobre transporte cifrado por defecto (TLS o tÃºnel cifrado) â€” una contraseÃ±a sobre una conexiÃ³n sin cifrar es una traba mÃ­nima, no un control de acceso remoto aceptable.
- RNF-4.12 Un hecho derivado de contenido no confiable que el sistema proponga anclar en memoria persistente (RF-3.5) o destilar en una skill (RF-4.3) debe pasar por el mismo flujo de aprobaciÃ³n humana que ya exige RF-4.4 para skills auto-generadas â€” nunca se ancla ni se promueve automÃ¡ticamente solo porque "funcionÃ³ una vez".

### RNF-5. Portabilidad
- RNF-5.1 Debe correr en Linux, macOS (Intel y Apple Silicon) y, deseable, Windows.
- RNF-5.2 No debe depender de servicios de infraestructura externos obligatorios (todo debe poder correr 100% local).

### RNF-6. Observabilidad
- RNF-6.1 Logging estructurado (JSON) con niveles configurables.
- RNF-6.2 GrabaciÃ³n y replay de sesiones completas (para debugging y para minerÃ­a de skills).
- RNF-6.3 MÃ©tricas de costo estimado por sesiÃ³n/proveedor cuando aplique (modelos de pago).

### RNF-7. Usabilidad / adaptabilidad al operador
- RNF-7.1 Debe soportar cambios de direcciÃ³n a mitad de tarea sin perder el estado ya construido (no exige reiniciar sesiÃ³n ante un cambio de idea).
- RNF-7.2 ConfiguraciÃ³n por proyecto debe ser versionable junto al cÃ³digo (archivo de config en el repo).

### RNF-8. AutonomÃ­a segura (ejecuciÃ³n desatendida) â€” ligado a RF-11
- RNF-8.1 Toda ejecuciÃ³n en modo autÃ³nomo debe operar sobre un worktree/branch aislado â€” nunca directamente sobre la rama base sin una aprobaciÃ³n HITL explÃ­cita de merge.
- RNF-8.2 Debe existir un **piso de seguridad no configurable** (no deshabilitable desde el run manifest, sin excepciÃ³n) para: operaciones git destructivas (force-push, `reset --hard`, borrado de branch), acceso a credenciales/secrets fuera de lo declarado, llamadas de red fuera de la allowlist, y exceso de presupuesto (tiempo/tokens/costo). Esto existe independientemente de lo que el usuario configure en HITL â€” el manifest puede *aÃ±adir* checkpoints, nunca *quitar* estos.
- RNF-8.3 El criterio de "tarea completada" no puede reducirse a "el proceso no arrojÃ³ error". Debe incluir verificaciÃ³n positiva del comportamiento esperado (tests que ejercitan el criterio declarado en la SPEC, no solo ausencia de excepciÃ³n) â€” de lo contrario el agente puede optimizar hacia la mÃ©trica equivocada (p. ej., debilitar o comentar un test para que "pase").
- RNF-8.4 Cada tarea completada en modo autÃ³nomo debe quedar como un commit atÃ³mico y reversible de forma independiente â€” nunca un commit gigante al final de la corrida.

### RNF-9. ClasificaciÃ³n de sensibilidad del proyecto â€” techo de autonomÃ­a
- RNF-9.1 Cada proyecto debe declarar, una Ãºnica vez en su configuraciÃ³n versionada (no en cada run manifest individual), una clasificaciÃ³n de sensibilidad: `general`, `regulado`, o `datos-sensibles`.
- RNF-9.2 Esta clasificaciÃ³n actÃºa como un **techo** sobre el nivel de autonomÃ­a permitido (Â§7.2), independiente de lo que solicite un run manifest puntual:
  - `datos-sensibles` (ej. informaciÃ³n de salud u otra especialmente protegida) â†’ techo duro en `supervised`, sin excepciÃ³n configurable.
  - `regulado` (ej. cumplimiento normativo, procesos con validez legal) â†’ techo en `checkpoint`, con el checkpoint `pre-merge` fijo en `required: true`, no removible.
  - `general` â†’ sin techo adicional; sigue la progresiÃ³n normal hasta `autonomous` (Â§7.2).
- RNF-9.3 Un run manifest que solicite un nivel de autonomÃ­a por encima del techo de su proyecto debe ser **rechazado** en la fase de validaciÃ³n (paso 1 del ciclo, Â§3.6) â€” nunca degradado silenciosamente ni ejecutado con una advertencia ignorable.
- RNF-9.4 Cambiar la clasificaciÃ³n de sensibilidad de un proyecto debe requerir una acciÃ³n humana explÃ­cita, registrada (quiÃ©n, cuÃ¡ndo, por quÃ©) â€” nunca una decisiÃ³n que el propio agente pueda tomar o proponer como "hecha".

### RNF-10. ValidaciÃ³n empÃ­rica de rendimiento
- RNF-10.1 Debe existir un banco de pruebas repetible que mida, como mÃ­nimo: tokens/segundo de generaciÃ³n, latencia al primer token (TTFT), tiempo de prefill por tamaÃ±o de contexto, y tiempo total de pared para un conjunto de tareas representativas.
- RNF-10.2 El banco de pruebas debe correr sobre los **dos perfiles de hardware de referencia definidos en Â§5** (Perfil A â€” interactivo/estÃ¡ndar; Perfil B â€” batch/autÃ³nomo), reportando mÃ©tricas por separado para cada uno â€” no un promedio combinado que oculte la brecha real entre ambos.
- RNF-10.3 Los objetivos cuantitativos de RNF-1 y RNF-2 (â‰¥40% de reducciÃ³n de tokens, cold-start <200ms, etc.) deben validarse contra este banco de pruebas antes de darse por cumplidos â€” son mÃ©tricas verificables, no afirmaciones de diseÃ±o.

---

## 3. Arquitectura general

### 3.1 Vista de alto nivel

```
                         â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
                         â”‚         CLIENTES           â”‚
                         â”‚                            â”‚
                         â”‚  â”Œâ”€â”€â”€â”€â”€â”€â”  â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â” â”‚
                         â”‚  â”‚  CLI â”‚  â”‚  GUI Web    â”‚ â”‚
                         â”‚  â”‚(TUI) â”‚  â”‚ (browser)   â”‚ â”‚
                         â”‚  â””â”€â”€â”€â”¬â”€â”€â”˜  â””â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”˜ â”‚
                         â”‚      â”‚            â”‚        â”‚
                         â”‚      â”‚  â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜        â”‚
                         â”‚      â”‚  â”‚  (futuro: mÃ³vil,  â”‚
                         â”‚      â”‚  â”‚   VS Code ext.)   â”‚
                         â””â”€â”€â”€â”€â”€â”€â”¼â”€â”€â”¼â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
                                â”‚  â”‚
                                â–¼  â–¼
                    â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
                    â”‚   API INTERNA (RPC)     â”‚   â† contrato Ãºnico,
                    â”‚  JSON-RPC / WebSocket   â”‚     todos los clientes
                    â”‚  eventos en streaming   â”‚     hablan lo mismo
                    â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
                                â”‚
                                â–¼
        â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
        â”‚                    CORE (daemon)                   â”‚
        â”‚                                                      â”‚
        â”‚  â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”   â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”    â”‚
        â”‚  â”‚ Orquestador de â”‚   â”‚  Gestor de Contexto     â”‚    â”‚
        â”‚  â”‚    Agentes     â”‚â—„â”€â–ºâ”‚  (retrieval, compact.,  â”‚    â”‚
        â”‚  â”‚ (supervisor +  â”‚   â”‚   anclaje, resumen)     â”‚    â”‚
        â”‚  â”‚  subagentes)   â”‚   â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜    â”‚
        â”‚  â””â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”˜               â”‚                 â”‚
        â”‚          â”‚                        â–¼                 â”‚
        â”‚          â”‚              â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”        â”‚
        â”‚          â”‚              â”‚  Memoria Persist. â”‚        â”‚
        â”‚          â”‚              â”‚  SQLite + vector   â”‚        â”‚
        â”‚          â”‚              â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜        â”‚
        â”‚          â–¼                                          â”‚
        â”‚  â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”   â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”    â”‚
        â”‚  â”‚ Motor de Tools  â”‚   â”‚  Registro de Skills     â”‚    â”‚
        â”‚  â”‚ (MCP + nativas) â”‚â—„â”€â–ºâ”‚  (lazy-load, manifest)  â”‚    â”‚
        â”‚  â””â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”˜   â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜    â”‚
        â”‚          â”‚                                          â”‚
        â”‚          â–¼                                          â”‚
        â”‚  â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”   â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”    â”‚
        â”‚  â”‚ Sandbox/Permisosâ”‚   â”‚  Registro de Plugins    â”‚    â”‚
        â”‚  â”‚  (WASM runtime) â”‚â—„â”€â–ºâ”‚  (manifest, versiones)  â”‚    â”‚
        â”‚  â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜   â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜    â”‚
        â”‚                                                      â”‚
        â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
                                 â”‚
                                 â–¼
                 â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
                 â”‚   ADAPTADORES DE PROVEEDOR      â”‚
                 â”‚                                 â”‚
                 â”‚  â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”  â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â” â”‚
                 â”‚  â”‚ OpenAI-  â”‚  â”‚  Anthropic  â”‚ â”‚
                 â”‚  â”‚compatibleâ”‚  â”‚  Messages   â”‚ â”‚
                 â”‚  â”‚(Ollama,  â”‚  â”‚  API        â”‚ â”‚
                 â”‚  â”‚ llama.cppâ”‚  â”‚             â”‚ â”‚
                 â”‚  â”‚ vLLM,LM  â”‚  â”‚             â”‚ â”‚
                 â”‚  â”‚ Studio)  â”‚  â”‚             â”‚ â”‚
                 â”‚  â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜  â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜ â”‚
                 â”‚  â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”                   â”‚
                 â”‚  â”‚  Gemini  â”‚   (+ futuros)      â”‚
                 â”‚  â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜                   â”‚
                 â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
```

### 3.2 Flujo de un turno de conversaciÃ³n

```
Usuario escribe mensaje
        â”‚
        â–¼
â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
â”‚ 1. ClasificaciÃ³n   â”‚  â†’ detecta tipo de tarea, complejidad,
â”‚    de intenciÃ³n    â”‚     decide si delega a subagente
â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
          â–¼
â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
â”‚ 2. Ensamblado de   â”‚  â†’ recupera SOLO fragmentos relevantes
â”‚    contexto        â”‚     (retrieval vectorial + resumen
â”‚    (NO historial   â”‚     rodante + hechos anclados)
â”‚    completo)        â”‚
â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
          â–¼
â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
â”‚ 3. Carga de skills  â”‚  â†’ solo las skills activadas por la
â”‚    y tools           â”‚     tarea detectada (lazy-load)
â”‚    relevantes        â”‚
â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
          â–¼
â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
â”‚ 4. Orden de layout  â”‚  â†’ sistema fijo â†’ tools â†’ memoria â†’
â”‚    optimizado para  â”‚     historial variable (maximiza
â”‚    prompt-caching   â”‚     cache hits del proveedor)
â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
          â–¼
â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
â”‚ 5. Llamada al       â”‚
â”‚    proveedor LLM    â”‚
â”‚    (streaming)      â”‚
â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
          â–¼
â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
â”‚ 6. Tool-calling     â”‚  â†’ ejecuta vÃ­a Motor de Tools
â”‚    (si aplica)      â”‚     (sandbox WASM si es plugin)
â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
          â–¼
â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
â”‚ 7. Persistencia     â”‚  â†’ guarda turno, actualiza memoria,
â”‚    incremental       â”‚     evalÃºa si corresponde compactar
â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
          â–¼
â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
â”‚ 8. Streaming de     â”‚
â”‚    respuesta al     â”‚
â”‚    cliente          â”‚
â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
```

### 3.3 Modelo de compactaciÃ³n jerÃ¡rquica de contexto

```
â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
â”‚                  CONTEXTO DE UNA SESIÃ“N                   â”‚
â”‚                                                             â”‚
â”‚  â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”   â”‚
â”‚  â”‚ NIVEL 0 â€” Hechos anclados (nunca se comprimen)      â”‚   â”‚
â”‚  â”‚  Â· decisiones de arquitectura                       â”‚   â”‚
â”‚  â”‚  Â· convenciones de cÃ³digo del proyecto               â”‚   â”‚
â”‚  â”‚  Â· specs vigentes                                    â”‚   â”‚
â”‚  â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜   â”‚
â”‚  â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”   â”‚
â”‚  â”‚ NIVEL 1 â€” Resumen de proyecto (rodante, se          â”‚   â”‚
â”‚  â”‚  actualiza incrementalmente turno a turno)           â”‚   â”‚
â”‚  â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜   â”‚
â”‚  â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”   â”‚
â”‚  â”‚ NIVEL 2 â€” Resumen de sesiÃ³n actual (se recompacta   â”‚   â”‚
â”‚  â”‚  cada N turnos o al superar umbral de tokens)        â”‚   â”‚
â”‚  â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜   â”‚
â”‚  â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”   â”‚
â”‚  â”‚ NIVEL 3 â€” Turnos recientes en crudo (ventana         â”‚   â”‚
â”‚  â”‚  deslizante, sin comprimir)                          â”‚   â”‚
â”‚  â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜   â”‚
â”‚                                                             â”‚
â”‚  Retrieval selectivo: en cada turno, se recuperan          â”‚
â”‚  fragmentos de niveles 0-2 SOLO si son semÃ¡nticamente       â”‚
â”‚  relevantes a la tarea actual (embedding query contra       â”‚
â”‚  el store vectorial) â€” no se inyectan completos por         â”‚
â”‚  defecto.                                                   â”‚
â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
```

### 3.4 OrquestaciÃ³n de agentes (supervisor / subagentes)

```
                    â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
                    â”‚  Agente Orquestador     â”‚
                    â”‚  (contexto completo del â”‚
                    â”‚   proyecto + spec)      â”‚
                    â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
                                 â”‚  delega tarea acotada
              â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¼â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
              â–¼                  â–¼                  â–¼
     â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â” â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â” â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
     â”‚  Subagente A      â”‚ â”‚  Subagente B      â”‚ â”‚  Subagente C      â”‚
     â”‚  (ctx acotado:    â”‚ â”‚  (ctx acotado:    â”‚ â”‚  (ctx acotado:    â”‚
     â”‚   solo archivos    â”‚ â”‚   solo tests       â”‚ â”‚   solo docs/spec  â”‚
     â”‚   del mÃ³dulo X)    â”‚ â”‚   relevantes)      â”‚ â”‚   afectada)       â”‚
     â”‚  tools: {read,     â”‚ â”‚  tools: {run_test, â”‚ â”‚  tools: {read,    â”‚
     â”‚   write, grep}     â”‚ â”‚   read}            â”‚ â”‚   write}          â”‚
     â””â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜ â””â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜ â””â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
              â”‚                    â”‚                    â”‚
              â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¼â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
                                   â–¼
                    â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
                    â”‚  Resultado consolidado  â”‚
                    â”‚  vuelve al orquestador  â”‚
                    â”‚  (solo el resumen, no    â”‚
                    â”‚   el contexto completo   â”‚
                    â”‚   del subagente)         â”‚
                    â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
```

Cada subagente recibe **solo** el contexto necesario para su sub-tarea â€” esto es en sÃ­ mismo un mecanismo de eficiencia de tokens: el orquestador nunca carga en su propio contexto el detalle completo de lo que hizo cada subagente, solo el resultado consolidado.

### 3.5 Plugins y skills (extensibilidad)

```
â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
â”‚                    REGISTRO DE PLUGINS                  â”‚
â”‚                                                          â”‚
â”‚  manifest.toml (por plugin):                            â”‚
â”‚    name, version, permissions[], entrypoint.wasm         â”‚
â”‚                                                          â”‚
â”‚  â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”   â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”   â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”      â”‚
â”‚  â”‚ Plugin: gitâ”‚   â”‚Plugin: dockerâ”‚  â”‚Plugin: jiraâ”‚      â”‚
â”‚  â”‚  extendido  â”‚   â”‚  management  â”‚  â”‚ integration â”‚      â”‚
â”‚  â”‚  (WASM)     â”‚   â”‚  (WASM)      â”‚  â”‚  (WASM)     â”‚      â”‚
â”‚  â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜   â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜   â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜      â”‚
â”‚                                                          â”‚
â”‚  Cada plugin corre en runtime WASM aislado (wasmtime/    â”‚
â”‚  wasmer) â€” no accede a filesystem/red salvo lo que el     â”‚
â”‚  manifest declara y el usuario aprueba.                   â”‚
â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜

â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
â”‚                    REGISTRO DE SKILLS                    â”‚
â”‚                                                          â”‚
â”‚  .forge/skills/                                          â”‚
â”‚    â”œâ”€â”€ deploy-checklist/SKILL.md                          â”‚
â”‚    â”œâ”€â”€ code-review-style/SKILL.md                         â”‚
â”‚    â””â”€â”€ db-migration-pattern/SKILL.md                      â”‚
â”‚                                                          â”‚
â”‚  Cada SKILL.md se indexa (embedding del frontmatter/      â”‚
â”‚  descripciÃ³n) â†’ se activa solo si la tarea detectada       â”‚
â”‚  matchea semÃ¡nticamente con la descripciÃ³n de la skill.    â”‚
â”‚                                                          â”‚
â”‚  MinerÃ­a de skills: trayectorias exitosas repetidas se     â”‚
â”‚  destilan periÃ³dicamente en propuestas de nuevas skills,   â”‚
â”‚  presentadas al usuario para aprobaciÃ³n antes de activarse.â”‚
â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
```

### 3.6 Ciclo de ejecuciÃ³n autÃ³noma (Run Manifest â€” RF-11)

```
   [Run Manifest: one-shot + SPEC + config HITL]
                    â”‚
                    â–¼
        â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
        â”‚ 1. Parseo y validaciÃ³n  â”‚  â†’ valida schema, resuelve
        â”‚    del manifest          â”‚     spec_ref, verifica lÃ­mites
        â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
                    â–¼
        â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
        â”‚ 2. Aislamiento de       â”‚  â†’ crea worktree/branch dedicado
        â”‚    entorno (git)        â”‚     (NUNCA sobre base_branch)
        â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
                    â–¼
        â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
        â”‚ 3. DescomposiciÃ³n de    â”‚  â†’ SPEC â†’ lista de tareas
        â”‚    la SPEC en tareas    â”‚     atÃ³micas + criterio "hecho"
        â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
                    â–¼
         Â¿HITL "post-decomposition"?â”€â”€SÃ­â”€â”€â–º [PAUSA: revisiÃ³n humana
                    â”‚                         del plan de tareas]
                    No
                    â–¼
     â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
     â”‚           LOOP por cada tarea de la SPEC         â”‚
     â”‚                                                   â”‚
     â”‚   a. Implementar                                  â”‚
     â”‚        â”‚                                          â”‚
     â”‚        â–¼                                          â”‚
     â”‚   b. Validar (tests/lint/build/criterio de la SPEC)â”‚
     â”‚        â”‚                                          â”‚
     â”‚   Â¿Pasa? â”€SÃ­â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”                   â”‚
     â”‚        â”‚No                    â”‚                   â”‚
     â”‚        â–¼                      â”‚                   â”‚
     â”‚   c. Diagnosticar + corregir   â”‚                   â”‚
     â”‚      (reintento N de MAX_N)    â”‚                   â”‚
     â”‚        â”‚                       â”‚                   â”‚
     â”‚   Â¿N > MAX_N? â”€SÃ­â”€â–º [PAUSA: circuit breaker â€”       â”‚
     â”‚        â”‚No           caso extraordinario, RF-11.5]  â”‚
     â”‚        â””â”€â”€â”€â”€â”€â”€â–º volver a (b)   â”‚                   â”‚
     â”‚                                â–¼                   â”‚
     â”‚                    d. Commit atÃ³mico de la tarea    â”‚
     â”‚                       (en el worktree aislado)       â”‚
     â”‚                                â”‚                    â”‚
     â”‚              Â¿HITL para esta tarea/archivo/patrÃ³n?    â”‚
     â”‚                SÃ­ â”‚                    No             â”‚
     â”‚                   â–¼                     â”‚             â”‚
     â”‚        [PAUSA: revisiÃ³n humana]         â”‚             â”‚
     â”‚                   â”‚                     â”‚             â”‚
     â”‚                   â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜             â”‚
     â”‚                              â–¼                        â”‚
     â”‚                   Â¿Quedan tareas? â”€SÃ­â”€â–º volver a (a)    â”‚
     â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¼â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
                                    No
                                    â–¼
        â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
        â”‚ 4. ValidaciÃ³n global    â”‚  â†’ toda la SPEC cumplida,
        â”‚    (todas las tareas)   â”‚     suite completa de tests
        â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
                    â–¼
         Â¿HITL "pre-merge"? â”€SÃ­â”€â”€â–º [PAUSA: aprobar merge a base_branch]
                    â”‚No
                    â–¼
        â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
        â”‚ 5. Merge a base_branch  â”‚  (solo si fue aprobado o el nivel
        â”‚    (si aplica)          â”‚   de autonomÃ­a lo permite, Â§7.2)
        â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¬â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
                    â–¼
        â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
        â”‚ 6. Reporte final        â”‚  â†’ tareas completadas, HITLs
        â”‚    (RF-11.10)           â”‚     activados, desviaciones,
        â”‚                          â”‚     estado de validaciÃ³n
        â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜

  Disparadores de pausa que NO dependen del manifest â€” piso de
  seguridad no configurable (RNF-8.2), siempre activo:
    Â· operaciÃ³n git destructiva (force-push, reset --hard, borrado de branch)
    Â· acceso a credenciales/secrets fuera de lo declarado
    Â· llamada de red fuera de la allowlist
    Â· presupuesto (tiempo/tokens/costo) excedido
```

### 3.7 DetecciÃ³n de ambigÃ¼edad genuina en la SPEC (Tier 1/2/3)

**El problema con dejarlo en "el agente pausa si algo es ambiguo":** no es un criterio operable â€” un LLM puede reportar "ambigÃ¼edad" ante cualquier dificultad de implementaciÃ³n, o no reportarla nunca porque siempre encuentra *alguna* interpretaciÃ³n razonable. Hace falta una prueba explÃ­cita, no un juicio abierto.

**Prueba de multiplicidad (correlato del test real):** antes de implementar una tarea, el agente debe poder articular explÃ­citamente si existen **dos o mÃ¡s interpretaciones vÃ¡lidas** que satisfacen la letra del criterio "hecho" de la SPEC, pero que producen comportamiento observable distinto (no solo detalle interno de implementaciÃ³n). Si no puede articular una segunda interpretaciÃ³n divergente, no hay ambigÃ¼edad â€” hay una tarea normal.

```
                    Â¿Existen â‰¥2 interpretaciones vÃ¡lidas
                     con comportamiento OBSERVABLE distinto?
                              â”‚
                 No â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”¼â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€ SÃ­
                  â”‚                        â”‚
                  â–¼                        â–¼
         TIER 1 â€” No es ambiguo    Â¿La divergencia ya estÃ¡ resuelta
         (detalle interno de       por una convenciÃ³n anclada del
         implementaciÃ³n: libre-    proyecto (Nivel 0, Â§3.3) o por
         rÃ­a, nombres internos,    un default documentado del
         organizaciÃ³n de          harness?
         archivos, etc.)                   â”‚
                  â”‚              SÃ­ â”€â”€â”€â”€â”€â”€â”€â”¼â”€â”€â”€â”€â”€â”€â”€ No
                  â–¼               â”‚                  â”‚
         Proceder sin              â–¼                  â–¼
         pausar, sin       TIER 2 â€” AmbigÃ¼edad     Â¿La divergencia cae en:
         registrar          resuelta por          producto/negocio, seguridad/
         nada especial.     convenciÃ³n/default     privacidad, cumplimiento
                             existente               legal, costo significativo,
                                  â”‚                  o irreversibilidad de datos?
                                  â–¼                          â”‚
                          Proceder, PERO             SÃ­ â”€â”€â”€â”€â”€â”¼â”€â”€â”€â”€â”€ No
                          registrar el                â”‚              â”‚
                          supuesto asumido             â–¼              â–¼
                          en el reporte final    TIER 3 â€” PAUSA   Aplicar heurÃ­stica
                          (RF-11.10)             HITL obligatoria  conservadora por
                                                  Presentar:        defecto (opciÃ³n
                                                  Â· pasaje exacto    mÃ¡s reversible /
                                                    de la SPEC que   mÃ¡s restrictiva),
                                                    dispara el caso  tratar como TIER 2
                                                  Â· interpretaciones (registrar supuesto)
                                                    candidatas
                                                    enumeradas
                                                  Â· categorÃ­a que lo
                                                    clasifica como
                                                    Tier 3 y por quÃ©
```

**HeurÃ­sticas de detecciÃ³n (primera pasada, durante RF-11.3 â€” descomposiciÃ³n):**
- *Palabras-bandera lÃ©xicas*: escanear la SPEC en busca de tÃ©rminos que histÃ³ricamente correlacionan con sub-especificaciÃ³n â€” "segÃºn corresponda", "de forma adecuada", "razonable", "rÃ¡pido"/"seguro" sin umbral numÃ©rico, "similar a", "etc.", "entre otros", "TBD", "TODO", "a definir". No dispara Tier 3 por sÃ­ solo â€” marca la secciÃ³n para escrutinio explÃ­cito de multiplicidad en la descomposiciÃ³n.
- *Referencias no resueltas*: toda entidad que la SPEC asume como existente (archivo, endpoint, config key, servicio externo, tabla) debe resolverse contra el repo/documentaciÃ³n real. Lo que no resuelve, se marca como candidato a Tier 3 (no se asume su existencia ni su forma).
- *Chequeo de conflictos entre tareas*: al construir el grafo de tareas, comparar restricciones declaradas en distintas secciones/tareas sobre el mismo sujeto (ej. "el sistema debe X" en una secciÃ³n y "el sistema no debe X" o algo incompatible en otra) â€” contradicciÃ³n directa es Tier 3 automÃ¡tico, no pasa por la prueba de multiplicidad.

**Sesgo deliberado del diseÃ±o:** ante la duda sobre si algo es Tier 2 o Tier 3, el sistema debe **sesgar hacia Tier 3 (pausar)**. Un falso positivo cuesta una interrupciÃ³n HITL; un falso negativo significa que el agente tomÃ³ una decisiÃ³n de producto/negocio/seguridad por su cuenta sin que nadie se enterara hasta el reporte final â€” asimetrÃ­a de costo que justifica el sesgo conservador.

---

## 4. Stack tecnolÃ³gico propuesto

| Componente | ElecciÃ³n | RazÃ³n |
|---|---|---|
| Lenguaje del core | **Go** | Cold-start rÃ¡pido, bajo footprint de memoria, concurrencia nativa simple, sin curva de aprendizaje excesiva |
| API interna | JSON-RPC 2.0 sobre WebSocket | Contrato Ãºnico para todos los clientes; streaming de eventos nativo |
| CLI/TUI | `cobra` + `bubbletea` (Go) | EstÃ¡ndar de facto en el ecosistema Go para CLIs modernas |
| Protocolo de herramientas | **MCP (Model Context Protocol)** | Reutiliza ecosistema existente de servidores MCP en vez de inventar uno propio |
| Sandbox de plugins | **WASM** (wasmtime o wasmer) | Aislamiento real, agnÃ³stico de lenguaje de implementaciÃ³n del plugin |
| Memoria estructurada | **SQLite** | Embebido, cero dependencias externas, transaccional |
| Memoria semÃ¡ntica | **sqlite-vec** o **LanceDB embebido** | Vectorial local-first, sin servicio externo |
| Adaptadores de proveedor | Interfaz comÃºn + implementaciones: OpenAI-compatible (Ollama/llama.cpp/vLLM/LM Studio), Anthropic Messages API, Gemini API | Cobertura amplia sin acoplar el core a un solo proveedor |
| GUI web | SPA liviana (framework a definir â€” React/Svelte) consumiendo la misma API interna | Desacoplada del core; un cliente mÃ¡s |
| Observabilidad | Logging estructurado (`zerolog`/`zap`) + grabaciÃ³n de sesiÃ³n en SQLite | Debug y minerÃ­a de skills |
| CuantizaciÃ³n / backend de inferencia | GGUF **Q4_K_M** como default vÃ­a Ollama/llama.cpp, backend CPU en Perfil A (sin ruta de aceleraciÃ³n GPU utilizable en ese hardware, Â§5) | Balance memoria/velocidad/calidad razonable para 7-8B en 32GB de RAM sin GPU discreta |

---

## 5. Riesgos y supuestos abiertos

- **Riesgo de alcance**: la lista de RF es amplia; sin fases, el proyecto corre riesgo de no converger nunca a un MVP usable. Ver secciÃ³n 6.
- **Supuesto**: "SDD" se interpreta como Spec-Driven Development â€” a confirmar.
- **Riesgo tÃ©cnico**: MCP como estÃ¡ndar de tools estÃ¡ en evoluciÃ³n activa; el adaptador debe diseÃ±arse con capa de compatibilidad para no romper ante cambios de protocolo.
- **Riesgo de mantenimiento**: proyecto de un solo desarrollador â€” mismo "bus factor" seÃ±alado para OpenChamber. DocumentaciÃ³n interna exhaustiva es mitigaciÃ³n mÃ­nima, no soluciÃ³n completa.
- **Perfiles de hardware de referencia (resuelto):**
  - **Perfil A â€” interactivo/estÃ¡ndar:** MacBook Pro, Intel Core i9 de 8 nÃºcleos @2.3GHz, 32GB DDR4-2667MHz, sin GPU discreta utilizable para inferencia (Intel UHD 630 integrada â€” sin ruta de aceleraciÃ³n real para LLMs). Inferencia 100% CPU. Dirige los objetivos de RNF-1/RNF-2 para el flujo interactivo â€” es el hardware "estÃ¡ndar" al que se refiere el RF principal del proyecto.
  - **Perfil B â€” batch/autÃ³nomo:** VM `llm` institucional (~62GiB RAM), tambiÃ©n CPU-bound pero con mÃ¡s margen de RAM/nÃºcleos. Reservado para corridas en segundo plano (RF-11) donde `budget.max_wall_clock` ya asume tolerancia de horas â€” permite modelos mÃ¡s grandes y contextos mÃ¡s largos que el Perfil A.
  - **ImplicaciÃ³n concreta para modelos:** en el Perfil A, el objetivo son modelos de 1-3B parÃ¡metros (cuantizados, ej. GGUF Q4_K_M) para los pasos baratos del ruteo (RF-2.4/2.5 â€” clasificaciÃ³n, queries de retrieval, resÃºmenes) y de 7-8B para generaciÃ³n real. Modelos de 13B+ son usables pero notablemente mÃ¡s lentos en CPU; 30B+ se considera poco prÃ¡ctico para trabajo interactivo en este perfil y se reserva para el Perfil B. Estas cifras son una hipÃ³tesis de diseÃ±o razonable, no una mediciÃ³n â€” deben confirmarse con el banco de pruebas de RNF-10 antes de fijarse como objetivo definitivo.
- **Riesgo de autonomÃ­a plena (RF-11)**: es, con diferencia, el componente de mayor riesgo del sistema. Un ciclo de auto-correcciÃ³n mal acotado puede degenerar en bucles infinitos, gasto descontrolado de tokens/costo, o "arreglos" que satisfacen la mÃ©trica (tests pasan) sin satisfacer la intenciÃ³n real de la SPEC. Las mitigaciones de diseÃ±o (circuit breaker en RF-11.5, piso de seguridad no configurable en RNF-8.2, aislamiento obligatorio en worktree en RNF-8.1) reducen el riesgo pero no lo eliminan â€” no reemplazan revisiÃ³n humana real en los primeros usos de este modo, especialmente sobre proyectos/repos que importan.
- **Alcance de la seguridad por diseÃ±o (RNF-4, RNF-9)**: lo cubierto en este documento es una primera pasada de diseÃ±o â€” postura deny-por-defecto, tratamiento de contenido no confiable, aislamiento por capas, allowlist de red, log a prueba de manipulaciÃ³n. No reemplaza un modelado de amenazas formal ni una revisiÃ³n de seguridad externa. Antes de habilitar v1.0 (ejecuciÃ³n autÃ³noma, Â§6) sobre un proyecto `regulado` o `datos-sensibles` real, corresponde una revisiÃ³n dedicada â€” no basta con que el diseÃ±o en papel luzca razonable.
- **Riesgo de velocidad del bootstrapping (Â§0)**: construir v1 usando v0 es hacerlo con la herramienta mÃ¡s dÃ©bil del ciclo â€” modelo local chico, sin retrieval ni compactaciÃ³n (justo lo que se estÃ¡ construyendo), y con los bugs propios de una v0. El costo es tiempo puro frente a usar OpenCode/Claude Code para el mismo trabajo. *MitigaciÃ³n:* aceptarlo como inversiÃ³n de validaciÃ³n â€” si v0 no alcanza para construir v1, la tesis del producto se falsa temprano y barato â€” y convertir cada dolor concreto sufrido con v0 en requisito priorizado de v1. **VÃ¡lvula de escape acordada â€” cambiar el modelo, no la herramienta:** si la velocidad de depuraciÃ³n llegara a ser inviable con modelos locales, v0 se conecta temporalmente a un modelo de frontera vÃ­a su adaptador OpenAI-compatible (RF-2.1/RF-2.3) hasta volver a ser viable el modelo local. El bootstrapping de herramienta queda intacto â€” el criterio "v1 se desarrolla usando v0" sigue cumpliÃ©ndose â€” bajo dos condiciones: toda excepciÃ³n se registra con motivo y duraciÃ³n, y las mÃ©tricas de RNF-10 se validan siempre contra el modelo local objetivo â€” optimizar compactaciÃ³n/retrieval medido contra un modelo de frontera puede sesgar el diseÃ±o hacia un perfil de contexto que no representa al Perfil A real.

---

## 6. Hoja de ruta de implementaciÃ³n (MVP v0 â†’ producto completo)

**Advertencia honesta antes de la tabla:** esto es mucho trabajo para una sola persona. La Ãºnica forma de que converja es tratar cada MVP como una versiÃ³n realmente usable (aunque incompleta) â€” no una lista de checkboxes que solo tiene sentido al final. Cada corte de abajo deberÃ­a poder usarse de verdad para trabajo real antes de pasar al siguiente. Y desde v1 ese trabajo real incluye, por el principio de bootstrapping (Â§0), el propio desarrollo del harness: cada versiÃ³n se construye usando la anterior.

### MVP v0 â€” NÃºcleo interactivo mÃ­nimo
**Tema:** un agente conversacional real, con herramientas reales, sobre un workspace real. Nada elegante todavÃ­a.

- **RF cubiertos:** RF-1.1 (agente + tools sobre workspace, sin subagentes aÃºn), RF-2.1 (un solo adaptador: OpenAI-compatible vÃ­a Ollama), RF-2.3 (cambiar modelo sin reiniciar), RF-3.1 (memoria persistente simple, SQLite sin vectorial), RF-6.1-6.3 (CLI completa), RF-10.1-10.2 (git bÃ¡sico + shell), RF-1.4 parcial (sesiÃ³n sobrevive a un cierre de cliente, sin el resto de background jobs complejos).
- **RNF cubiertos â€” y esto es lo que quiero remarcarte:** RNF-1.1/1.2/1.3 (mÃ©tricas base), RNF-3.1 (independencia de proveedor â€” decisiÃ³n de arquitectura que no se puede corregir despuÃ©s sin reescritura), RNF-4.1 (deny-por-defecto), RNF-4.3/4.4 (local-first, secrets fuera de logs), **RNF-4.5 (tratar contenido no confiable como dato, no como instrucciÃ³n) y RNF-4.7 (aislamiento de shell del propio core vÃ­a contenedor/seccomp)** â€” no son "seguridad para despuÃ©s": si el core ejecuta shell sin esto desde el dÃ­a uno, cada mes que pasa hace mÃ¡s caro meterlo retroactivamente. **RNF-4.8** (parada de emergencia) y **RNF-4.9** (allowlist de red por defecto) tambiÃ©n entran aquÃ­ â€” son baratos de construir ahora y muy caros de agregar despuÃ©s de que el hÃ¡bito de "todo pasa" ya estÃ© instalado. RNF-6.1 (logging estructurado), RNF-7.1/7.2 (cambios de direcciÃ³n a mitad de tarea, config versionable).
- **Diferido explÃ­citamente:** subagentes, retrieval, plugins, skills, GUI, SDD, branching de sesiones, ejecuciÃ³n autÃ³noma, RNF-8/RNF-9 (no aplican sin modo autÃ³nomo), adaptadores remotos.
- **Criterio de salida:** podÃ©s sostener una conversaciÃ³n con herramientas reales (leer/escribir archivos, correr comandos, commitear) contra un modelo local, sin que se degrade en sesiones largas, y sin que el core pueda hacer nada fuera de lo que vos declaraste permitido. Es la Ãºnica versiÃ³n que se construye sin sÃ­ misma: herramientas externas (OpenCode/Claude Code u otras) hacen de andamio â€” este hito es la semilla del bootstrapping (Â§0).

**Aclaraciones de implementaciÃ³n â€” v0 (decididas durante exploraciÃ³n agÃ©ntica del proyecto):**
1. **MCP:** las tools nativas (file read/write, shell, git) se implementan como funciones Go en proceso, no como servidores MCP separados â€” pero con la misma forma de interfaz (nombre, descripciÃ³n, JSON schema, dispatch) que un tool de MCP, para que v2 solo agregue transporte/cliente MCP sobre la abstracciÃ³n existente, sin reescribir el contrato.
2. **Aislamiento de shell (RNF-4.7), con matiz por plataforma:** `sandbox-exec` (macOS) es una API no documentada/en desuso â€” no es base aceptable para el requisito. En **Linux** (Perfil B), aislamiento completo vÃ­a seccomp/Landlock desde v0 (primitivas estables). En **macOS** (Perfil A), v0 se limita al modelo de permisos deny-by-default (RNF-4.1) sin sandbox de SO; el aislamiento duro en macOS queda para v1 con un mecanismo a evaluar entonces â€” defendible porque el uso interactivo en Perfil A siempre tiene un humano mirando.
3. **Modelo de referencia para tool-calling:** `qwen2.5-coder:7b` (Q4_K_M vÃ­a Ollama) como target principal para optimizar prompt layout y manejo de tool-calling; `llama3.1:8b-instruct` como referencia secundaria para el banco de pruebas de v1 (RNF-10).
4. **CLI vs. TUI:** v0 es solo CLI con REPL interactivo bÃ¡sico (`cobra`) y modo no interactivo. `bubbletea`/TUI con paneles se justifica reciÃ©n con retrieval (v1) o subagentes (v3) â€” antes no hay estado rico que visualizar.
5. **Persistencia de sesiÃ³n (RF-1.4 parcial):** el daemon sigue corriendo en background y el cliente se reconecta a la sesiÃ³n en curso (ya implÃ­cito en la arquitectura de Â§3.1). SQLite (RF-3.1) resuelve un problema complementario distinto â€” sobrevivir a que el propio daemon se reinicie â€” no el de "seguir mientras el cliente estÃ¡ desconectado".

### MVP v1 â€” RÃ¡pido de verdad (el diferencial del proyecto)
**Tema:** eficiencia de contexto y tokens con modelos locales â€” la razÃ³n de ser del proyecto.

- **RF cubiertos:** RF-3.2 (retrieval selectivo), RF-3.3 (compactaciÃ³n jerÃ¡rquica), RF-3.4 (memoria inspeccionable/editable), RF-3.5 (anclaje), RF-2.4/2.5 (ruteo por costo de paso).
- **RNF cubiertos:** RNF-2.1-2.5 completos (mediciÃ³n de tokens, prompt-caching, objetivo â‰¥40%, KV-cache local, techo de contexto), RNF-1.4 (validado, no solo asumido), RNF-1.5/1.6 (concurrencia realista, reserva de nÃºcleos), **RNF-10 completo (banco de pruebas)** â€” deliberadamente aquÃ­ y no al final: sin medir desde este punto, "eficiencia de contexto" es una afirmaciÃ³n de fe, no un requisito cumplido. RNF-6.3 (mÃ©tricas de costo, junto con RNF-2.1).
- **Nota de dependencia:** RNF-4.12 (aprobaciÃ³n humana para anclar contenido no confiable) empieza a aplicar aquÃ­ en su mitad de memoria (RF-3.5) â€” la otra mitad (skills, RF-4.3) llega en v2.
- **Criterio de salida:** el banco de pruebas de RNF-10 confirma el â‰¥40% de reducciÃ³n de tokens sobre el Perfil A real (Â§5) â€” no es un objetivo de diseÃ±o, es un nÃºmero medido. AdemÃ¡s: todo v1 se desarrolla usando v0 como herramienta principal de trabajo â€” primer ejercicio real de bootstrapping, con la herramienta mÃ¡s cruda del ciclo.

### MVP v2 â€” Se extiende sin tocar el core
**Tema:** extensibilidad real vÃ­a plugins y skills, y el primer adaptador remoto.

- **RF cubiertos:** RF-4.1-4.4 (skills con lazy-load y aprobaciÃ³n humana), RF-5.1-5.4 (plugins WASM), **RF-2.2 (adaptadores Anthropic/Gemini)** â€” entra aquÃ­, no en v0: el primer adaptador remoto es, en los hechos, el primer "plugin real" que ejercita el sistema de extensibilidad reciÃ©n construido, en vez de ser un caso especial cableado al core.
- **RNF cubiertos:** RNF-3.2 (extender vÃ­a plugin sin tocar el core â€” ahora se prueba de verdad), RNF-3.3 (tests de integraciÃ³n sobre la API interna), RNF-4.2 (mÃ­nimo privilegio en plugins), RNF-4.6 (procedencia/firma de plugins y skills externos), RNF-4.12 completo (incluye ahora la mitad de skills), RNF-6.2 (grabaciÃ³n/replay de sesiones â€” necesario para que la minerÃ­a de skills tenga de dÃ³nde sacar trayectorias).
- **Criterio de salida:** instalÃ¡s un plugin de terceros y una skill sin recompilar el binario, y ambos corren aislados con permisos mÃ­nimos declarados. AdemÃ¡s: v2 se desarrolla usando v1.

### MVP v3 â€” Trabaja en equipo consigo mismo
**Tema:** orquestaciÃ³n multiagente.

- **RF cubiertos:** RF-1.2/1.3 (subagentes con contexto acotado), RF-9.1-9.3 (branching, merge, multi-run).
- **RNF cubiertos:** re-validaciÃ³n de RNF-1.5 con subagentes reales (no solo teÃ³rica como en v1) â€” la cola/scheduler ahora tiene contenciÃ³n de verdad que probar.
- **Diferido:** RF-10.3 (integraciÃ³n GitHub) es explÃ­citamente "deseable, no v1" â€” entra acÃ¡ o despuÃ©s, sin bloquear nada.
- **Criterio de salida:** el orquestador delega una tarea real a un subagente con contexto acotado, y el resultado consolidado vuelve sin inflar el contexto del orquestador (medible con el banco de RNF-10). AdemÃ¡s: v3 se desarrolla usando v2 â€” todavÃ­a monoagente; los subagentes que introduce esta versiÃ³n se estrenan construyendo lo que viene despuÃ©s.

### MVP v4 â€” Superficies adicionales
**Tema:** GUI web, SDD formal, auto-aprendizaje activo, portabilidad validada.

- **RF cubiertos:** RF-7.1-7.4 (GUI web), RF-8.1-8.4 (SDD formal â€” descomposiciÃ³n de spec en tareas trackeables), auto-aprendizaje activo (minerÃ­a de patrones sobre las trayectorias grabadas desde v2).
- **RNF cubiertos:** RNF-5.1/5.2 (portabilidad â€” validaciÃ³n real en Linux/Windows, no solo "el lenguaje lo permite"), RNF-4.11 (transporte cifrado para acceso remoto a la GUI).
- **Nota de reutilizaciÃ³n:** el motor de descomposiciÃ³n de RF-8.2 (spec â†’ tareas) es el mismo que necesita RF-11.3 â€” construirlo bien acÃ¡ evita reconstruirlo en la fase siguiente.
- **Criterio de salida:** la GUI muestra el mismo estado que la CLI en tiempo real, y una SPEC real se descompone y trackea automÃ¡ticamente sin intervenciÃ³n manual. AdemÃ¡s: v4 se desarrolla usando v3 â€” el desarrollo mismo del SDD y la GUI sirve como banco de pruebas de la orquestaciÃ³n multiagente sobre tareas reales.

### v1.0 â€” Producto completo: ejecuciÃ³n autÃ³noma end-to-end
**Tema:** RF-11 completo â€” el modo de mayor riesgo, por eso va Ãºltimo.

- **RF cubiertos:** RF-11.1-11.10 completos, casos extraordinarios (ambigÃ¼edad Tier 3, Â§3.7).
- **RNF cubiertos:** RNF-8.1-8.4 completos (autonomÃ­a segura), RNF-9.1-9.4 completos (techo de sensibilidad de proyecto), RNF-4.8/4.9 ya existÃ­an desde v0 pero se validan bajo carga real, RNF-4.10 (log a prueba de manipulaciÃ³n â€” reciÃ©n tiene sentido con RNF-9 ya activo).
- **Rollout dentro de esta versiÃ³n, no como MVP aparte:** la progresiÃ³n `dry_run â†’ supervised â†’ checkpoint â†’ autonomous` (Â§7.2) se recorre sobre proyectos reales de sensibilidad `general` antes de considerar el modo maduro.
- **Criterio de salida:** un run manifest completo corre en modo `checkpoint` sobre un proyecto real de baja sensibilidad, sin intervenciÃ³n fuera de los checkpoints declarados, con reporte final coherente (RF-11.10) y sin que el piso de seguridad no configurable (RNF-8.2) haya tenido que intervenir. AdemÃ¡s: v1.0 se desarrolla usando v4, y una vez validado el flujo, parte de sus tareas restantes puede ejecutarlas el propio harness en modo `checkpoint` â€” el cierre natural del bootstrapping.

---

### Tabla de trazabilidad (todos los RF/RNF por versiÃ³n)

| VersiÃ³n | RF | RNF |
|---|---|---|
| v0 | 1.1, 1.4(parcial), 2.1, 2.3, 3.1, 6.1-6.3, 10.1-10.2 | 1.1-1.3, 3.1, 4.1, 4.3-4.5, 4.7-4.9, 6.1, 7.1-7.2 |
| v1 | 2.4-2.5, 3.2-3.5 | 1.4-1.6, 2.1-2.5, 6.3, 10.1-10.3 |
| v2 | 2.2, 4.1-4.4, 5.1-5.4 | 3.2-3.3, 4.2, 4.6, 4.12, 6.2 |
| v3 | 1.2-1.3, 9.1-9.3 | (re-validaciÃ³n de 1.5) |
| v4 | 7.1-7.4, 8.1-8.4, 10.3 (opcional) | 4.11, 5.1-5.2 |
| v1.0 | 11.1-11.10 | 8.1-8.4, 9.1-9.4, 4.10 |

---

## 7. Formato del Run Manifest (propuesta â€” RF-11)

### 7.1 Estructura del archivo

Un Ãºnico archivo Markdown con front-matter YAML â€” mismo patrÃ³n que `SKILL.md`, consistente con el resto del proyecto. Nombre sugerido: `RUN.md`, o `.forge/runs/<run_id>.md` si se versionan mÃºltiples corridas.

```yaml
---
run_id: implementar-modulo-facturacion
mode: autonomous              # supervised | checkpoint | autonomous | dry_run
spec_ref: ./SPEC.md            # opcional â€” se puede omitir si la SPEC va embebida abajo

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

  # Piso de seguridad NO configurable (RNF-8.2) â€” el sistema lo aplica
  # siempre, aparezca o no en este manifest. El manifest puede AÃ‘ADIR
  # checkpoints; nunca puede QUITAR estos:
  #   - operaciones git destructivas
  #   - acceso a credenciales fuera de lo declarado
  #   - red fuera de la allowlist
  #   - presupuesto excedido
---

# Objetivo (one-shot)

<instrucciÃ³n de alto nivel, en una sola frase o pÃ¡rrafo>

# EspecificaciÃ³n

<contenido de la SPEC embebido aquÃ­, si no se usÃ³ spec_ref>

# Criterios de aceptaciÃ³n global

- Toda la suite de tests pasa
- Lint sin errores/warnings
- Build exitoso
- (cualquier criterio adicional especÃ­fico del proyecto â€” evitar que
  "sin errores" sea el Ãºnico criterio; ver RNF-8.3)
```

### 7.2 Niveles de autonomÃ­a

| Nivel | Comportamiento | CuÃ¡ndo usarlo |
|---|---|---|
| `supervised` | Pausa despuÃ©s de **cada** tarea completada, sin excepciÃ³n | Primeras corridas, tareas de alto riesgo, o mientras no confÃ­as aÃºn en el harness |
| `checkpoint` | Pausa solo en los HITL declarados explÃ­citamente + piso de seguridad | Uso normal, una vez validado el flujo en `supervised` |
| `autonomous` | Igual que `checkpoint`, pero permite `merge_to_base: auto_if_all_hitl_passed` | Tareas acotadas y de bajo riesgo, con buena cobertura de tests existente |
| `dry_run` | Ejecuta todo el ciclo de decisiÃ³n (incluida la descomposiciÃ³n y los diffs propuestos) pero no escribe nada â€” solo reporta quÃ© harÃ­a | Validar el plan antes de comprometerse a ejecutar |

**RecomendaciÃ³n de uso:** ningÃºn proyecto deberÃ­a arrancar en `autonomous`. La progresiÃ³n natural es `dry_run` â†’ `supervised` â†’ `checkpoint` â†’ `autonomous`, ganando confianza en el comportamiento del harness sobre ese repo/tipo de tarea especÃ­fico antes de soltarle mÃ¡s autonomÃ­a.

---

## Registro de revisiones

- **0.8** â€” Se hace explÃ­cito el principio rector de bootstrapping (Â§0) y se incorpora como criterio de salida verificable en cada versiÃ³n de la hoja de ruta (Â§6): desde v1, cada MVP se desarrolla usando el anterior. Incluye el riesgo de velocidad asociado en Â§5, con vÃ¡lvula de escape por capacidad de modelo (frontera temporal vÃ­a RF-2.1/2.3) sin suspender el bootstrapping de herramienta.
- **0.7** â€” Borrador inicial para validaciÃ³n de arquitectura.

---

*Fin del documento. Este spec es un punto de partida para iteraciÃ³n â€” no una decisiÃ³n de arquitectura cerrada.*
