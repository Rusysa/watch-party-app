# Compilación y desarrollo

[Índice de documentación](README.md)

Todos los comandos que empiezan con `cd watchparty` o `Set-Location watchparty`
parten de la raíz del repositorio, no de `docs/`.

## Compilar en Linux

Compila desde Linux para Linux. Requisitos comunes:

- **Go 1.26 o posterior**, con los parches de seguridad actuales (consulta también
  `watchparty/go.mod`).
- **Node.js 22.12 o posterior** y npm; se recomienda una versión LTS actualizada.
- Compilador C, `pkg-config`, GTK 3 y WebKitGTK.
- mpv instalado y accesible mediante `PATH`.

### Ubuntu 24.04 / Debian con WebKitGTK 4.1

```bash
sudo apt update
sudo apt install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev mpv
```

Instala Go y Node desde sus distribuciones oficiales si los paquetes de tu
distribución no cumplen las versiones indicadas. Luego, desde la raíz del repo:

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0
export PATH="$(go env GOPATH)/bin:$PATH"
cd watchparty
wails doctor
go mod download
cd frontend
npm ci
cd ..
wails build -clean -tags webkit2_41
./build/bin/watchparty
```

El ejecutable queda en **`watchparty/build/bin/watchparty`**. Necesita las
bibliotecas de GTK/WebKitGTK correspondientes en el equipo donde se ejecute;
no es un binario Linux completamente estático.

### Fedora / Nobara

```bash
sudo dnf install gcc gcc-c++ pkgconf-pkg-config gtk3-devel webkit2gtk4.1-devel mpv
```

Después sigue los mismos pasos de Wails usando `-tags webkit2_41`. La
disponibilidad de mpv depende de los repositorios habilitados en la distribución.

Si tu distribución utiliza **WebKitGTK 4.0**, instala su paquete de desarrollo
(por ejemplo `libwebkit2gtk-4.0-dev`) y omite `-tags webkit2_41` tanto en `build`
como en `dev`. No mezcles el tag 4.1 con las bibliotecas 4.0.

Para instalar el RPM precompilado de Fedora 44 x86_64, consulta
[empaquetado](packaging.md#linux); no es necesario instalar Go, Node ni Wails
en el equipo de destino.

## Compilar en Windows (amd64)

Compila desde Windows para Windows. Instala:

1. [Go](https://go.dev/dl/) 1.26 o posterior, actualizado.
2. [Node.js](https://nodejs.org/) LTS, versión 22.12 o posterior, con npm.
3. [Microsoft Edge WebView2 Runtime](https://developer.microsoft.com/microsoft-edge/webview2/),
   necesario para ejecutar la interfaz Wails (suele estar instalado en Windows 11).
4. Git si vas a clonar el repositorio. **NSIS es opcional**, solo para generar
   un instalador con `-nsis`.

Abre PowerShell en la raíz del repositorio:

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0
$env:Path += ";$(go env GOPATH)\bin"
Set-Location watchparty
wails doctor
go mod download
Set-Location frontend
npm ci
Set-Location ..
wails build -clean -platform windows/amd64
.\build\bin\watchparty.exe
```

Salida: **`watchparty\build\bin\watchparty.exe`**. Para un instalador, con NSIS
en `PATH`:

```powershell
wails build -clean -platform windows/amd64 -nsis
```

Los artefactos se generan en `build\bin`. También se verificó la generación del
`.exe` desde Linux con `wails build -platform windows/amd64`; esto no sustituye
las pruebas de ejecución en Windows. Para Linux hacen falta sus bibliotecas nativas.
Windows ARM64 no está cubierto por el instalador automático de mpv.

### Cómo se obtiene mpv

- **Linux:** se busca `mpv` en `PATH`. No se descarga un ejecutable de Windows.
- **Windows amd64:** si falta `%APPDATA%\WatchParty\bin\mpv.exe`, se descarga una
  compilación x86_64 de `shinchiro/mpv-winbuild-cmake` por HTTPS. Se verifica el
   SHA-256 publicado por GitHub; se extraen `mpv.exe` y las DLL de ejecución
   necesarias mediante archivos temporales y renombrado.
  Una release sin digest válido, vacía o demasiado grande se rechaza.
- mpv se ejecuta sin configuración de usuario, scripts automáticos ni `ytdl`;
  utiliza enlaces directos. Las descargas tienen un límite de cinco minutos.
- Una instalación creada por versiones anteriores, que solo extraían `mpv.exe`,
  se reemplaza automáticamente en el siguiente inicio para añadir sus DLL.

## Desarrollo y comprobaciones

Desde `watchparty/`, tras instalar las dependencias:

```bash
wails dev -tags webkit2_41   # Linux con WebKitGTK 4.1
# wails dev                # Windows o Linux con WebKitGTK 4.0
```

Wails genera `frontend/wailsjs/` y compila `frontend/dist/`; ambos directorios
están ignorados por Git. En un clon limpio, ejecuta primero Wails para generar
los bindings: `npm run build` aislado necesita esos archivos.

```bash
go test -race ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
cd frontend
npm ci
npm audit
npm run build
```

El detector de carreras necesita CGO y un compilador C; en Windows puede requerir
MinGW-w64 para esa comprobación. `go test ./...` funciona sin el detector.

Las pruebas de `internal/transport` establecen conexiones WebRTC reales sobre
loopback con un relay WebSocket de prueba. No necesitan Google STUN, Render,
mpv ni una interfaz gráfica. Incluyen intercambio bidireccional, malla de tres
participantes, reconexión y validación del formato cifrado de señalización.

Para ejecutar solo esas pruebas:

```bash
go test -race -timeout 90s ./internal/transport -v
```

Consulta [seguridad](security.md) para el resultado de las auditorías y
[empaquetado](packaging.md) para los recursos e instaladores de Wails.
