# Revisión de seguridad

[Índice de documentación](README.md)

Fecha: **21 de septiembre de 2026**.

Alcance: lectura del código Go/JavaScript y configuración de despliegue,
auditoría de dependencias npm/Go, pruebas de regresión de las fronteras de
autorización y validación, y comprobaciones de compilación. No es una auditoría
externa ni una certificación de ausencia de vulnerabilidades.

## Correcciones aplicadas

| Hallazgo | Cambio |
| --- | --- |
| Cualquier participante podía enviar `transfer` y alterar el control | Autorización en `internal/p2p/room.go`: solo el host conocido puede transferir a un participante conectado. La sala es la fuente de autoridad para la UI y el controlador de sincronización. |
| Un `hello` posterior podía sustituir al host y lanzar otra URL | Se fija el primer host reconocido durante la sesión. Otros participantes no pueden reclamar ese rol ni anunciar URLs; los cambios posteriores requieren transferencia autorizada. El límite de confianza inicial se detalla más abajo. |
| Play/pause/seek solo estaban restringidos en la UI | Comprobación de autoridad también en los métodos Go enlazados a JavaScript. Se rechazan posiciones negativas, no finitas o superiores a un año. |
| Contraseñas concatenadas a la query permitían alterar parámetros | Construcción mediante `net/url`, conservando caracteres especiales. Se exige `wss`, salvo `ws` en loopback. Contraseñas de 12–256 bytes UTF-8 validadas por el backend. |
| URL de streaming completa escrita en logs | Se elimina ese registro, se oculta el detalle del error inicial del adaptador que puede incluir credenciales y no se heredan stdout/stderr de mpv. |
| Socket Unix predecible en un directorio compartido | Directorio temporal aleatorio con permisos `0700` por reproductor, limpiado al terminar. En Windows se aleatoriza el nombre del pipe con 192 bits. |
| Instalación de ejecutable sin verificación de integridad ni límites | SHA-256 de GitHub, HTTPS también en redirecciones, límite temporal de 5 minutos, metadata de 2 MiB, descarga de 256 MiB y extracción de 512 MiB. Publicación por archivo temporal y renombrado; instalaciones concurrentes serializadas. |
| Selección ambigua de archivos y compilación de CPU | Se extrae únicamente un archivo cuyo nombre base sea `mpv.exe`, a un destino fijo; se usa x86_64 base, sin forzar instrucciones v3. |
| Entradas remotas y consumo de recursos | URLs absolutas HTTP/HTTPS sin userinfo; posiciones acotadas; mensajes de hasta 32 KiB, 64 mensajes por segundo por conexión y máximo de 32 peers remotos registrados. Escrituras serializadas por conexión y cierre tras 5 segundos de bloqueo. Cierre de lectores al cancelar. |
| Superficie de ejecución de mpv | Se preserva `--` antes de la URL y se desactivan configuración, scripts automáticos y el hook `ytdl`. El proceso se espera y sus eventos se cierran al finalizar. |
| Imagen de signaler mutable y ejecución privilegiada | Imagen Weron v0.3.0 fijada por digest de GHCR, UID/GID sin privilegios y `exec` para propagar señales. Esto es endurecimiento, no una auditoría del contenido de la imagen. |
| Nonces AES-GCM de DTLS vulnerables (CVE-2026-26014) | Sustituido el adaptador cliente de Weron por `internal/transport`, Pion WebRTC v4.2.20 y DTLS v3.1.9. Eliminadas las ramas WebRTC v3 y DTLS v2 del grafo de dependencias del cliente. |
| Healthcheck que impedía un despliegue correcto en Render | Retirado el GET `/` como healthcheck: el contenedor real devuelve 501 sin API de gestión. Render debe dejar vacío el healthcheck; la verificación se realiza mediante conexión WebSocket e intercambio real entre clientes. |

Además, se corrigió la detección de mpv en Linux (antes intentaba utilizar
`mpv.exe`) y se sincroniza el rol de la UI al recibir una transferencia.

## Dependencias

### npm

`npm audit` encontró y se corrigieron mediante actualización del lockfile:

- `nanoid`: [GHSA-2v37-7h3g-55p8](https://github.com/advisories/GHSA-2v37-7h3g-55p8).
- `postcss`: [GHSA-fxqj-rqcc-2cmp](https://github.com/advisories/GHSA-fxqj-rqcc-2cmp).

Resultado posterior: **0 vulnerabilidades reportadas**. Wails utiliza `npm ci`
para instalar a partir del lockfile. Son dependencias de la cadena de build,
no evidencia por sí mismas de explotación de la aplicación distribuida.

### Go

Versiones relevantes del cliente:

- `github.com/pion/webrtc/v4`: **v4.2.20**.
- `github.com/pion/dtls/v3`: **v3.1.9**.
- `github.com/pion/interceptor`: **v0.1.48**.
- `golang.org/x/crypto`: **v0.56.0**; `golang.org/x/net`: **v0.57.0**.
- Versión mínima declarada: **Go 1.26.0**. Compilar con una versión soportada y
  parcheada, no necesariamente con la versión mínima.

**DTLS corregido:** [GO-2026-4479 / CVE-2026-26014](https://pkg.go.dev/vuln/GO-2026-4479)
se encontraba en DTLS v2.2.12, incorporado por el adaptador de Weron. La aplicación
ahora usa un adaptador propio basado en WebRTC v4 y DTLS v3.1.9, que incorpora la
corrección del nonce de AES-GCM con número de secuencia. No quedan dependencias
del cliente en Weron, WebRTC v3 ni DTLS v2; no hay exclusiones ni reemplazos para
silenciar el aviso. [Detalles de la migración](transport.md).

La versión anterior de este informe dejó ese fallo pendiente. La migración y
las pruebas de transporte descritas aquí cierran ese pendiente. Actualiza todos
los clientes: el envío desde un cliente antiguo seguirá usando su DTLS vulnerable.

**Hallazgo a nivel de módulo sin uso detectado:**

- [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932), paquete
  `golang.org/x/crypto/openpgp` obsoleto e inseguro. Está en el módulo requerido,
  pero no se importa ni se detectaron llamadas desde esta aplicación. No tiene
  versión corregida según la base consultada.

Resultado de `govulncheck` tras migrar: **0 vulnerabilidades alcanzables**, ninguna
en paquetes importados y **1 aviso solo a nivel de módulo** (OpenPGP sin uso).
El comando termina correctamente con código 0; no se han añadido exclusiones.

## Límites del modelo de confianza

1. **Host inicial por confianza en el primer anuncio (TOFU).** Fijar su ID evita
   sustituciones posteriores, pero un participante con la contraseña puede
   anunciarse antes que el creador al conectarse un invitado. No hay una clave
   pública del creador incluida en la invitación ni identidad individual firmada.
   La solución completa requiere un protocolo de invitación autenticado. Si el
   host se desconecta sin transferir, crea otra sala; no se elige automáticamente
   un nuevo host a partir de un anuncio no autenticado.
2. **La contraseña es conocida por el signaler.** Weron la recibe en la query y
   el cliente la reutiliza como clave de la señalización. Un operador del servidor
   o proxy TLS puede verla. No debe describirse como cifrado extremo a extremo
   frente a ese operador. Los participantes P2P también pueden conocer sus IPs.
3. **La URL es compartida con todos los invitados**, incluidos tokens o firmas
   en su query. Los logs propios ya no imprimen la URL, pero el argumento del
   proceso puede ser visible para usuarios con acceso suficiente al equipo.
4. **mpv no está aislado en una sandbox.** HTTP/HTTPS incluye servidores locales,
   redirecciones y playlists procesadas por mpv/FFmpeg; la validación de esquema
   no es una protección contra acceso a recursos de red internos. Este cliente
   se debe usar con un host y participantes de confianza y un mpv actualizado.
5. **El digest de mpv procede del mismo proveedor de descarga.** Detecta archivos
   distintos de los publicados, no una cuenta upstream comprometida. Un binario
   ya instalado se reutiliza sin verificar su digest de nuevo. En Windows el
   nombre aleatorio del pipe no constituye una ACL ni aísla procesos del mismo
   usuario; los permisos del pipe los crea mpv.
6. **El contenedor del signaler se probó localmente con Podman**, incluido un
   intercambio entre dos clientes WebRTC. No se escaneó su sistema base ni se
   desplegó en Render durante esta revisión. La auditoría Go corresponde al
   cliente, no al conjunto de componentes empaquetados en la imagen Weron.

## Verificación realizada

Entorno de revisión: Linux amd64, Go 1.27.1, Node.js 22.23.2, Wails 2.13.0.

- `go test -race ./...`: correcto; regresiones de autorización, transferencia,
  codificación de credenciales, URLs, límites de copia y privacidad del IPC;
  intercambio WebRTC real en loopback, tres peers, reconexión y cancelación.
- `TestWeronSignaler`: intercambio bidireccional con el contenedor real fijado
  en `signaler/Dockerfile`, ejecutado localmente con Podman.
- `govulncheck` para **Linux amd64 y Windows amd64**: código 0, sin vulnerabilidades
  alcanzables; DTLS v3.1.9 en el grafo de dependencias. El aviso OpenPGP sin uso
  se conserva en el informe.
- `go vet ./...` y `go mod verify`: correctos.
- `npm ci`, `npm audit` y compilación Vite: correctos.
- `wails build -platform windows/amd64`: correcto; genera
  `watchparty/build/bin/watchparty.exe`. No se ejecutó el binario en Windows.
- `wails build -tags webkit2_41`: bindings y frontend correctos; compilación
  nativa bloqueada por falta de GTK, GIO, WebKitGTK y libsoup de desarrollo en
  este entorno. Paquetes e instrucciones en [compilación](build.md).
- No se verificó una sesión real entre dos equipos ni la descarga/ejecución de
  mpv en Windows. Los tests unitarios no sustituyen esa prueba de integración.

Para repetir las auditorías, consulta
[Desarrollo y comprobaciones](build.md#desarrollo-y-comprobaciones). Los
resultados dependen de la fecha y de las bases de avisos consultadas.
