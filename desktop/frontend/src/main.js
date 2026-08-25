import './style.css';

import * as App from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime/runtime';

const MAX_LOG_LINES = 500;

const state = {
    tab: 'tunnels',
    services: [],
    serviceStatus: {},
    serviceLogs: {},
    selectedServiceId: null,

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
        state.services = list;
        for (const s of list) {
            if (!(s.id in state.serviceStatus)) state.serviceStatus[s.id] = 'detenido';
        }
        render();
    });
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
    if (ev.kind === 'status') {
        state.serviceStatus[ev.serviceId] = ev.status;
    } else {
        pushLog(state.serviceLogs, ev.serviceId, `[${ev.label}] ${ev.text}`);
    }
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

function statusBadgeClass(status) {
    switch (status) {
        case 'activo': return 'ok';
        case 'iniciando':
        case 'deteniendo': return 'starting';
        case 'falló': return 'failed';
        default: return 'off';
    }
}

function renderTunnelsView() {
    const rows = state.services.map((s) => {
        const status = state.serviceStatus[s.id] || 'detenido';
        const selected = s.id === state.selectedServiceId ? ' selected' : '';
        const options = ['<option value="">(sin perfil)</option>']
            .concat(state.profiles.map((p) => `<option value="${escapeHtml(p.name)}" ${p.name === s.profile ? 'selected' : ''}>${escapeHtml(p.name)}</option>`));
        return `
      <div class="row${selected}" data-select-service="${escapeHtml(s.id)}">
        <div class="row-top">
          <span class="row-title">${escapeHtml(s.title)}</span>
          <span class="badge ${statusBadgeClass(status)}">${escapeHtml(status)}</span>
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

    const logs = state.selectedServiceId
        ? (state.serviceLogs[state.selectedServiceId] || []).join('\n')
        : '';

    return `
    <div class="panel list-panel">
      <div class="panel-header"><span>Túneles</span></div>
      ${rows}
    </div>
    <div class="panel log-panel">
      <div class="panel-header"><span>Logs${state.selectedServiceId ? ' — ' + escapeHtml(state.selectedServiceId) : ''}</span></div>
      <div class="log-body">${escapeHtml(logs) || '<span class="empty">Selecciona un túnel para ver sus logs.</span>'}</div>
    </div>`;
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

function render() {
    root.innerHTML = `
    <div class="topbar">
      <h1>scriptstui</h1>
      <button class="tab ${state.tab === 'tunnels' ? 'active' : ''}" data-tab="tunnels">Túneles</button>
      <button class="tab ${state.tab === 'sso' ? 'active' : ''}" data-tab="sso">Cuentas SSO</button>
    </div>
    <div class="view">
      ${state.tab === 'tunnels' ? renderTunnelsView() : renderSSOView()}
    </div>
    ${renderWizard()}
  `;
    bindEvents();
}

function bindEvents() {
    root.querySelectorAll('[data-tab]').forEach((el) => {
        el.addEventListener('click', () => { state.tab = el.dataset.tab; render(); });
    });

    root.querySelectorAll('[data-select-service]').forEach((el) => {
        el.addEventListener('click', (e) => {
            if (e.target.closest('button, select')) return;
            state.selectedServiceId = el.dataset.selectService;
            render();
        });
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

render();
refreshServices();
refreshProfiles();
setInterval(refreshProfiles, 15000);
