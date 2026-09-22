# Desplegar el signaler en Render

[Índice de documentación](README.md)

El directorio [`signaler/`](../signaler/) contiene únicamente el despliegue del
**servidor Weron**. La aplicación de escritorio está en
[`watchparty/`](../watchparty/). El archivo [`signaler/render.yaml`](../signaler/render.yaml)
es el Blueprint que Render utiliza para construir el servicio Docker.

El signaler permite descubrir participantes e intercambiar señalización WebRTC;
el vídeo se descarga desde el origen por cada participante y los mensajes de
reproducción circulan por los canales de datos P2P.

## Despliegue en Render

1. Abre [Render](https://render.com/) y crea un **Blueprint**.
2. Selecciona el repositorio `Rusysa/watch-party-app`.
3. Indica **`signaler/render.yaml`** como ruta del Blueprint. Render evalúa las
   rutas de Docker desde la raíz del repositorio; por eso el Blueprint usa
   explícitamente `dockerfilePath: ./signaler/Dockerfile`.
4. Pulsa **Apply**. Render asignará el puerto mediante `PORT`; Weron escucha en
   todas las interfaces con ese puerto.
5. Genera o consulta el dominio público del servicio. Si Render muestra
   `https://watchparty-signaler.onrender.com`, configura
   **`wss://watchparty-signaler.onrender.com/`** en todos los clientes.
6. Configura la URL antes de crear o unirte a una sala. El endpoint WebSocket es
   `/`.
7. Deja vacío **Health Check Path**. Weron sin API de gestión devuelve HTTP 501
   para un GET normal a `/`; eso no significa que el WebSocket esté caído.
   Render debe comprobar la disponibilidad mediante el proceso/puerto asignado,
   no mediante una ruta HTTP 200 que Weron no expone por defecto.

El plan del Blueprint es `free`; sus límites, suspensión por inactividad y
disponibilidad dependen de Render. El almacenamiento y broker de Weron son en
memoria porque no se definen `DATABASE_URL` ni `REDIS_URL`; las comunidades son
efímeras y no hay volumen persistente.

## Variables

| Variable | Dónde | Valor / función |
| --- | --- | --- |
| `PORT` | Render | Asignada por Render; el contenedor usa 15325 como valor local predeterminado. |
| `DATABASE_URL` | Render | No definida; Weron usa almacenamiento en memoria. |
| `REDIS_URL` | Render | No definida; Weron usa broker en memoria. |
| `WATCHPARTY_SIGNALER_URL` | Equipo cliente | URL inicial opcional; no se envía al contenedor. |

El cliente usa `wss://watchparty-signaler.onrender.com/` si no se establece
`WATCHPARTY_SIGNALER_URL`. Puede cambiarse desde la interfaz para nuevas salas;
esa selección no persiste entre ejecuciones. Para fijarla al iniciar:

```bash
WATCHPARTY_SIGNALER_URL=wss://watchparty-signaler.onrender.com/ ./watchparty/build/bin/watchparty
```

```powershell
$env:WATCHPARTY_SIGNALER_URL = "wss://watchparty-signaler.onrender.com/"
.\watchparty\build\bin\watchparty.exe
```

Sustituye el dominio de ejemplo por el dominio real asignado por Render.

## Contenedor local

Desde la raíz, con Docker o Podman:

```bash
docker build -t watchparty-signaler ./signaler
docker run --rm --name watchparty-signaler -p 127.0.0.1:15325:15325 watchparty-signaler
```

En el cliente configura `ws://127.0.0.1:15325/`. La excepción sin TLS solo se
permite para loopback; para equipos externos utiliza el dominio HTTPS de Render
con `wss://`.

El Dockerfile fija Weron **v0.3.0 por digest multiplataforma** y utiliza un
usuario sin privilegios. `exec` permite que Weron reciba SIGTERM al detener el
contenedor.

## Verificar el servidor local

Con el contenedor escuchando en el puerto 15325, desde `watchparty/`:

```bash
WATCHPARTY_TEST_SIGNALER_URL=ws://127.0.0.1:15325/ go test -race -timeout 45s ./internal/transport -run TestWeronSignaler -v
```

```powershell
$env:WATCHPARTY_TEST_SIGNALER_URL = "ws://127.0.0.1:15325/"
go test -timeout 45s ./internal/transport -run TestWeronSignaler -v
Remove-Item Env:WATCHPARTY_TEST_SIGNALER_URL
```

La prueba crea una comunidad temporal con nombre aleatorio y conecta dos clientes
Pion reales, intercambiando mensajes en ambas direcciones. Sus candidatos ICE
son de loopback; úsala con un signaler local. Para comprobar Render y NAT reales,
abre los clientes en dos equipos y entra en una sala.

## Relación con la corrección de DTLS

El cliente actualizado usa Pion WebRTC v4 y DTLS v3. El signaler solo retransmite
los mensajes cifrados de descubrimiento y SDP; no participa en el handshake DTLS
entre los equipos. Por tanto, el protocolo de Render no necesita migrar para
esta corrección. Actualiza todos los clientes y crea salas nuevas; consulta
[arquitectura y transporte](transport.md).

## Seguridad y mantenimiento

El operador de Render recibe el código y la contraseña de la comunidad en la
query de la conexión WebSocket. TLS protege el tránsito, **no oculta esos datos
al servidor o al proxy TLS**. Evita registrar query strings y no reutilices
contraseñas de otros servicios. La misma contraseña se pasa a Weron como clave
de cifrado de señalización; no protege frente a un operador que ya la conoce.

Actualizar la imagen requiere revisar una nueva versión/digest y analizar sus
dependencias. Fijar el digest hace reproducible la base, pero no certifica que
la imagen carezca de vulnerabilidades. El contenedor se construyó y ejecutó
localmente con Podman y se verificó el intercambio WebRTC contra Weron. Consulta
[seguridad](security.md) para el alcance de la revisión.
