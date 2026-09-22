# Watch Party P2P — documentación

Aplicación de escritorio para ver un **enlace directo HTTP/HTTPS** en grupo.
Cada participante reproduce el vídeo con mpv en su propia ventana; el host
comparte la URL y controla reproducción, pausa y posición por WebRTC.

## Guías

| Documento | Contenido |
| --- | --- |
| [Compilación y desarrollo](build.md) | Requisitos, Linux, Windows, mpv, desarrollo y pruebas. |
| [Arquitectura y transporte](transport.md) | Componentes, STUN, señalización, WebRTC y migración de DTLS. |
| [Despliegue en Render](render.md) | Servicio `signaler`, Docker, variables y conexión del cliente. |
| [Empaquetado](packaging.md) | Iconos, metadatos y recursos de los instaladores Wails. |
| [Seguridad](security.md) | Correcciones, auditorías y límites del modelo de confianza. |
| [Plan original](archive/plan-watchparty-debrid.md) | Investigación histórica, no especificación actual. |

## Estructura del repositorio

```text
docs/                        Toda la documentación mantenida e histórica
signaler/
  Dockerfile                 Servidor Weron para Render
signaler/render.yaml         Blueprint del despliegue en Render
watchparty/                  Aplicación completa de escritorio
  app.go                     Métodos Wails y ciclo de vida
  internal/p2p/              Salas y autorización de mensajes
  internal/transport/        Cliente WebSocket + Pion WebRTC v4 / DTLS v3
  internal/sync/             Heartbeats y corrección de desfase
  internal/mpv/              Instalación/detección de mpv e IPC
  frontend/                 Interfaz JavaScript y Vite
  build/                    Recursos nativos de empaquetado Wails
```

`signaler/` se despliega como servicio independiente en Render. `watchparty/`
contiene la aplicación que se compila y ejecuta en cada equipo. Los comandos y
rutas de estas guías indican siempre desde qué directorio deben ejecutarse.

## Uso

1. [Compila el cliente](build.md) y ábrelo; espera a la comprobación de mpv.
2. Configura la misma URL `wss://tu-servicio.onrender.com/` en todos los clientes,
   en «Configuración del signaler». Sigue la [guía de Render](render.md) para
   obtener tu dominio real.
3. Crea una sala con contraseña de al menos 4 caracteres: el backend admite **4–256
   bytes UTF-8**. Comparte el código de seis caracteres y la contraseña.
4. Los invitados introducen ambos datos en «Unirse a sala».
5. El host pega el enlace directo HTTP/HTTPS y pulsa «Iniciar Transmisión».
6. Usa los controles de la aplicación para play/pause/seek; «Control» transfiere
   el rol a otro participante conectado. «Salir» cierra la sala local y mpv.

No hay resolución de torrents ni integración con APIs Debrid: el usuario obtiene
el enlace fuera de la aplicación. Cada equipo descarga directamente desde el
servidor del vídeo. La corrección de desfase depende de la red y del buffering;
no garantiza una precisión de ±2 segundos.

Los participantes reciben la URL completa, incluidos sus tokens. El host inicial
se reconoce por confianza en el primer anuncio; la contraseña compartida no
identifica individualmente al creador. Estos límites son independientes del
fallo de DTLS corregido y se explican en [seguridad](security.md).

## Actualizar desde el cliente anterior

Recompila y distribuye el cliente actualizado a **todos los participantes** y
crea salas nuevas. El nuevo adaptador elige quién genera la oferta mediante el
orden de los IDs; no se garantiza interoperabilidad con el adaptador antiguo.
El servicio Weron de Render conserva su protocolo de relay y no necesita
terminar ni descifrar las conexiones DTLS.

## Licencias

El repositorio no define una licencia propia. El cliente utiliza Pion WebRTC
bajo MIT y ya no importa la librería Go de Weron. El servidor Weron de `signaler/`
es un componente separado bajo AGPL-3.0. mpv y sus compilaciones tienen sus propias
licencias y componentes; consulta las licencias aplicables al distribuir.
