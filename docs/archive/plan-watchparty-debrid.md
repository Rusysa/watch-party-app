# Plan: App de Watch Party Sincronizado vía Debrid

> Documento histórico de diseño e investigación. Para el estado implementado,
> instrucciones de compilación Linux/Windows y limitaciones actuales, consulta
> [índice de documentación](../README.md) y [seguridad](../security.md). Las propuestas y
> afirmaciones sobre proveedores de este plan no constituyen funciones implementadas
> ni verificaciones actuales de sus condiciones.

## 1. Idea central

Una app que permite a varios usuarios ver el mismo contenido multimedia en sincronía,
usando un servicio de debrid (ej. TorBox) que soporta streaming del mismo link desde
múltiples IPs simultáneamente. Un usuario actúa como **host**: genera el link directo
desde su cuenta de debrid y controla la reproducción (play/pause/seek). Los demás
usuarios (**peers**) reciben ese link y ven el mismo contenido en su propio reproductor
local, sincronizado con el host.

No hay web app de por medio: la app es una **capa externa ligera** que controla un
reproductor multiplataforma (mpv) vía IPC, más una capa de red para sincronización.

---

## 2. Decisiones ya tomadas

- **Reproductor:** mpv, controlado por **IPC** (socket en Linux/Mac, named pipe en
  Windows) usando `--input-ipc-server`. Se descartó libmpv embebido por complejidad de
  build multiplataforma — se puede revisar más adelante si se quiere UI más integrada.
- **Distribución del contenido:** un solo link de debrid (del host) compartido con
  todos los peers, aprovechando que TorBox permite múltiples IPs sobre el mismo stream.
  No cada usuario resuelve su propio link (se descartó ese enfoque inicial por
  innecesario).
- **Sincronización:** modelo peer-to-peer vía WebRTC Data Channels usando **weron**
  directamente (librería cliente Go + servidor de señalización), auto-hosteado por el
  proyecto en vez de usar el servidor público. El servidor de señalización solo
  participa en el handshake inicial (intercambio de ICE candidates) y en mantener
  comunidades persistentes; una vez conectados, los eventos de control
  (play/pause/seek/posición) viajan directo entre peers por el Data Channel.
  **Licencia:** weron es AGPL-3.0 — decisión tomada de que el proyecto será open
  source, por lo que no representa un obstáculo (ver 5.3).
- **Stack sugerido:** Go + Wails para la capa externa (UI ligera + lógica de red +
  adaptador IPC a mpv), reusando directamente la librería `wrtcconn`/`wrtcchat` de
  weron para la parte P2P. Alternativas evaluadas: Rust+Tauri, Python (descartado por
  fricción de empaquetado multiplataforma).

---

## 3. Referencias e investigación previa

### Prior art
- **Syncplay** (open source, Python) — sincroniza mpv/VLC/MPC-HC entre usuarios que ya
  tienen el archivo/stream cargado localmente. Usa servidor central de señalización
  (solo texto/timestamps, nunca el media). Corrección de drift vía reportes periódicos
  de posición + micro-pausas o seeks. Referencia principal para el algoritmo de
  sincronización.
- **Multiplex** (github.com/pojntfx/multiplex, antes vintangle) — "watch torrents with
  your friends", usa mpv + IPC para el reproductor y `weron` (WebRTC P2P sin servidor
  central de datos) para sincronizar play/pause/seek entre peers. Tiene modo
  "Remoting" que usa un gateway HTTP-a-BitTorrent (hTorrent) como proxy de confianza,
  conceptualmente similar a lo que un debrid resuelve para nosotros.
  **Limitación detectada:** su UI está en GTK4/libadwaita → Linux-only en la práctica
  (Flathub). La lógica de sync (`weron`, Go puro) sí es portable; la UI no.
- **Torrent Party** — mismo concepto, discontinuado. Señal de nicho válido pero sin
  ganador consolidado.
- **weron** (github.com/pojntfx/weron) — librería Go de overlay networks sobre WebRTC.
  Servidor de señalización ligero + comunicación P2P real vía Data Channels después
  del handshake. **Decisión final: se usa directamente como dependencia**, auto-
  hosteando el servidor de señalización propio (ver 5.3). Licencia AGPL-3.0, aceptada
  dado que el proyecto será open source.

### Términos de servicio de TorBox (verificado)
- TorBox declara explícitamente que no rastrea IP, permite compartir cuenta/API key
  y no limita cuántos dispositivos pueden estar conectados a la vez.
  Fuente: support.torbox.app — "Can I Use My Account With Many Different IP's?"
- **Riesgo a vigilar:** tienen una política de "fair share" sin límites numéricos
  publicados. Uso muy alto puede activar revisión manual y baneo. Mitigación sugerida:
  diseñar para que cada "room" use la cuenta de debrid de su propio host (no una
  cuenta compartida a nivel de toda la base de usuarios de la app), para no
  concentrar tráfico anómalo en una sola cuenta.
- Contraste con Real-Debrid: en RD, streaming desde dos IPs distintas es motivo de
  ban explícito. Esto refuerza que TorBox (u otro debrid con política similar) es
  el proveedor correcto para este caso de uso, no cualquiera.

---

## 4. Arquitectura propuesta

```
┌────────────────────────────┐          ┌────────────────────────────┐
│   Host                     │          │   Peer                     │
│  ┌───────────────────────┐ │          │  ┌───────────────────────┐ │
│  │ App (Go + Wails)       │ │   P2P    │  │ App (Go + Wails)       │ │
│  │ - UI sala / controles  │ │ (WebRTC  │  │ - UI sala              │ │
│  │ - Cliente sync (weron)◄┼─┼─────────►│  │ - Cliente sync (weron) │ │
│  │   (wrtcconn/wrtcchat)  │ │  Data    │  │   (wrtcconn/wrtcchat)  │ │
│  │ - Adaptador IPC        │ │ Channel) │  │ - Adaptador IPC        │ │
│  └───────────┬────────────┘ │          │  └───────────┬────────────┘ │
│              ▼               │          │              ▼               │
│        ┌─────────┐          │          │        ┌─────────┐          │
│        │   mpv   │          │          │        │   mpv   │          │
│        └─────────┘          │          │        └─────────┘          │
└──────────────┬───────────────┘          └────────────────────────────┘
               │ genera link                              ▲
               ▼                                           │ recibe link (vía sala)
┌────────────────────────────┐                             │
│  API de TorBox (debrid)     │─────────────────────────────┘
└────────────────────────────┘

     (weron signaler auto-hosteado, con comunidades persistentes — solo
      participa en el handshake inicial de la sala, no en el streaming
      ni en el control post-conexión; ver 5.3)
```

### Flujo completo
1. Host resuelve el contenido → llama a la API de TorBox → obtiene link directo.
2. Host crea una "room" (comunidad persistente en weron) → se conecta al signaler
   auto-hosteado → obtiene un código de sala para compartir.
3. Peers ingresan el código → señalización los conecta directamente al host (o entre
   sí) vía WebRTC Data Channel.
4. Servidor de señalización distribuye el link de TorBox a los peers dentro de la sala.
5. Cada cliente lanza su propio mpv apuntando a ese link (`mpv <url> --input-ipc-server=...`).
6. Host controla reproducción → eventos van por el Data Channel directo a cada peer →
   cada app aplica el comando a su mpv local vía IPC.
7. Corrección de drift: cada peer reporta periódicamente su posición; si se desvía
   del umbral respecto al host, se autoajusta (seek si va atrasado, reducción
   temporal de velocidad si va adelantado — ver 5.5 para el detalle del algoritmo).

---

## 5. Decisiones sobre las preguntas abiertas

### 5.1 Control: centralizado con opción a transferible
Modelo por defecto: un host fijo tiene el control (play/pause/seek). Se añade la
opción de **transferir el control** a otro peer durante la sesión (similar al
"operador de sala" de Syncplay), en vez de control totalmente distribuido. Evita el
caos de comandos simultáneos de múltiples usuarios, pero da flexibilidad si el host
original necesita ceder el control (se ausenta, tiene mala conexión, etc.).

### 5.2 Manejo de desconexión de un peer
- Al perder la conexión P2P, el peer entra en estado de **espera/reconexión
  automática** por un tiempo determinado (a definir, ej. 30-60s) intentando
  restablecer el Data Channel.
- Si se agota el tiempo sin reconectar, el peer **abandona la room** — el resto de
  los usuarios puede continuar viendo sin interrupción.
- El peer que abandonó puede **volver a entrar a la misma room más tarde** (requiere
  que la room sea persistente, no efímera — ver 5.3) y resincronizarse con la
  posición actual del host al reconectar.

### 5.3 Signaling server: weron auto-hosteado (AGPL aceptado)
Se decide usar **weron completo** (servidor de señalización + librería cliente Go),
pero **auto-hosteado** en vez de depender del servidor público
(`wss://weron.up.railway.app/`). Esto resuelve los problemas de infraestructura
identificados inicialmente:

- **SLA/disponibilidad:** resuelto — control total sobre el uptime del servidor.
- **Latencia:** resuelta — se puede hostear cerca de la base de usuarios.
- **Comunidades efímeras:** resuelto — se gestionan comunidades persistentes desde
  el inicio vía `weron manager`, necesario para el requisito de reconexión tardía
  (ver 5.2).

**Sobre la licencia AGPL-3.0:** se investigó y confirmó que la obligación de AGPL no
depende de quién hostea el servidor sino de qué código se distribuye. Al usar la
librería cliente de weron (`wrtcconn`) embebida en la app y distribuirla a los
usuarios, el proyecto completo queda bajo AGPL-3.0 — **decisión tomada: esto es
aceptable, el proyecto será open source.** No es necesario evitar la librería
cliente ni reescribir un cliente propio sobre `pion/webrtc`; se puede reusar
directamente el código de ejemplo de weron (`wrtcconn`/`wrtcchat`) como base de la
capa de sincronización.

**Bus factor (sigue siendo un riesgo a vigilar):** proyecto pequeño, pocos
colaboradores. Auto-hostear mitiga el riesgo de que el servidor público desaparezca,
pero no mitiga el riesgo de que el proyecto deje de recibir actualizaciones o
parches de seguridad a largo plazo. Vale la pena revisar periódicamente el estado
del repo.

**Nota:** weron sigue sin resolver el algoritmo de sincronización de reproducción en
sí (solo da el transporte P2P) — eso se construye por separado, ver 5.5.

### 5.4 Debrid: solo TorBox (o proveedores con política similar)
No se planea soportar múltiples debrids en la primera versión. Queda abierta la
puerta a soportar otros proveedores en el futuro, pero **únicamente si tienen una
política explícita equivalente a la de TorBox** (sin límite de IPs/dispositivos
simultáneos sobre la cuenta). **Real-Debrid queda descartado**, ya que su política
penaliza el streaming desde múltiples IPs con riesgo de baneo — incompatible con el
modelo de este proyecto.

### 5.5 Tolerancia de drift: basada en el algoritmo de Syncplay
Investigado en detalle (documentación oficial del cliente de Syncplay). Estrategia a
adoptar:

- **Si un peer va atrasado respecto al host:** hacer *seek* hacia adelante para
  alcanzar la posición correcta (equivalente a "fast-forward if lagging behind" de
  Syncplay).
- **Si un peer va adelantado respecto al host:** en vez de un seek brusco hacia atrás
  (visualmente molesto), **reducir temporalmente la velocidad de reproducción**
  (mpv soporta esto vía `set_property speed`) hasta que el resto lo alcance, y luego
  volver a velocidad normal (equivalente a "slow down on desync"; Syncplay reporta
  que esto funciona muy bien específicamente en mpv).
- **Umbral de tolerancia:** no corregir ante desviaciones mínimas o momentáneas —
  usar un umbral razonable (punto de partida: ~1-2s) y opcionalmente un promedio
  móvil de la desviación antes de disparar una corrección, para evitar el problema
  documentado por la propia comunidad de Syncplay de corrección "demasiado agresiva"
  con conexiones inestables (rebobinados constantes y molestos a otros usuarios).
- **No corregir durante interacción manual:** mientras un usuario está manualmente
  navegando su propio reproductor (ej. adelantando para saltarse una escena en su
  copia local), no forzar corrección hasta que vuelva a reproducción normal.
- Reportar posición periódicamente entre peers (ej. cada 1-2s) para alimentar este
  cálculo.

### 5.6 Reproductor: ventana separada
Se confirma el enfoque simple: mpv como proceso externo, ventana separada,
controlado vía IPC (socket/named pipe). Se descarta libmpv embebido para esta etapa
por complejidad de build multiplataforma — queda como posible mejora futura si se
quiere integrar el reproductor dentro de la misma ventana de la app.

---

## 6. Próximos pasos sugeridos

1. **Prototipo mínimo (sin sync todavía):** script en Go que lance mpv, se conecte
   por IPC, y exponga comandos básicos (play, pause, seek) desde la terminal.
2. **Levantar weron signaler auto-hosteado:** desplegar el servidor de señalización
   propio (`weron signaler`), configurado con comunidades persistentes desde el
   inicio (necesario para 5.2).
3. **Prototipo de señalización:** dos instancias locales conectándose vía el signaler
   propio (usando `wrtcconn`/`wrtcchat` de weron) e intercambiando un mensaje simple.
4. **Unir ambos:** eventos recibidos por el Data Channel disparando comandos IPC
   sobre mpv.
5. **Integrar TorBox:** llamada a su API para resolver un link real desde un
   magnet/ID.
6. **UI mínima con Wails:** pantalla de crear/unirse a sala + botones de control +
   opción de transferir el control (5.1).
7. **Implementar el algoritmo de corrección de drift** descrito en 5.5 (seek si
   atrasado, slowdown si adelantado, umbral con promedio móvil) una vez el flujo
   básico funcione end-to-end.
8. **Manejo de reconexión de peers** (5.2): timeout de espera, salida de la room,
   reingreso y resincronización con la posición actual del host.

---

## 7. Notas legales / de producto

- El patrón técnico en sí (sincronizar reproducción, cada cliente resolviendo su
  propia fuente local) es neutral y tiene usos legítimos.
- El uso concreto (contenido con o sin licencia) depende de las leyes del país de
  cada usuario y de los términos del debrid usado — no es un tema resuelto por el
  diseño técnico.
- Revisar periódicamente los TOS de TorBox (o el debrid que se use), ya que su
  política de "fair share" no está cuantificada y podría endurecerse.
- El proyecto usa weron (AGPL-3.0) como dependencia directa → el código fuente
  completo de la app debe permanecer disponible públicamente bajo los términos de
  la AGPL. Esto es una decisión ya tomada y aceptada (ver 5.3), no un pendiente.
