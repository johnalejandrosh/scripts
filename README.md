# scriptstui

Herramienta para levantar y detener los túneles SSM de AWS que antes vivían
como scripts `.sh` sueltos, y para manejar las cuentas AWS SSO que los
autentican. Reemplaza a los scripts en [legacy-sh/](legacy-sh/) (que se
dejaron ahí solo de referencia, no se usan).

Tiene **dos interfaces**, ambas sobre la misma lógica en `internal/`
(ningún dato ni comportamiento vive solo en una de las dos):

- **TUI** (`main.go`, estilo lazygit) — la interfaz **principal**. Es la que
  se documenta abajo y la que se usa día a día.
- **App de escritorio** ([desktop/](desktop/), Wails) — vista con ventana
  nativa para quien prefiera no usar la terminal. Ver
  [desktop/README.md](desktop/README.md).

## Requisitos

- Go 1.26+ (`go version`) — el módulo declara `go 1.26.5`
- AWS CLI v2 instalado y en el `PATH` (`aws --version`)

## Compilar y ejecutar (TUI)

Desde la raíz del repo:

```sh
go build -o scriptstui .
./scriptstui
```

El binario `scriptstui` queda en la raíz y está en `.gitignore`, así que hay
que recompilarlo cuando cambies `main.go` o algo en `internal/`. Para una
corrida rápida sin dejar binario:

```sh
go run .
```

Otros comandos útiles (compilan/chequean también `desktop/`, sin generar
binarios):

```sh
go build ./...
go vet ./...
```

La app de escritorio se compila con otro comando (`wails`, no `go build`):
ver [App de escritorio](#app-de-escritorio) al final y
[desktop/README.md](desktop/README.md).

## Uso

El panel principal lista los túneles con su estado (`detenido` / `iniciando`
/ `activo` / `deteniendo` / `falló`) y, si está activo, cuánto lleva
corriendo. El panel derecho muestra los logs en vivo del túnel seleccionado,
con el perfil AWS que tiene asignado en su encabezado.

Solo 6 teclas visibles por defecto — el resto vive detrás de `?`:

| Tecla         | Acción                                    |
|---------------|--------------------------------------------|
| `↑`/`↓`       | Navegar la lista                           |
| `enter` / `s` | Iniciar el túnel resaltado                 |
| `x`           | Detener el túnel resaltado                 |
| `v`           | Ver los logs de los 3 túneles a la vez (en vez de solo el resaltado) |
| `a`           | Abrir el panel de cuentas AWS SSO          |
| `?`           | Ver todos los atajos (cierra con cualquier tecla) |
| `q`           | Salir (detiene todo lo que esté corriendo) |

Atajos adicionales (ver también con `?`):

| Tecla     | Acción                                                        |
|-----------|-----------------------------------------------------------------|
| `espacio` | Marcar varios túneles para iniciarlos/detenerlos juntos         |
| `n`       | Crear un túnel nuevo                                             |
| `e`       | Editar el túnel resaltado                                        |
| `D`       | Eliminar el túnel resaltado (pide confirmación)                  |
| `y`       | Copiar el comando `aws ssm` del túnel resaltado                  |
| `C`       | Copiar la cadena de conexión del túnel resaltado                 |
| `p`       | Abre un cuadro para elegir el perfil AWS del túnel resaltado (`↑`/`↓` + `enter`) |
| `c`       | Pegar credenciales AWS manualmente (respaldo, ver abajo)         |
| `pgup`/`pgdn` | Scroll de los logs                                           |

`v` (ver todos los logs) reemplaza la lista y el log único por 3 paneles
apilados, uno por túnel, cada uno con su propio título/estado/perfil — para
vigilar los tres a la vez sin tener que ir cambiando de selección. `v` o
`esc` vuelve a la vista normal.

### Panel de cuentas AWS SSO (`a`)

| Tecla         | Acción                                              |
|---------------|------------------------------------------------------|
| `↑`/`↓`       | Navegar las cuentas                                  |
| `enter` / `l` | Iniciar sesión en la cuenta seleccionada (`aws sso login --profile X`) |
| `n`           | Crear una cuenta nueva (asistente propio, ver abajo) |
| `r`           | Refrescar la lista y el estado de login              |
| `x`           | Cerrar sesión — **global**: `aws sso logout` cierra todas las cuentas a la vez, el AWS CLI no permite cerrar solo una |
| `esc`/`a`     | Volver al panel principal                            |

## Credenciales AWS

Hay dos formas de darle credenciales a la app, y ambas conviven:

1. **Cuentas AWS SSO (`a`)** — recomendado. La app **no inventa ni guarda
   ninguna URL de portal**: lee directo tu `~/.aws/config` y lista los
   perfiles que ya tengan `sso_account_id`/`sso_role_name` configurados
   (formato clásico con `sso_start_url` en el propio perfil, o el moderno con
   `sso_session` apuntando a un bloque `[sso-session ...]` — soporta ambos).

   Cada túnel usa el perfil que le asignes con `p` (abre un cuadro para
   elegir entre "ninguno" y cada cuenta configurada). Si no le asignas
   ninguno, cae al flujo de credenciales pegadas (opción 2).

2. **Pegar credenciales (`c`)** — respaldo manual. Si el túnel seleccionado
   no tiene un perfil asignado, al presionar `enter`/`s` la app primero
   valida las credenciales que ya tengas ambientales (`aws sts
   get-caller-identity`); si fallan, abre un cuadro para pegar el bloque que
   genera AWS (`export AWS_ACCESS_KEY_ID=...` o el formato `.ini` de
   `~/.aws/credentials`, detecta ambos). **No se escribe nada en disco**:
   solo vive como variables de entorno mientras la ventana está abierta.

## Crear una cuenta SSO nueva (`a` → `n`)

Todo pasa **dentro de la misma interfaz** — no se sale a una terminal en
blanco como con `aws configure sso`:

1. `a` para abrir el panel de cuentas, luego `n`.
2. Un formulario pide **2 datos**, con ejemplo de cómo va cada uno:
   - **URL del portal**: ej. `https://d-90671b694f.awsapps.com/start`
     (la que ves detrás de "Portal de acceso" en tu navegador).
   - **Región SSO**: ej. `us-east-1`.

   `tab` cambia de campo, `enter` en el segundo campo continúa.
3. La app abre tu navegador para que apruebes el acceso (si no se abre solo,
   te muestra la URL y el código para pegar a mano) y espera ahí mismo,
   dentro de la TUI.
4. Tras aprobar, te lista **tus cuentas** y luego **tus roles** en la cuenta
   elegida — navegas con `↑`/`↓` y confirmas con `enter`, igual que el resto
   de la app.
5. Con cuenta + rol elegidos, escribe el perfil en `~/.aws/config` (solo
   configuración, nada secreto) e inicia sesión automáticamente con
   `aws sso login --profile <nuevo>` — puede pedirte aprobar el navegador
   una vez más, ya que es el AWS CLI cacheando su propia sesión.
6. Vuelves al panel de cuentas con la nueva cuenta ya lista. En el panel
   principal, selecciona un túnel y presiona `p` para asignarle ese perfil.

`esc` cancela en cualquier paso del asistente.

Por debajo, esto usa solo subcomandos de bajo nivel del AWS CLI (`aws
sso-oidc register-client`/`start-device-authorization`/`create-token`, `aws
sso list-accounts`/`list-account-roles`) — el mismo protocolo que usa `aws
configure sso`, sin depender de su asistente interactivo. El único archivo
que la app escribe es `~/.aws/config` (el bloque `[profile ...]`, sin
secretos); el token de acceso que se usa para listar cuentas/roles se queda
en memoria y se descarta — la sesión real que sí queda cacheada en disco es
la que crea el propio `aws sso login`, en `~/.aws/sso/cache`, con sus
permisos restringidos de siempre.

## Agregar un túnel nuevo

Desde la TUI, con `n`. No hay que tocar código ni recompilar: los túneles
viven en una base SQLite (`scriptstui.db`, en la raíz del proyecto) y se
administran desde la terminal.

| Tecla | Acción                                            |
|-------|---------------------------------------------------|
| `n`   | Crear un túnel                                    |
| `e`   | Editar el resaltado (el `id` no se puede cambiar) |
| `D`   | Eliminarlo (pide confirmación)                    |

Dentro del formulario: `tab`/`↑`/`↓` cambia de campo, `espacio` activa o
desactiva el túnel, `ctrl+s` guarda y `esc` cancela.

El comando `aws ssm start-session` no se escribe a mano: se arma solo con el
target, el host y los puertos. Un túnel desactivado sigue guardado pero no
aparece como arrancable y libera su puerto local.

### La tabla `tunnels`

```sql
id                        -- 'db-balu', clave del túnel
proyecto                  -- agrupador libre; la columna solo se muestra si se usa
title                     -- nombre que sale en la lista
target                    -- instancia EC2, 'i-074e88b2ee9d1d67f'
host, remote_port         -- destino real (RDS, Redis, Redshift...)
local_port                -- puerto local; único entre los túneles activos
region, document_name     -- 'us-east-1' y el documento SSM
profile                   -- nombre de un perfil de ~/.aws/config (se asigna con 'p')
connection_string_mac     -- cadena de conexión lista para pegar, por SO
connection_string_windows
wait_seconds, enabled, sort_order, created_at, updated_at
```

### Copiar la cadena de conexión

El panel derecho muestra, bajo el comando, la cadena de conexión guardada
para el SO en curso (`connection_string_windows` en Windows,
`connection_string_mac` en el resto). `C` la copia al portapapeles.

Vale la pena la tecla: los dos paneles comparten cada fila de la pantalla,
así que seleccionar con el mouse siempre arrastra también la lista de
túneles. `y` hace lo mismo con el comando `aws ssm`.

Las cadenas empiezan vacías — el panel muestra `sin definir` y `C` avisa en
vez de copiar nada. Se llenan con `e`, en los campos `conn. string mac` y
`conn. string win`.

La base guarda **solo el nombre** del perfil AWS: los perfiles en sí siguen
en `~/.aws/config`, que es de donde los lee el CLI de `aws`. Nunca se
escriben credenciales ni tokens en `scriptstui.db`.

Dos túneles activos no pueden compartir `local_port`: lo impide un índice
único y el formulario lo avisa antes de guardar.

En el primer arranque, si la base está vacía, se siembra con los túneles que
están en `internal/config/config.go` y con los perfiles que hubiera en el
antiguo `~/.config/scriptstui/tunnel-profiles.json`. Después de eso esa lista
en Go ya no manda: manda la base.

`scriptstui.db` está en `.gitignore` porque es un binario que conflictúa en
cada edición. Para compartir su contenido: `sqlite3 scriptstui.db .dump`.
Para usar otra ruta, `SCRIPTSTUI_DB=/otra/ruta.db`.

## Estructura

```
main.go                      arranca la TUI (interfaz principal)
scriptstui.db                los túneles (SQLite, se crea en el primer arranque)
internal/store/              la base de túneles: esquema, CRUD y siembra inicial
internal/config/             tipos Service/Step y la siembra inicial de túneles
internal/procman/            arranca/detiene los procesos, streamea logs
internal/awscreds/           parseo/validación de credenciales pegadas
internal/ssologin/           descubre perfiles, login/logout y alta de cuentas SSO
internal/prefs/              lector del JSON viejo de perfiles (solo para migrar)
internal/tui/                interfaz de terminal (bubbletea + lipgloss)
desktop/                     app de escritorio (Wails) — mismo internal/, otra cara
legacy-sh/                    scripts .sh originales, solo de referencia
```

## App de escritorio

Vive en [desktop/](desktop/) como un subpaquete de este mismo módulo Go (no
tiene su propio `go.mod`, por eso puede importar `internal/...` tal cual) con
un frontend propio en `desktop/frontend/`.

No se compila con `go build` sino con el CLI de Wails (que además construye el
frontend y empaqueta el `.app`):

```sh
cd desktop
wails build                                # compila y empaqueta
open build/bin/scriptstui-desktop.app      # abrir (macOS)
wails dev                                  # desarrollo, con recarga en caliente
```

Requiere Node.js + npm y el CLI de Wails instalados; los detalles (incluido el
`PATH` de `wails` tras el `go install`) están en
[desktop/README.md](desktop/README.md), que también explica cómo
[empaquetarla para distribuirla](desktop/README.md#distribución) (build
universal, DMG, firma y notarización).

La TUI sigue siendo la interfaz de referencia: se documenta primero, se
prueba primero, y cualquier funcionalidad nueva en `internal/` queda
disponible en el escritorio casi gratis porque comparten el mismo backend.
