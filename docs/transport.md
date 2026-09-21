# Arquitectura, red y corrección de DTLS

[Índice de documentación](README.md)

## Componentes y datos

```text
Cliente Wails                         Cliente Wails
  sala + sincronización  <--- WebRTC ---> sala + sincronización
          |                    DTLS              |
         mpv                                    mpv
          |                                      |
          +---------- servidor del vídeo --------+

Los clientes consultan STUN y usan el signaler Weron de Render
para descubrirse e intercambiar SDP antes de establecer WebRTC.
```

- `watchparty/app.go` enlaza la interfaz y gestiona la sala y el proceso mpv.
- `internal/p2p` autoriza `hello`, `play`, `pause`, `seek`, `sync` y `transfer`.
- `internal/sync` envía posición cada 2 segundos y corrige el desfase con seeks
  o cambios de velocidad; sigue al host reconocido por la sala.
- `internal/transport` implementa descubrimiento y canales de datos con Pion.
- `internal/mpv` controla el reproductor mediante socket Unix o named pipe.

La URL del vídeo y los comandos van por el canal de datos; el vídeo se descarga
directamente por cada mpv desde el origen. El signaler no retransmite el vídeo
ni termina las conexiones DTLS entre participantes.

## STUN y TURN

Se configuran estos servidores públicos en `internal/p2p/room.go`:

```text
stun:stun.l.google.com:19302
stun:stun1.l.google.com:19302
```

STUN descubre la dirección pública y el puerto asignados por el NAT del router.
Google observa los datos de esa consulta y la dirección de origen, no recibe la
contraseña de sala, URL de vídeo ni controles por esas consultas STUN.

**No hay TURN configurado.** Algunas combinaciones de NAT o cortafuegos no
permiten conexión directa. Disponer de un signaler accesible no garantiza que
WebRTC logre atravesar esas redes.

## Migración que corrige CVE-2026-26014

Antes:

```text
Room → github.com/pojntfx/weron/pkg/wrtcconn
     → github.com/pion/webrtc/v3 → github.com/pion/dtls/v2@v2.2.12
```

Ahora:

```text
Room → watchparty/internal/transport
     → github.com/pion/webrtc/v4@v4.2.20
     → github.com/pion/dtls/v3@v3.1.9
```

La dependencia cliente Weron y las ramas antiguas de WebRTC/DTLS se eliminaron
de `go.mod` y `go.sum` mediante `go mod tidy`. No hay un `replace` que oculte la
versión vulnerable ni un parche criptográfico local.

Pion DTLS v3.1.9 incorpora la corrección de
[GO-2026-4479](https://pkg.go.dev/vuln/GO-2026-4479): el nonce explícito de los
registros AES-GCM se construye con el número de secuencia, evitando la colisión
que permitía la generación aleatoria de la implementación anterior. Las
primeras versiones corregidas fueron v3.0.11 y v3.1.1.

Todos los equipos deben usar el cliente actualizado: actualizar solo un extremo
no corrige el generador de nonces del otro. Crea salas nuevas tras actualizar.

## Contrato de señalización

El adaptador mantiene el formato de mensajes del ecosistema Weron v0.3.0:

- WebSocket en `/`, query `community` y `password` codificada con `net/url`.
- JSON con `type`, `from`, `to` y `payload`; `payload` es `[]byte` codificado
  en base64 por JSON y contiene la descripción SDP.
- Tipos utilizados: `introduction`, `offer` y `answer`.
- Cifrado de señalización compatible: SHA-224 de la contraseña, completado con
  cuatro bytes cero para formar una clave AES-256; AES-GCM con nonce de 12 bytes
  antepuesto al texto cifrado. La implementación usa la API estándar de Go
  `cipher.NewGCMWithRandomNonce` para el encapsulado.

Este cifrado de señalización es independiente de los registros DTLS. Se conserva
por compatibilidad, no como recomendación de derivación de contraseñas. La
contraseña es conocida por el operador del signaler; consulta [seguridad](security.md).
La especificación de referencia está en
[Weron v0.3.0](https://github.com/pojntfx/weron/tree/v0.3.0/internal).

El peer con ID lexicográficamente menor genera la oferta. Una introducción
dirigida permite descubrir al peer existente cuando el nuevo participante es
quien debe ofrecer. Esto evita ofertas simultáneas. Las descripciones se envían
después de reunir los candidatos ICE, incluidos en el SDP; no se envían ni se
procesan candidatos por mensajes `candidate` separados.

El canal `watchparty/sync/v1` es fiable y ordenado. Los mensajes de sala continúan
siendo JSON delimitado por salto de línea. La autorización del host pertenece a
la capa de sala, no al algoritmo que elige quién genera la oferta SDP.

## Recursos, cancelación y reconexión

- Un único escritor WebSocket con cola acotada de 64 mensajes.
- Frames entrantes limitados a 256 KiB y colas de negociación de 32 mensajes.
- Hasta 32 peers en negociación/conectados por sesión.
- Timeout inicial/de escritura de 10 segundos, negociación de 30 segundos.
- Pings cada 5 segundos y timeout de recepción de pong de 20 segundos.
- Al perder el signaler se cierran las conexiones de esa sesión y se reintenta
  cada 2 segundos. El ID local se conserva durante la vida de la sala.
- Cancelar la sala cierra los sockets, cancela las negociaciones y detiene los
  workers. No se registran la query del signaler ni las descripciones SDP.

## Pruebas

`internal/transport/adapter_test.go` verifica:

- Intercambio bidireccional sobre DTLS/SCTP real en loopback, con ambos órdenes
  de llegada de los IDs.
- Malla de tres clientes y reconexión tras interrumpir el relay WebSocket.
- Formato de señalización compatible, rechazo de frames truncados, alterados
  o cifrados con otra contraseña.
- Cancelación sin bloqueo y errores iniciales sin filtración de credenciales.
- Integración opcional con un contenedor Weron real mediante
  `WATCHPARTY_TEST_SIGNALER_URL`; véase [Render](render.md).

Los tests locales no dependen de servidores STUN públicos y no validan por sí
solos conectividad entre redes NAT distintas. Las auditorías y limitaciones
restantes están registradas en [seguridad](security.md).
