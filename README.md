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

### Las dos interfaces comparten los túneles

La terminal y la app de escritorio no manejan túneles cada una por su lado.
Los dos hablan con un mismo **agente**: un proceso en segundo plano que es
el único dueño de los túneles.

- Si inicias un túnel en una interfaz, sale **activo en la otra**, con el
  mismo tiempo encendido y sus logs. Si lo detienes en cualquiera, se
  detiene para las dos.
- Crear, editar o eliminar un túnel, o cambiarle el perfil, se refleja al
  instante en la otra interfaz.
- El agente se inicia solo con la primera interfaz que abras; no hay que
  hacer nada. Es el mismo binario con el argumento `agent`. Su salida queda
  en `~/Library/Caches/scriptstui/agent.log`.
- **`q` o `ctrl+c` en la terminal detiene todo:** todos los túneles, el
  agente y también la app gráfica, si está abierta.
- **Cerrar la ventana de la app gráfica no detiene nada** si la terminal
  sigue abierta. Si la cerraste y no queda ninguna otra interfaz, los
  túneles se detienen a los 5 s.
- Si el agente no puede iniciarse, la interfaz funciona igual pero con
  túneles propios, y avisa que no se comparten.

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
| `g`           | **Nuevo.** Abrir la interfaz gráfica (comparte los mismos túneles) |
| `?`           | Ver todos los atajos (cierra con cualquier tecla) |
| `q`           | Salir: detiene todos los túneles y cierra también la interfaz gráfica |

Atajos adicionales (ver también con `?`):

| Tecla     | Acción                                                        |
|-----------|-----------------------------------------------------------------|
| `espacio` | Marcar varios túneles para iniciarlos/detenerlos juntos         |
| `t`       | **Nuevo.** Verificar todos los túneles (en cola o en paralelo) y mostrar cuáles fallaron la conexión (ver [Verificar túneles](#verificar-túneles)) |
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

### Qué muestra el panel principal

- **Distribución.** Arriba a la izquierda está la lista de túneles y
  debajo un recuadro de información. Los logs ocupan toda la altura a la
  derecha y los atajos quedan en un recuadro abajo. Si la lista no cabe,
  se desplaza con el cursor.
- **Recuadro de información.** Tiene el título, una **onda de señal**
  animada, cuántos túneles están activos, el aviso de vencimiento SSO y la
  **fecha y hora**. La onda avanza mientras haya túneles activos y cambia
  según su salud:
  - verde y limpia si todos responden;
  - ámbar si alguno está iniciando;
  - roja y con interferencia si alguno no responde o falló.

  Si no hay nada activo, es una línea plana gris. En terminales bajas se
  dibuja con menos filas.
- **Vencimiento SSO.** Muestra la sesión más próxima a vencer entre los
  perfiles asignados a túneles activados. Se ve ámbar si faltan menos de
  30 minutos y rojo si faltan menos de 10 o si ya venció. Lee la caché del
  AWS CLI cada minuto, así que también detecta un login hecho desde otra
  terminal.
- **Spinner** (`⠋⠙⠹…`) en lugar del punto mientras un túnel inicia o se
  detiene.
- **Monitor de salud.** Cada 45 s, a cada túnel activo se le manda una
  conexión, igual que al verificar. El estado muestra el tiempo encendido y
  la latencia (`activo · 1h15m · 43ms`). La latencia solo se mide en
  Postgres/Redshift, porque en otros puertos no hay nada que responda. Si
  un túnel deja de contestar, pasa a ámbar con `sin respuesta`, y queda
  una línea `[SALUD]` en sus logs.
- **Túnel que falla.** Si un túnel falla al iniciar o se cae, sus procesos
  se cierran y queda en `falló` con una cuenta atrás (`falló · se detiene
  en 25s`). A los 30 s pasa solo a `detenido` y queda una línea `[AUTO]` en
  sus logs. Si lo reinicias o lo detienes antes, la cuenta atrás se anula.
- **Marca de verificación.** Después de `t`, cada túnel queda con `✓` o
  `✗` junto al puerto. La marca se va apagando y desaparece a los 5
  minutos.

### Panel de cuentas AWS SSO (`a`)

| Tecla         | Acción                                              |
|---------------|------------------------------------------------------|
| `↑`/`↓`       | Navegar las cuentas                                  |
| `enter` / `l` | Iniciar sesión en la cuenta seleccionada (`aws sso login --profile X`) |
| `n`           | Crear una cuenta nueva (asistente propio, ver abajo) |
| `r`           | Refrescar la lista y el estado de login              |
| `x`           | Cerrar sesión — **global**: `aws sso logout` cierra todas las cuentas a la vez, el AWS CLI no permite cerrar solo una |
| `esc`/`a`     | Volver al panel principal                            |

### Verificar túneles (`t`) — nuevo

`t` prueba cada túnel activado y al final lista los que fallaron la
conexión. En el pie de pantalla y en `?` está marcado como **NUEVO**.

Al abrir el panel pregunta cómo probarlos (`↑`/`↓` + `enter`, o `1`/`2`):

- **En cola (uno a uno)** — la opción por defecto.
- **En paralelo** — más rápido, hasta 4 túneles a la vez. No hay choque de
  puertos porque dos túneles activados nunca comparten puerto local; el tope
  es para no disparar el límite de llamadas de AWS (`StartSession`, refresco
  del token SSO), que se vería como fallos falsos.

- **Túnel detenido**: lo abre un momento en su puerto local, manda una
  conexión por él y lo vuelve a cerrar.
- **Túnel ya activo**: prueba su puerto sin reiniciarlo.
- En Postgres/Redshift (puerto remoto 5432/5439) exige que la base
  responda. En otros puertos (Redis, etc.) basta con que el túnel no corte
  la conexión.
- Los túneles desactivados, o los que están iniciando/deteniéndose, se
  omiten. Si el puerto local de un túnel detenido está ocupado por otro
  proceso, ese túnel sale como fallido.
- La salida de cada prueba queda en los logs del túnel con la etiqueta
  `[VERIFICAR]`.

Dentro del panel: `?` explica en palabras simples qué hace y qué hacer si
un túnel falla, `esc` cancela o cierra, y `t` repite la verificación
cuando termina.

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
internal/procman/            arranca/detiene los procesos, streamea logs, verifica túneles
internal/agent/              agente compartido: dueño de los túneles para ambas interfaces
internal/shellenv/           PATH de la terminal para la app abierta desde Finder
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
