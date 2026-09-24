# Recursos de empaquetado Wails

[Índice de documentación](README.md) · [Compilación](build.md)

Todos los recursos nativos están en **`watchparty/build/`**. Ejecuta los comandos
Wails desde `watchparty/`; los binarios resultantes se guardan en `build/bin/`.

## Recursos comunes

- `appicon.png`: icono de la aplicación y fuente para generar iconos por plataforma.
- `bin/`: ejecutables y paquetes generados, ignorados por Git. `wails build -clean`
  limpia esta salida antes de compilar; no guardes aquí archivos fuente.
- `watchparty/wails.json`: nombre de salida, datos del autor y comandos del frontend.

## Windows

En `watchparty/build/windows/`:

| Archivo | Función |
| --- | --- |
| `icon.ico` | Icono del ejecutable. Si falta, Wails puede generarlo desde `appicon.png`. |
| `info.json` | Metadatos de producto, versión y empresa usados al empaquetar. |
| `wails.exe.manifest` | Manifiesto de la aplicación Windows. |
| `installer/project.nsi` | Proyecto del instalador NSIS. |
| `installer/wails_tools.nsh` | Funciones auxiliares del instalador Wails. |

Con NSIS instalado y en `PATH`, genera los avisos antes de compilar. Desde
`watchparty/`:

```powershell
wails build -clean -platform windows/amd64
go list -deps -json . | python ../scripts/collect-licenses.py build/windows/installer/tmp/third-party
wails build -s -m -platform windows/amd64 -nsis -installscope user
```

El ejecutable se llama `build/bin/watchparty.exe`; los artefactos del instalador
se generan en el mismo directorio. Comprueba los metadatos y la ejecución en
Windows antes de distribuir. WebView2 Runtime sigue siendo necesario; el
instalador de Wails incorpora su bootstrapper. Incluye `LICENSE`, créditos y
avisos de las dependencias Go en `licenses/`.

La compilación de CI genera el instalador por usuario en un runner Windows y lo
ofrece como artefacto temporal en cada ejecución. Las etiquetas `vX.Y.Z` lo
publican en [GitHub Releases](https://github.com/Rusysa/watch-party-app/releases).
El instalador `v0.1.0` era para todo el equipo: desinstálalo antes de instalar
`v0.2.1` por usuario. Si ya instalaste `v0.2.0`, instala `v0.2.1` manualmente:
su actualizador anterior no solicitaba confirmación, por lo que las releases
interactivas usan un nombre de instalador diferente que esa versión no puede
descargar. Después, al iniciar y cada seis horas, la aplicación consulta la
última release estable **sin descargar nada**. Muestra «Instalar ahora» y «Ahora
no»; solo tras elegir «Instalar ahora» descarga el instalador, verifica su
SHA-256, cierra la aplicación, instala en el mismo directorio y la reinicia.
«Ahora no» pospone el aviso hasta el siguiente inicio. Las compilaciones locales
de desarrollo y los ejecutables portátiles no se actualizan solos.

## Linux

El ejecutable es `watchparty/build/bin/watchparty`. Requiere GTK, WebKitGTK y las
bibliotecas correspondientes de la distribución donde se compile. Consulta los
paquetes y los tags de WebKitGTK en [compilación](build.md#compilar-en-linux).

El RPM de Fedora x86_64 se define en `packaging/rpm/watchparty.spec` y
`packaging/rpm/watchparty.desktop`. Compila primero el ejecutable y luego,
desde `watchparty/`, ejecuta
`go list -deps -json . | python3 ../scripts/collect-licenses.py` para recopilar
los avisos de los módulos Go usados. Luego, desde la raíz del repositorio,
ejecuta `bash scripts/build-rpm.sh 0.1.0`.
La salida se copia a `watchparty/build/bin/`. Requiere `rpmbuild` y las
bibliotecas de WebKitGTK 4.1 durante la compilación; mpv es una dependencia
de ejecución del RPM. Para instalar un archivo descargado de Releases:
`sudo dnf install ./watchparty-*.rpm`. Las versiones nuevas se descargan de
Releases e instalan de la misma manera; aún no hay repositorio DNF.

`.github/workflows/distribution.yml` comprueba Fedora 44 y Windows amd64. Al
subir una etiqueta `vX.Y.Z`, espera a que ambas compilaciones terminen y publica
en una única GitHub Release únicamente el RPM y el instalador Windows.
La licencia y los créditos permanecen en el repositorio y dentro de ambos paquetes.
`LICENSE` contiene la licencia propia MIT; `license.spdx` la identifica y
`rpm-license.spdx` enumera las licencias del código enlazado en el RPM.
La receta exige la licencia y los avisos de Go.

## Recursos macOS del template

`watchparty/build/darwin/Info.plist` y `Info.dev.plist` son los metadatos del
template Wails para producción y desarrollo. Su presencia no implica que se haya
verificado una distribución macOS en este proyecto; las guías mantenidas cubren
Linux y Windows.
