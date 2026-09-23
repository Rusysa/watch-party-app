# Créditos y componentes de terceros

Gracias a quienes mantienen los proyectos y herramientas que hacen posible
Watch Party. El código propio se publica bajo [MIT](../LICENSE). Las licencias
de terceros se conservan por separado.

| Componente | Uso | Proyecto y licencia |
| --- | --- | --- |
| [Wails](https://wails.io/) | Aplicación de escritorio, bindings y WebView | [Repositorio y licencia](https://github.com/wailsapp/wails/blob/master/LICENSE) |
| [Pion WebRTC](https://github.com/pion/webrtc) | Conexiones WebRTC entre participantes | [Licencia MIT](https://github.com/pion/webrtc/blob/master/LICENSE) |
| [Gorilla WebSocket](https://github.com/gorilla/websocket) | Transporte WebSocket del signaler | [Licencia BSD-2-Clause](https://github.com/gorilla/websocket/blob/main/LICENSE) |
| [Weron](https://github.com/pojntfx/weron) | Servidor de señalización desplegado aparte | [AGPL-3.0](https://github.com/pojntfx/weron/blob/main/LICENSE) |
| [mpv](https://mpv.io/) | Reproductor externo requerido en cada equipo | [Licencias y compilaciones](https://github.com/mpv-player/mpv/blob/master/Copyright) |
| [FFmpeg](https://ffmpeg.org/) | Decodificación multimedia utilizada por mpv | [Licencias](https://ffmpeg.org/legal.html) |
| [shinchiro/mpv-winbuild-cmake](https://github.com/shinchiro/mpv-winbuild-cmake) | Fuente de binarios mpv para Windows | Consultar los avisos y licencias del archivo descargado |
| [Vite](https://vite.dev/) | Compilación de la interfaz | [Licencia MIT](https://github.com/vitejs/vite/blob/main/LICENSE) |
| [Inter](https://rsms.me/inter/) | Fuente cargada por la interfaz desde Google Fonts | [SIL Open Font License](https://github.com/rsms/inter/blob/master/docs/LICENSE.txt) |
| [Nunito](https://github.com/googlefonts/nunito) | Fuente incluida en los archivos fuente (actualmente no utilizada por la interfaz) | SIL OFL 1.1, texto en `watchparty/frontend/src/assets/fonts/OFL.txt` |
| [Go](https://go.dev/) | Lenguaje y toolchain | [Licencia BSD](https://go.dev/LICENSE) |
| [Node.js](https://nodejs.org/) | Herramienta de compilación | [Licencia](https://github.com/nodejs/node/blob/main/LICENSE) |
| [GTK](https://www.gtk.org/) y [WebKitGTK](https://webkitgtk.org/) | Bibliotecas de la interfaz Linux | Consultar las licencias de los paquetes de la distribución |
| [Syncplay](https://syncplay.pl/) | Inspiración para la corrección de desfase; no se distribuye su código | [Proyecto](https://github.com/Syncplay/syncplay) |

Otras bibliotecas utilizadas directamente se identifican en `watchparty/go.mod`:
`go-winio`, `sevenzip`, `google/uuid` y los módulos de Pion. El inventario
completo de dependencias Go, incluidas las transitivas, se obtiene de
`watchparty/go.mod` y sus verificaciones están en `watchparty/go.sum`;
las dependencias de compilación del frontend están en
`watchparty/frontend/package-lock.json`. Se deben conservar sus avisos al
preparar cualquier release. Render y GitHub Actions/Releases prestan servicios
de alojamiento y automatización; no son dependencias empaquetadas.

El RPM Linux utiliza el mpv instalado por el sistema y no incluye binarios de
Weron, mpv, GTK ni WebKitGTK. Sus licencias y textos se obtienen en los paquetes
respectivos. El instalador Windows de mpv es independiente del RPM.
