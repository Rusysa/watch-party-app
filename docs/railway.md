# Desplegar el signaler en Railway

[Índice de documentación](README.md)

El directorio [`signaler/`](../signaler/) contiene únicamente el despliegue del
**servidor Weron**. La aplicación de escritorio está en
[`watchparty/`](../watchparty/).

El signaler permite descubrir participantes e intercambiar señalización WebRTC;
también mantiene conexiones para descubrimiento y reconexiones. El vídeo se
descarga desde el origen por cada participante y los mensajes de reproducción
circulan por los canales de datos P2P.

## Contenedor local

Desde la raíz del repositorio, con Docker:

```bash
docker build -t watchparty-signaler ./signaler
docker run --rm --name watchparty-signaler -p 127.0.0.1:15325:15325 watchparty-signaler
```

En el cliente configura `ws://127.0.0.1:15325/`. La excepción sin TLS solo permite
loopback; para conexiones entre equipos utiliza un proxy HTTPS/WebSocket y
configura `wss://tu-dominio/`.

El Dockerfile fija Weron **v0.3.0 por digest multiplataforma** y utiliza un usuario
sin privilegios. Sin `DATABASE_URL` ni `REDIS_URL`, Weron utiliza almacenamiento
y broker en memoria. Las comunidades son efímeras; no hay
volumen ni base de datos persistente. `PORT` cambia el puerto interno (15325 por
defecto); ajusta también el mapeo de puertos cuando lo cambies.

## Railway

1. Crea un servicio desde este repositorio en Railway.
2. Configura **Root Directory: `/signaler`**. No despliegues `watchparty/` como
   servidor: es la aplicación de escritorio.
3. Selecciona **Config File Path: `/signaler/railway.toml`**. La ruta del archivo
   de configuración es relativa al repositorio; el Dockerfile se resuelve
   dentro de la raíz del servicio.
4. Railway utiliza Docker y asigna `PORT`; el proceso escucha en todas las
   interfaces en ese puerto. No se necesitan variables de base de datos para
   las salas efímeras.
5. Genera un dominio público en Networking. Si Railway muestra
   `https://tu-servicio.up.railway.app`, utiliza
   **`wss://tu-servicio.up.railway.app/`** en la aplicación.
6. Configura esa misma URL en todos los clientes antes de crear/unirte a una
   sala. El endpoint WebSocket es `/`.
7. Deja vacío **Healthcheck Path** en Railway, también si anteriormente se había
   configurado `/` manualmente. Weron sin API de gestión devuelve HTTP 501 para
   un GET normal a `/`; eso no significa que el WebSocket esté caído. El archivo
   `railway.toml` no define ese healthcheck incorrecto.

Si publicas únicamente el contenido de `signaler/` en otro repositorio, utiliza
`/` como Root Directory y `/railway.toml` como ruta de configuración.

| Variable | Valor / función |
| --- | --- |
| `PORT` | Asignada por Railway; localmente se usa 15325 por defecto. |
| `DATABASE_URL` / `REDIS_URL` | Se dejan sin definir en este despliegue efímero. |
| `WATCHPARTY_SIGNALER_URL` | Variable **del equipo cliente**, no del contenedor; URL inicial opcional del signaler. |

El cliente usa `ws://127.0.0.1:15325/` si no se establece
`WATCHPARTY_SIGNALER_URL`. Puede cambiarse desde la interfaz para nuevas salas;
esa selección no persiste entre ejecuciones. Para fijarla al iniciar:

```bash
WATCHPARTY_SIGNALER_URL=wss://tu-servicio.up.railway.app/ ./watchparty/build/bin/watchparty
```

```powershell
$env:WATCHPARTY_SIGNALER_URL = "wss://tu-servicio.up.railway.app/"
.\watchparty\build\bin\watchparty.exe
```

Los dominios mostrados son ejemplos; usa el dominio asignado a tu servicio.

## Verificar el servidor local

Con el contenedor anterior escuchando en el puerto 15325, desde `watchparty/`:

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
son de loopback; úsala con un signaler local. Para comprobar un despliegue remoto
en Railway y NAT reales, abre los clientes en dos equipos y entra en una sala.

## Relación con la corrección de DTLS

El cliente actualizado usa Pion WebRTC v4 y DTLS v3. El signaler solo retransmite
los mensajes cifrados de descubrimiento y SDP; no participa en el handshake DTLS
entre los equipos. Por tanto, no necesita migrar su protocolo para esta corrección.
Actualiza todos los clientes y crea salas nuevas; consulta
[arquitectura y transporte](transport.md).

## Seguridad y mantenimiento

El operador del signaler recibe el código y la contraseña de la comunidad en
la query de la conexión WebSocket. TLS protege el tránsito, **no oculta esos
datos al servidor o al proxy TLS**. Evita registrar query strings y no reutilices
contraseñas de otros servicios. La misma contraseña se pasa a Weron como clave
de cifrado de la señalización; no protege frente a un operador que ya la conoce.

Actualizar la imagen requiere revisar una nueva versión/digest y analizar sus
dependencias. Fijar el digest hace reproducible la base, pero no certifica que
la imagen carezca de vulnerabilidades. Se construyó y ejecutó localmente con
Podman y se verificó el intercambio WebRTC contra el contenedor. No se realizó
un escaneo de vulnerabilidades del sistema base ni un despliegue remoto. Consulta
[seguridad](security.md) para el alcance de la revisión.
