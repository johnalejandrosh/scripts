import './style.css';

import * as App from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime/runtime';

const MAX_LOG_LINES = 500;

// How long a Verificar result stays on its row, fading out, as in the
// terminal UI.
const VERIFY_MARK_TTL_MS = 5 * 60 * 1000;

const state = {
    tab: 'tunnels',
    services: [],
    serviceLogs: {},
    selectedServiceId: null,

    // Verificar: null when closed, else {choosing, parallel, help, update}.
    verify: null,
    sso: { expired: 0, soonestSeconds: 0, known: false },
    backendWarning: '',
    backendLost: '',

    profiles: [],
    ssoOutput: [],
    ssoRunning: false,
    ssoError: '',

    wizard: null, // {step:'form'|'waiting'|'accounts'|'roles', ...}
};

const root = document.getElementById('app');

function escapeHtml(s) {
    return String(s ?? '').replace(/[&<>"']/g, (c) => ({
        '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
    }[c]));
}

function fmtRemaining(expiresAt) {
    if (!expiresAt) return '';
    const ms = new Date(expiresAt).getTime() - Date.now();
    if (Number.isNaN(ms)) return '';
    if (ms <= 0) return ' · vence en breve';
    const mins = Math.round(ms / 60000);
    const h = Math.floor(mins / 60);
    const m = mins % 60;
    return h > 0 ? ` · vence en ${h}h ${m}m` : ` · vence en ${m}m`;
}

function pushLog(map, id, line) {
    const arr = map[id] || (map[id] = []);
    arr.push(line);
    if (arr.length > MAX_LOG_LINES) arr.splice(0, arr.length - MAX_LOG_LINES);
}

// --- data loading ------------------------------------------------------

function refreshServices() {
    App.ListServices().then((list) => {
        state.services = list || [];
        if (!state.selectedServiceId && state.services.length) {
            selectService(state.services[0].id);
        }
        render();
    });
}

function selectService(id) {
    state.selectedServiceId = id;
    // Logs from before this window opened live in the Go side's mirror.
    App.GetLogs(id).then((lines) => {
        state.serviceLogs[id] = lines || [];
        render();
    });
}

function refreshSSOSummary() {
    App.GetSSOSummary().then((sum) => {
        state.sso = sum;
        render();
    });
}

function service(id) {
    return state.services.find((s) => s.id === id);
}

function refreshProfiles() {
    App.ListSSOProfiles().then((list) => {
        state.profiles = list || [];
        render();
    }).catch((err) => {
        state.ssoError = String(err);
        render();
    });
}

EventsOn('tunnel:event', (ev) => {
    const svc = service(ev.serviceId);
    if (svc) svc.live = ev.live;
    if (ev.line && state.serviceLogs[ev.serviceId]) {
        pushLog(state.serviceLogs, ev.serviceId, ev.line);
    }
    render();
});

// The terminal UI created, edited or deleted a tunnel, or changed a profile.
EventsOn('tunnels:changed', refreshServices);

EventsOn('backend:lost', (msg) => {
    state.backendLost = String(msg);
    render();
});

EventsOn('verify:update', (update) => {
    if (!state.verify || state.verify.choosing) return;
    state.verify.update = update;
    render();
});

EventsOn('sso:line', (ev) => {
    state.ssoOutput.push(ev.line);
    if (state.ssoOutput.length > MAX_LOG_LINES) state.ssoOutput.shift();
    render();
});

EventsOn('sso:done', (ev) => {
    state.ssoRunning = false;
    state.ssoOutput.push(ev.error ? `✗ ${ev.error}` : '✓ listo');
    refreshProfiles();
    render();
});

EventsOn('sso:new:accounts', (accounts) => {
    if (!state.wizard) return;
    state.wizard.step = 'accounts';
    state.wizard.accounts = accounts || [];
    render();
});

EventsOn('sso:new:error', (err) => {
    if (!state.wizard) return;
    state.wizard.step = 'form';
    state.wizard.error = String(err);
    render();
});

// --- actions -------------------------------------------------------------

function openVerify() {
    const parallel = state.verify ? state.verify.parallel : false;
    state.verify = { choosing: true, parallel, help: false, update: null };
    render();
}

function beginVerify() {
    const v = state.verify;
    v.choosing = false;
    App.StartVerify(v.parallel).then((update) => {
        if (state.verify === v && !v.update) v.update = update;
        render();
    });
    render();
}

function closeVerify() {
    App.CancelVerify();
    state.verify = null;
    render();
}

function startService(id) {
    App.StartService(id).catch((err) => alert(String(err)));
}

function stopService(id) {
    App.StopService(id);
}

function copyCommand(id) {
    const svc = state.services.find((s) => s.id === id);
    if (!svc) return;
    navigator.clipboard.writeText(svc.command).catch((err) =>
        alert(`No se pudo copiar: ${err}`),
    );
}

function assignProfile(id, profile) {
    App.AssignProfile(id, profile)
        .then(refreshServices)
        .catch((err) => alert(`No se pudo guardar el perfil: ${err}`));
}

function loginSSO(name) {
    state.ssoRunning = true;
    state.ssoOutput = [`iniciando sesión en ${name}...`];
    App.LoginSSO(name);
    render();
}

function logoutSSO() {
    state.ssoRunning = true;
    state.ssoOutput = ['cerrando todas las sesiones...'];
    App.LogoutSSO();
    render();
}

function openWizard() {
    state.wizard = { step: 'form', url: '', region: 'us-east-1', error: '' };
    render();
}

function closeWizard() {
    if (state.wizard) App.CancelNewSSOProfile();
    state.wizard = null;
    render();
}

function submitWizardForm(url, region) {
    state.wizard.url = url;
    state.wizard.region = region;
    state.wizard.error = '';
    state.wizard.step = 'waiting';
    render();
    App.StartNewSSOProfile(url, region).then((device) => {
        if (!state.wizard) return;
        state.wizard.device = device;
        render();
    }).catch((err) => {
        if (!state.wizard) return;
        state.wizard.step = 'form';
        state.wizard.error = String(err);
        render();
    });
}

function pickAccount(accountId, accountName) {
    state.wizard.error = '';
    App.ListSSORoles(accountId).then((roles) => {
        state.wizard.accountId = accountId;
        state.wizard.accountName = accountName;
        state.wizard.roles = roles || [];
        state.wizard.step = 'roles';
        render();
    }).catch((err) => {
        state.wizard.error = String(err);
        render();
    });
}

function pickRole(roleName) {
    const { accountId, region } = state.wizard;
    App.FinishNewSSOProfile(accountId, roleName, region).then((profileName) => {
        state.wizard = null;
        state.tab = 'sso';
        state.ssoRunning = true;
        state.ssoOutput = [`✓ perfil creado: ${profileName}`, 'iniciando sesión...'];
        render();
    }).catch((err) => {
        state.wizard.error = String(err);
        render();
    });
}

// --- rendering -------------------------------------------------------------

function statusBadgeClass(live) {
    switch (live.status) {
        case 'activo': return live.health === 'bad' ? 'starting' : 'ok';
        case 'iniciando':
        case 'deteniendo': return 'starting';
        case 'falló': return 'failed';
        default: return 'off';
    }
}

// compactDuration writes a duration in at most two units — 34s, 12m, 1h05m,
// 2d3h — like the terminal UI.
function compactDuration(ms) {
    const s = Math.max(0, Math.floor(ms / 1000));
    if (s < 60) return `${s}s`;
    const m = Math.floor(s / 60);
    if (m < 60) return `${m}m`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h${String(m % 60).padStart(2, '0')}m`;
    return `${Math.floor(h / 24)}d${h % 24}h`;
}

// How long procman leaves a failed tunnel before stopping it on its own.
const FAILED_RESET_MS = 30 * 1000;

function statusText(live) {
    let text = live.status;
    if (live.status === 'falló' && live.since) {
        const left = FAILED_RESET_MS - (Date.now() - new Date(live.since).getTime());
        return `${text} · se detiene en ${Math.max(0, Math.round(left / 1000))}s`;
    }
    if (live.status !== 'activo' || !live.since) return text;
    text += ' · ' + compactDuration(Date.now() - new Date(live.since).getTime());
    if (live.health === 'bad') return text + ' · sin respuesta';
    if (live.health === 'ok' && live.latencyMs > 0) return `${text} · ${live.latencyMs}ms`;
    return text;
}

function verifyMark(live) {
    if (!live.verifiedAt) return '';
    const age = Date.now() - new Date(live.verifiedAt).getTime();
    if (age > VERIFY_MARK_TTL_MS) return '';
    // Fades from fully visible to faint over its lifetime.
    const opacity = (1 - 0.75 * (age / VERIFY_MARK_TTL_MS)).toFixed(2);
    return live.verifiedOk
        ? `<span class="verify-mark ok" style="opacity:${opacity}" title="última verificación: conecta">✓</span>`
        : `<span class="verify-mark failed" style="opacity:${opacity}" title="última verificación: falló">✗</span>`;
}

function renderTunnelsView() {
    const rows = state.services.map((s) => {
        const live = s.live || { status: 'detenido' };
        const busy = live.status === 'iniciando' || live.status === 'deteniendo';
        const selected = s.id === state.selectedServiceId ? ' selected' : '';
        const options = ['<option value="">(sin perfil)</option>']
            .concat(state.profiles.map((p) => `<option value="${escapeHtml(p.name)}" ${p.name === s.profile ? 'selected' : ''}>${escapeHtml(p.name)}</option>`));
        return `
      <div class="row${selected}" data-select-service="${escapeHtml(s.id)}">
        <div class="row-top">
          <span class="row-title">${escapeHtml(s.title)}</span>
          <span class="row-status">
            ${verifyMark(live)}
            <span class="port">[${s.localPort}]</span>
            <span class="badge ${statusBadgeClass(live)}" data-status-id="${escapeHtml(s.id)}">${busy ? '<span class="spinner"></span>' : ''}${escapeHtml(statusText(live))}</span>
          </span>
        </div>
        <div class="row-detail">
          <select class="profile-select" data-assign="${escapeHtml(s.id)}">${options.join('')}</select>
        </div>
        <div class="row-command">
          <code>${escapeHtml(s.command)}</code>
          <button class="btn small" data-copy="${escapeHtml(s.id)}">copiar</button>
        </div>
        <div class="row-actions">
          <button class="btn primary" data-start="${escapeHtml(s.id)}">iniciar</button>
          <button class="btn" data-stop="${escapeHtml(s.id)}">detener</button>
        </div>
      </div>`;
    }).join('') || '<div class="empty">No hay túneles configurados.</div>';

    const sel = service(state.selectedServiceId);
    const logs = sel ? (state.serviceLogs[sel.id] || []).join('\n') : '';

    return `
    <div class="left-col">
      <div class="panel list-panel">
        <div class="panel-header">
          <span>Túneles</span>
          <button class="btn new-feature" id="btn-verify" title="Probar todos los túneles (tecla t)">t · verificar <span class="new-badge">NUEVO</span></button>
        </div>
        ${rows}
      </div>
      ${renderInfoCard()}
    </div>
    <div class="panel log-panel">
      <div class="panel-header"><span>Logs${sel ? ' — ' + escapeHtml(sel.title) : ''}</span></div>
      <div class="log-body" id="log-body">${escapeHtml(logs) || '<span class="empty">Selecciona un túnel para ver sus logs.</span>'}</div>
    </div>`;
}

// --- info card: signal wave, activity, SSO and clock -----------------------

function activity() {
    let running = 0, bad = 0, busy = 0;
    for (const s of state.services) {
        const live = s.live || {};
        if (live.status === 'activo') {
            running++;
            if (live.health === 'bad') bad++;
        } else if (live.status === 'falló') {
            bad++;
        } else if (live.status === 'iniciando' || live.status === 'deteniendo') {
            busy++;
        }
    }
    let level = 'ok';
    if (running === 0 && busy === 0) level = 'idle';
    else if (bad > 0) level = 'bad';
    else if (busy > 0) level = 'busy';
    return { level, running, bad };
}

const WEEKDAYS = ['dom', 'lun', 'mar', 'mié', 'jue', 'vie', 'sáb'];
const MONTHS = ['ene', 'feb', 'mar', 'abr', 'may', 'jun', 'jul', 'ago', 'sep', 'oct', 'nov', 'dic'];

function clockText(d = new Date()) {
    const pad = (n) => String(n).padStart(2, '0');
    return `${WEEKDAYS[d.getDay()]} ${d.getDate()} ${MONTHS[d.getMonth()]} · ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

function ssoText() {
    const { expired, soonestSeconds, known } = state.sso;
    if (expired > 0) {
        return `<span class="sso bad">✗ ${expired === 1 ? '1 sesión SSO vencida' : expired + ' sesiones SSO vencidas'} · entra en Cuentas SSO</span>`;
    }
    if (!known) return '';
    const text = 'SSO vence en ' + compactDuration(soonestSeconds * 1000);
    if (soonestSeconds < 600) return `<span class="sso bad">⚠ ${text}</span>`;
    if (soonestSeconds < 1800) return `<span class="sso warn">⚠ ${text}</span>`;
    return `<span class="sso">${text}</span>`;
}

function renderInfoCard() {
    const { level, running, bad } = activity();
    let label;
    if (level === 'idle') {
        label = `<span class="activity idle">sin túneles activos${bad ? ` · ${bad} fallido` : ''}</span>`;
    } else {
        label = `<span class="activity ${level}">♥ ${running} activo${running === 1 ? '' : 's'}${bad ? ` · ${bad} sin respuesta` : ''}</span>`;
    }
    return `
      <div class="panel info-card">
        <div class="info-title">scriptstui — gestor de túneles</div>
        <canvas id="wave" class="wave"></canvas>
        ${label}
        <div class="info-bottom">${ssoText()}<span class="clock" id="clock">${clockText()}</span></div>
      </div>`;
}

const WAVE_COLORS = { ok: '#3FB950', busy: '#D4A72C', bad: '#F85149', idle: '#6E7681' };
let wavePhase = 0;

// drawWave animates the signal: a clean sine when every tunnel answers,
// interference when one is failing, a flat line with nothing running — the
// same rules as the terminal UI's wave.
function drawWave() {
    const canvas = document.getElementById('wave');
    if (canvas) {
        const dpr = window.devicePixelRatio || 1;
        const w = canvas.clientWidth, h = canvas.clientHeight;
        if (canvas.width !== w * dpr || canvas.height !== h * dpr) {
            canvas.width = w * dpr;
            canvas.height = h * dpr;
        }
        const ctx = canvas.getContext('2d');
        ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
        ctx.clearRect(0, 0, w, h);

        const { level } = activity();
        const mid = h / 2, amp = h * 0.38, cycles = 3;
        ctx.strokeStyle = WAVE_COLORS[level];
        ctx.lineWidth = 2;
        ctx.shadowColor = WAVE_COLORS[level];
        ctx.shadowBlur = level === 'idle' ? 0 : 6;
        ctx.beginPath();
        for (let x = 0; x <= w; x += 2) {
            const t = (2 * Math.PI * cycles * x) / w;
            let v = 0;
            if (level === 'ok' || level === 'busy') v = Math.sin(t + wavePhase);
            if (level === 'bad') v = 0.6 * Math.sin(t + wavePhase) + 0.4 * Math.sin(3.7 * t - 2 * wavePhase);
            const y = mid - amp * v;
            if (x === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
        }
        ctx.stroke();
        if (level !== 'idle') wavePhase += 0.06;
    }
    requestAnimationFrame(drawWave);
}

// tickLive refreshes what changes with time alone — uptimes, the clock, the
// fading verify marks — without redrawing the page, which would close an open
// profile picker.
function tickLive() {
    const clock = document.getElementById('clock');
    if (clock) clock.textContent = clockText();
    root.querySelectorAll('[data-status-id]').forEach((el) => {
        const svc = service(el.dataset.statusId);
        if (!svc || !svc.live || (svc.live.status !== 'activo' && svc.live.status !== 'falló')) return;
        el.textContent = statusText(svc.live);
    });
}

// --- Verificar modal ---------------------------------------------------------

const VERIFY_ICONS = { queued: '·', checking: '<span class="spinner"></span>', ok: '✓', failed: '✗', skipped: '-' };

function renderVerify() {
    const v = state.verify;
    if (!v) return '';
    let body;
    if (v.help) {
        body = renderVerifyHelp();
    } else if (v.choosing) {
        const option = (parallel, title, hint) => `
          <div class="verify-option ${v.parallel === parallel ? 'selected' : ''}" data-verify-mode="${parallel ? 'parallel' : 'queue'}">
            <div class="row-title">${title}</div>
            <div class="row-detail">${hint}</div>
          </div>`;
        body = `
          <p>¿Cómo quieres probarlos?</p>
          ${option(false, 'En cola (uno a uno)', 'por defecto · más lento, una sesión a la vez')}
          ${option(true, 'En paralelo', `más rápido · hasta 4 túneles a la vez`)}
          <div class="modal-actions">
            <button class="btn" id="vf-help">¿qué es esto?</button>
            <button class="btn" id="vf-close">cancelar</button>
            <button class="btn primary" id="vf-begin">empezar</button>
          </div>`;
    } else {
        const u = v.update || { items: [], done: false };
        const items = u.items.map((it) => `
          <div class="verify-item ${it.state}">
            <span class="verify-icon">${VERIFY_ICONS[it.state] || ''}</span>
            <span class="verify-title">${escapeHtml(it.title)}</span>
            <span class="verify-note">${escapeHtml(it.note)}</span>
          </div>`).join('') || '<div class="empty">No hay túneles activados para verificar.</div>';
        const mode = u.parallel ? 'en paralelo' : 'en cola';
        const count = (st) => u.items.filter((it) => it.state === st).length;
        let footer;
        if (!u.done) {
            const finished = u.items.length - count('queued') - count('checking');
            footer = `<p class="verify-progress">Verificando ${mode} · ${finished} de ${u.items.length} listos · ${count('checking')} en curso</p>`;
        } else {
            let summary = `${count('ok')} ok · ${count('failed')} fallaron`;
            if (count('skipped')) summary += ` · ${count('skipped')} omitidos`;
            if (u.disabled) summary += ` · ${u.disabled} desactivados sin probar`;
            summary += ` · ${mode} en ${(u.elapsedMs / 1000).toFixed(1)}s`;
            const failed = u.items.filter((it) => it.state === 'failed');
            footer = failed.length === 0
                ? `<p class="verify-all-ok">✓ Todos los túneles conectan</p><p class="empty">${summary}</p>`
                : `<p class="error-text">Fallaron la conexión:</p>
                   <ul class="verify-failed">${failed.map((it) => `<li>✗ ${escapeHtml(it.title)}</li>`).join('')}</ul>
                   <p class="empty">${summary} · el detalle queda en los logs de cada túnel</p>`;
        }
        body = `
          <div class="verify-list">${items}</div>
          ${footer}
          <div class="modal-actions">
            <button class="btn" id="vf-help">?</button>
            ${u.done ? '<button class="btn" id="vf-again">verificar de nuevo</button>' : ''}
            <button class="btn ${u.done ? 'primary' : ''}" id="vf-close">${u.done ? 'cerrar' : 'cancelar'}</button>
          </div>`;
    }
    return `
    <div class="modal-backdrop">
      <div class="modal verify-modal">
        <h2>Verificar túneles <span class="new-badge">NUEVO</span></h2>
        <p class="empty">Los detenidos se abren un momento y se vuelven a cerrar; los que ya están activos se prueban sin reiniciarlos.</p>
        ${body}
      </div>
    </div>`;
}

function renderVerifyHelp() {
    return `
      <div class="verify-help">
        <h3>Para qué sirve</h3>
        <p>Revisa si cada túnel logra llegar de verdad a su base de datos o servicio, para que sepas cuáles funcionan antes de necesitarlos.</p>
        <h3>Qué hace con tus túneles</h3>
        <ul>
          <li>Si está detenido: lo abre unos segundos, prueba la conexión y lo cierra.</li>
          <li>Si ya está activo: solo prueba la conexión. No lo reinicia ni lo corta.</li>
          <li class="dim">Los desactivados no se prueban.</li>
        </ul>
        <h3>En cola o en paralelo</h3>
        <ul>
          <li>En cola: prueba un túnel a la vez. Es más lento, pero es lo más seguro.</li>
          <li>En paralelo: prueba hasta 4 a la vez, así termina mucho antes.</li>
          <li class="dim">No pasan de 4 a la vez para que AWS no bloquee tantas conexiones juntas.</li>
        </ul>
        <h3>Cómo leer el resultado</h3>
        <ul class="legend">
          <li><span class="ok">✓</span> conecta bien</li>
          <li><span class="failed">✗</span> no pudo conectar; al lado dice por qué</li>
          <li><span class="spinner"></span> probándose ahora</li>
          <li>· esperando su turno</li>
          <li>- omitido (se estaba iniciando o deteniendo)</li>
        </ul>
        <h3>Si uno falla</h3>
        <ul>
          <li>Mensajes con "token", "expired" o "SSO": tu sesión de AWS venció. Entra de nuevo en Cuentas SSO.</li>
          <li>"is not connected" o "TargetNotConnected": el perfil es de otra cuenta o la instancia está apagada. Revisa el perfil del túnel.</li>
          <li>"cerró la conexión" o "no respondió": la instancia no llega al host. Revisa el host y el puerto remoto del túnel.</li>
          <li>"puerto ... en uso": otro programa ocupa ese puerto local.</li>
          <li class="dim">El detalle completo queda en los logs del túnel, marcado [VERIFICAR].</li>
        </ul>
      </div>
      <div class="modal-actions"><button class="btn primary" id="vf-help-close">volver</button></div>`;
}

function renderSSOView() {
    const rows = state.profiles.map((p) => `
      <div class="row">
        <div class="row-top">
          <span class="row-title">${escapeHtml(p.name)}</span>
          <span class="badge ${p.loggedIn ? 'ok' : 'off'}">${p.loggedIn ? '✓ logueado' + fmtRemaining(p.expiresAt) : '✗ no logueado'}</span>
        </div>
        <div class="row-detail">cuenta ${escapeHtml(p.accountId)} · rol ${escapeHtml(p.roleName)}</div>
        <div class="row-actions">
          <button class="btn primary" data-login="${escapeHtml(p.name)}" ${state.ssoRunning ? 'disabled' : ''}>iniciar sesión</button>
        </div>
      </div>`).join('') || '<div class="empty">No hay cuentas SSO en ~/.aws/config todavía.</div>';

    return `
    <div class="panel list-panel">
      <div class="panel-header">
        <span>Cuentas AWS SSO</span>
        <div class="row-actions">
          <button class="btn" id="btn-new-sso">+ nueva</button>
          <button class="btn" id="btn-logout-sso" ${state.ssoRunning ? 'disabled' : ''}>cerrar todas</button>
        </div>
      </div>
      ${rows}
    </div>
    <div class="panel log-panel">
      <div class="panel-header"><span>Salida de aws sso</span></div>
      <div class="log-body">${escapeHtml(state.ssoOutput.join('\n'))}${state.ssoRunning ? '<br><em>ejecutando...</em>' : ''}</div>
    </div>`;
}

function renderWizard() {
    const w = state.wizard;
    if (!w) return '';

    let body = '';
    if (w.step === 'form') {
        body = `
      <label>URL del portal</label>
      <input id="wz-url" placeholder="https://d-xxxxxxxxxx.awsapps.com/start" value="${escapeHtml(w.url)}">
      <label>Región SSO</label>
      <input id="wz-region" placeholder="us-east-1" value="${escapeHtml(w.region)}">
      ${w.error ? `<div class="error-text">${escapeHtml(w.error)}</div>` : ''}
      <div class="modal-actions">
        <button class="btn" id="wz-cancel">cancelar</button>
        <button class="btn primary" id="wz-submit">continuar</button>
      </div>`;
    } else if (w.step === 'waiting') {
        body = w.device ? `
      <p>Aprueba el acceso en el navegador (se intentó abrir solo). Si no se abrió, copia este enlace:</p>
      <div class="code-box">
        <div class="code">${escapeHtml(w.device.userCode)}</div>
        <div class="link-row">
          <input readonly value="${escapeHtml(w.device.verificationUriComplete)}">
        </div>
      </div>
      <p class="empty">Esperando aprobación...</p>
      <div class="modal-actions"><button class="btn" id="wz-cancel">cancelar</button></div>
    ` : `<p class="empty">Iniciando autorización...</p>
      <div class="modal-actions"><button class="btn" id="wz-cancel">cancelar</button></div>`;
    } else if (w.step === 'accounts') {
        const items = (w.accounts || []).map((a) => `
        <div class="row" data-account="${escapeHtml(a.AccountID)}" data-account-name="${escapeHtml(a.AccountName)}">
          <div class="row-title">${escapeHtml(a.AccountName)}</div>
          <div class="row-detail">${escapeHtml(a.AccountID)} · ${escapeHtml(a.Email)}</div>
        </div>`).join('') || '<div class="empty">Sin cuentas.</div>';
        body = `
      <p>Elige una cuenta:</p>
      <div class="accounts-list">${items}</div>
      ${w.error ? `<div class="error-text">${escapeHtml(w.error)}</div>` : ''}
      <div class="modal-actions"><button class="btn" id="wz-cancel">cancelar</button></div>`;
    } else if (w.step === 'roles') {
        const items = (w.roles || []).map((r) => `
        <div class="row" data-role="${escapeHtml(r.RoleName)}">
          <div class="row-title">${escapeHtml(r.RoleName)}</div>
        </div>`).join('') || '<div class="empty">Sin roles.</div>';
        body = `
      <p>Cuenta: ${escapeHtml(w.accountName)} — elige un rol:</p>
      <div class="accounts-list">${items}</div>
      ${w.error ? `<div class="error-text">${escapeHtml(w.error)}</div>` : ''}
      <div class="modal-actions"><button class="btn" id="wz-cancel">cancelar</button></div>`;
    }

    return `
    <div class="modal-backdrop" id="wz-backdrop">
      <div class="modal">
        <h2>➕ Nueva cuenta AWS SSO</h2>
        ${body}
      </div>
    </div>`;
}

let renderPending = false;

function render() {
    // Redrawing while a profile picker is open would close it under the
    // user's cursor; finish the redraw once it loses focus.
    const active = document.activeElement;
    if (active && active.tagName === 'SELECT' && root.contains(active)) {
        if (!renderPending) {
            renderPending = true;
            active.addEventListener('blur', () => { renderPending = false; render(); }, { once: true });
        }
        return;
    }
    const logBody = document.getElementById('log-body');
    const stickToBottom = !logBody || logBody.scrollTop + logBody.clientHeight >= logBody.scrollHeight - 4;

    const banner = state.backendLost || state.backendWarning;
    root.innerHTML = `
    <div class="topbar">
      <h1>scriptstui</h1>
      <button class="tab ${state.tab === 'tunnels' ? 'active' : ''}" data-tab="tunnels">Túneles</button>
      <button class="tab ${state.tab === 'sso' ? 'active' : ''}" data-tab="sso">Cuentas SSO</button>
      ${banner ? `<span class="banner">${escapeHtml(banner)}</span>` : '<span class="shared-note">misma sesión que la terminal</span>'}
    </div>
    <div class="view">
      ${state.tab === 'tunnels' ? renderTunnelsView() : renderSSOView()}
    </div>
    ${renderWizard()}
    ${renderVerify()}
  `;
    const newLogBody = document.getElementById('log-body');
    if (newLogBody && stickToBottom) newLogBody.scrollTop = newLogBody.scrollHeight;
    bindEvents();
}

function bindEvents() {
    root.querySelectorAll('[data-tab]').forEach((el) => {
        el.addEventListener('click', () => { state.tab = el.dataset.tab; render(); });
    });

    root.querySelectorAll('[data-select-service]').forEach((el) => {
        el.addEventListener('click', (e) => {
            if (e.target.closest('button, select')) return;
            selectService(el.dataset.selectService);
            render();
        });
    });

    const on = (id, fn) => {
        const el = root.querySelector('#' + id);
        if (el) el.addEventListener('click', fn);
    };
    on('btn-verify', openVerify);
    on('vf-begin', beginVerify);
    on('vf-close', closeVerify);
    on('vf-again', openVerify);
    on('vf-help', () => { state.verify.help = true; render(); });
    on('vf-help-close', () => { state.verify.help = false; render(); });
    root.querySelectorAll('[data-verify-mode]').forEach((el) => {
        el.addEventListener('click', () => { state.verify.parallel = el.dataset.verifyMode === 'parallel'; render(); });
        el.addEventListener('dblclick', beginVerify);
    });
    root.querySelectorAll('[data-start]').forEach((el) => {
        el.addEventListener('click', () => startService(el.dataset.start));
    });
    root.querySelectorAll('[data-stop]').forEach((el) => {
        el.addEventListener('click', () => stopService(el.dataset.stop));
    });
    root.querySelectorAll('[data-copy]').forEach((el) => {
        el.addEventListener('click', () => copyCommand(el.dataset.copy));
    });
    root.querySelectorAll('[data-assign]').forEach((el) => {
        el.addEventListener('change', () => assignProfile(el.dataset.assign, el.value));
    });

    root.querySelectorAll('[data-login]').forEach((el) => {
        el.addEventListener('click', () => loginSSO(el.dataset.login));
    });
    const logoutBtn = root.querySelector('#btn-logout-sso');
    if (logoutBtn) logoutBtn.addEventListener('click', logoutSSO);
    const newBtn = root.querySelector('#btn-new-sso');
    if (newBtn) newBtn.addEventListener('click', openWizard);

    const wzCancel = root.querySelector('#wz-cancel');
    if (wzCancel) wzCancel.addEventListener('click', closeWizard);
    const wzSubmit = root.querySelector('#wz-submit');
    if (wzSubmit) wzSubmit.addEventListener('click', () => {
        submitWizardForm(
            root.querySelector('#wz-url').value.trim(),
            root.querySelector('#wz-region').value.trim(),
        );
    });
    root.querySelectorAll('[data-account]').forEach((el) => {
        el.addEventListener('click', () => pickAccount(el.dataset.account, el.dataset.accountName));
    });
    root.querySelectorAll('[data-role]').forEach((el) => {
        el.addEventListener('click', () => pickRole(el.dataset.role));
    });
}

// Same keys as the terminal UI: t opens Verificar, ? explains it, esc closes.
document.addEventListener('keydown', (e) => {
    if (e.target.closest('input, select, textarea')) return;
    const v = state.verify;
    if (e.key === 'Escape') {
        if (v && v.help) { v.help = false; render(); } else if (v) closeVerify();
    } else if (e.key === 't' && !v && !state.wizard && state.tab === 'tunnels') {
        openVerify();
    } else if (e.key === '?' && v) {
        v.help = !v.help;
        render();
    } else if (v && v.choosing && !v.help) {
        if (e.key === '1') { v.parallel = false; beginVerify(); }
        if (e.key === '2') { v.parallel = true; beginVerify(); }
        if (e.key === 'ArrowUp' || e.key === 'ArrowDown') { v.parallel = !v.parallel; render(); }
        if (e.key === 'Enter') beginVerify();
    }
});

App.BackendWarning().then((w) => { state.backendWarning = w || ''; render(); });
render();
refreshServices();
refreshProfiles();
refreshSSOSummary();
setInterval(refreshProfiles, 15000);
setInterval(refreshSSOSummary, 60000);
setInterval(tickLive, 1000);
requestAnimationFrame(drawWave);
