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

- Node.js + npm (para el frontend, en `frontend/`)
- El CLI de Wails: `go install github.com/wailsapp/wails/v2/cmd/wails@latest`

## Desarrollo

```sh
cd desktop
wails dev
```

Levanta la app con recarga en caliente del frontend. Si cambias métodos en
`app.go` (nombres, firmas, tipos), regenera los bindings de JS/TS con:

```sh
wails generate module
```

## Compilar

```sh
cd desktop
wails build
```

Genera `desktop/build/bin/scriptstui-desktop.app` (macOS) listo para abrir.

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
