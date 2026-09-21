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

Con NSIS instalado y en `PATH`:

```powershell
wails build -clean -platform windows/amd64 -nsis
```

El ejecutable se llama `build/bin/watchparty.exe`; los artefactos del instalador
se generan en el mismo directorio. Comprueba los metadatos y la ejecución en
Windows antes de distribuir. WebView2 Runtime sigue siendo necesario.

## Linux

El ejecutable es `watchparty/build/bin/watchparty`. Requiere GTK, WebKitGTK y las
bibliotecas correspondientes de la distribución donde se compile. El repositorio
no contiene recetas propias para `.deb`, `.rpm` o AppImage. Consulta los paquetes
y los tags de WebKitGTK en [compilación](build.md#compilar-en-linux).

## Recursos macOS del template

`watchparty/build/darwin/Info.plist` y `Info.dev.plist` son los metadatos del
template Wails para producción y desarrollo. Su presencia no implica que se haya
verificado una distribución macOS en este proyecto; las guías mantenidas cubren
Linux y Windows.
