# scriptstui-desktop

Vista de escritorio (ventana nativa) de [scriptstui](../README.md), hecha con
[Wails](https://wails.io). No es una reimplementación: `desktop/app.go` es un
adaptador delgado sobre los mismos paquetes que usa la TUI
(`internal/config`, `internal/procman`, `internal/ssologin`), así que ambas
interfaces comparten un solo backend y un solo estado real (`~/.aws/config`,
`~/.aws/sso/cache`, los procesos de túnel).

Este directorio **no tiene su propio `go.mod`** — es un subpaquete del
módulo raíz (`scriptstui/desktop`) a propósito, porque `internal/...` solo es
importable desde dentro del mismo módulo.

## Requisitos

Además de los del [README principal](../README.md):

- Node.js + npm, para el frontend en `frontend/` (probado con Node 24 y npm 11)
- El CLI de Wails v2:

  ```sh
  go install github.com/wailsapp/wails/v2/cmd/wails@latest
  ```

  `go install` lo deja en `$(go env GOPATH)/bin` — normalmente `~/go/bin`, que
  **no siempre está en el `PATH`**. Si `wails version` responde
  `command not found`, agrégalo (en `~/.zshrc` para que quede):

  ```sh
  export PATH="$PATH:$HOME/go/bin"
  ```

  o llámalo por ruta completa: `~/go/bin/wails build`. Probado con Wails
  v2.13.0; `wails doctor` revisa que no falte nada del entorno.

## Desarrollo

```sh
cd desktop
wails dev
```

Levanta la app con recarga en caliente del frontend (Vite), así que editar
`frontend/src/main.js` o `frontend/src/style.css` se ve al instante sin
recompilar. Si cambias métodos en `app.go` (nombres, firmas, tipos), regenera
los bindings de JS/TS con:

```sh
wails generate module
```

`wails build` ya los regenera solo; esto es para el ciclo de `wails dev`.

## Compilar

```sh
cd desktop
wails build
open build/bin/scriptstui-desktop.app
```

`wails build` hace todo de una pasada: genera los bindings, corre
`npm install` y `npm run build` (Vite) en `frontend/`, compila el Go, empaqueta
el `.app` y lo auto-firma. Tarda ~20-25 s en frío.

El resultado es `desktop/build/bin/scriptstui-desktop.app` (macOS), listo para
abrir con `open` o desde Finder. Para ver la salida estándar del backend
mientras corre, lánzalo por el binario en vez del bundle:

```sh
./build/bin/scriptstui-desktop.app/Contents/MacOS/desktop
```

`build/bin` está en `.gitignore`: el `.app` no viaja en el repo y **se queda
viejo** en cuanto cambia `app.go`, `internal/...` o `frontend/src/`. Recompila
después de un `git pull` o de tocar código; para confirmar si el bundle está
al día, compara fechas:

```sh
ls -l build/bin/scriptstui-desktop.app/Contents/MacOS/desktop app.go frontend/src/main.js
```

Nota: `go build ./...` desde la raíz compila este paquete y sirve para
detectar errores de Go, pero **no** produce la app — no construye el frontend
ni empaqueta el bundle. Para eso siempre `wails build`.

## Distribución

El `.app` que produce `wails build` ya es un bundle completo, pero **no es
autocontenido ni está firmado con un certificado de Apple**. Dos cosas a tener
en cuenta antes de mandárselo a alguien:

- **La máquina destino necesita AWS CLI v2 y el `session-manager-plugin`.**
  Los túneles ejecutan `aws ssm start-session --document-name
  AWS-StartPortForwardingSessionToRemoteHost`, así que sin esos dos binarios la
  app abre pero ningún túnel arranca. También necesita su propio
  `~/.aws/config` (la app no distribuye perfiles ni credenciales).
- **La firma es *ad-hoc*** (`codesign` reporta `Signature=adhoc`,
  `TeamIdentifier=not set`), así que Gatekeeper la rechaza: `spctl -a -vv`
  responde `rejected`.

### Interno / informal (sin cuenta de Apple)

Compila universal (para que corra tanto en Apple Silicon como en Intel) y
empaqueta en un DMG:

```sh
cd desktop
wails build -clean -platform darwin/universal
hdiutil create -volname scriptstui \
  -srcfolder build/bin/scriptstui-desktop.app \
  -ov -format UDZO build/bin/scriptstui.dmg
```

Comprobar antes de repartirlo:

```sh
lipo -archs build/bin/scriptstui-desktop.app/Contents/MacOS/desktop  # x86_64 arm64
hdiutil verify build/bin/scriptstui.dmg                              # checksum VALID
```

El DMG pesa ~7 MB. Como va sin firmar, al abrirlo macOS dirá *"no se puede
comprobar si contiene software malicioso"*; quien lo reciba lo abre con **clic
derecho → Abrir**, o le quita la cuarentena:

```sh
xattr -dr com.apple.quarantine /Applications/scriptstui-desktop.app
```

Sirve para repartir dentro del equipo; no para distribución abierta.

### Firmado y notarizado (requiere Apple Developer, 99 USD/año)

Con un certificado *Developer ID Application* en el llavero (revisa los que
tengas con `security find-identity -v -p codesigning`):

```sh
codesign --deep --force --options runtime --timestamp \
  --sign "Developer ID Application: NOMBRE (TEAMID)" \
  build/bin/scriptstui-desktop.app
# ...volver a crear el DMG con el .app ya firmado, y luego:
xcrun notarytool submit build/bin/scriptstui.dmg \
  --apple-id correo@dominio --team-id TEAMID \
  --password <app-specific-password> --wait
xcrun stapler staple build/bin/scriptstui.dmg
```

Al terminar, `spctl -a -vv build/bin/scriptstui-desktop.app` debe decir
`accepted`.

### Metadata del bundle

Por defecto el bundle sale con el identificador y la versión genéricos de
Wails (`com.wails.scriptstui-desktop`, `1.0.0`). Para distribuir conviene
fijarlos en [`wails.json`](wails.json), que hoy los tiene vacíos:

```json
"info": {
  "companyName": "...",
  "productName": "scriptstui",
  "productVersion": "1.0.0",
  "copyright": "..."
}
```

### Windows

La plantilla del instalador NSIS ya está en
`build/windows/installer/project.nsi` y se genera con `wails build -nsis`,
pero **Wails no cross-compila**: hay que correrlo desde una máquina Windows.

Nada de lo que queda en `build/bin` (ni el `.app` ni el `.dmg`) se versiona —
ese directorio está en `.gitignore`.

## Qué cubre y qué no (todavía)

- **Túneles**: listar, asignar perfil AWS, iniciar/detener, logs en vivo.
- **Cuentas AWS SSO**: listar con estado y tiempo restante de sesión, login,
  logout global, y alta de cuenta nueva (mismo flujo de device
  authorization que la TUI).
- **No incluido**: pegar credenciales AWS a mano (`c` en la TUI) — es el
  respaldo manual para cuando no se usa SSO; si lo necesitas, por ahora usa
  la TUI.

## Estructura

```
app.go              backend: métodos bindeados + forwarding de eventos
main.go              arranca la ventana Wails
wails.json           config del proyecto Wails
frontend/            HTML/CSS/JS vanilla (sin framework)
frontend/wailsjs/     bindings generados (no editar a mano)
```
