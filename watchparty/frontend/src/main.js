// main.js — Watch Party frontend
// Communicates with the Go backend via Wails auto-generated bindings.

import { CreateRoom, JoinRoom, LeaveRoom, Play, Pause, Seek,
         GetPlaybackState, GetRoomState, TransferControl,
         SetSignalerURL, GetSignalerURL, CheckAndInstallMPV, SetStreamURL } from '../wailsjs/go/main/App.js';
import { EventsOn } from '../wailsjs/runtime/runtime.js';

// ─────────────────────────────────────────────────────────────────────────────
// State
// ─────────────────────────────────────────────────────────────────────────────
const state = {
  inRoom: false,
  isHost: false,
  roomId: '',
  selfId: '',
  streamUrl: '',
  peers: [],
  paused: true,
  position: 0,
  positionInterval: null,
  peerInterval: null,
};

// ─────────────────────────────────────────────────────────────────────────────
// DOM helpers
// ─────────────────────────────────────────────────────────────────────────────
const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => document.querySelectorAll(sel);

function escapeHtml(str) {
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function showView(id) {
  $$('.view').forEach(v => v.classList.remove('active'));
  $(`#view-${id}`)?.classList.add('active');
}

function setLoading(btn, loading) {
  btn.disabled = loading;
  if (loading) {
    btn._originalText = btn.innerHTML;
    btn.innerHTML = '<span class="spinner"></span>';
  } else {
    btn.innerHTML = btn._originalText || btn.innerHTML;
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// Toast notifications
// ─────────────────────────────────────────────────────────────────────────────
function toast(message, type = 'info', duration = 4000) {
  const icons = { info: '💬', success: '✅', error: '❌' };
  const container = $('#toast-container');
  const el = document.createElement('div');
  el.className = `toast ${type}`;
  const iconSpan = document.createElement('span');
  iconSpan.className = 'toast-icon';
  iconSpan.textContent = icons[type];
  const msgSpan = document.createElement('span');
  msgSpan.textContent = message;
  el.appendChild(iconSpan);
  el.appendChild(msgSpan);
  container.appendChild(el);
  setTimeout(() => { el.style.opacity = '0'; el.style.transform = 'translateX(20px)'; el.style.transition = 'all 0.3s'; setTimeout(() => el.remove(), 300); }, duration);
}

// ─────────────────────────────────────────────────────────────────────────────
// Time formatter
// ─────────────────────────────────────────────────────────────────────────────
function formatTime(seconds) {
  if (!seconds || isNaN(seconds)) return '0:00:00';
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  return `${h}:${String(m).padStart(2,'0')}:${String(s).padStart(2,'0')}`;
}

// ─────────────────────────────────────────────────────────────────────────────
// Copy to clipboard
// ─────────────────────────────────────────────────────────────────────────────
async function copyToClipboard(text) {
  try {
    await navigator.clipboard.writeText(text);
    toast('Código copiado al portapapeles', 'success', 2000);
  } catch {
    toast('No se pudo copiar', 'error');
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// Render peers list
// ─────────────────────────────────────────────────────────────────────────────
function renderPeers() {
  const countEl = $('#peer-count');
  if (countEl) countEl.textContent = state.peers.length;

  const list = $('#peers-list');
  if (!list) return;

  if (state.peers.length === 0) {
    list.innerHTML = '<div class="empty-peers">👥 Esperando peers...</div>';
    return;
  }

  const myPos = state.position;
  list.innerHTML = state.peers.map(peer => {
    const drift = Math.abs(peer.position - myPos);
    const syncClass = drift < 2 ? 'synced' : drift < 5 ? 'drifting' : 'lost';
    const isHost = peer.role === 'host';
    const avatarClass = isHost ? 'host-avatar' : 'peer-avatar-item';
    const safeId = escapeHtml(peer.id);
    const safeTruncId = escapeHtml(peer.id.slice(0,8));
    const initial = escapeHtml(peer.id.charAt(0).toUpperCase());

    const transferBtn = state.isHost && peer.role !== 'host'
      ? `<button class="btn transfer-btn" data-peer-id="${safeId}">🎮 Control</button>`
      : '';

    return `
      <div class="peer-item">
        <div class="peer-avatar ${avatarClass}">${initial}</div>
        <div class="peer-info">
          <div class="peer-id">${isHost ? '👑 ' : ''}${safeTruncId}...</div>
          <div class="peer-pos">${formatTime(peer.position)}</div>
        </div>
        ${transferBtn}
        <div class="sync-dot ${syncClass}" title="Drift: ${drift.toFixed(1)}s"></div>
      </div>`;
  }).join('');

  // Attach transfer button handlers via data attributes (prevents XSS)
  list.querySelectorAll('.transfer-btn').forEach(btn => {
    btn.addEventListener('click', () => window._transferControl(btn.dataset.peerId));
  });
}

// ─────────────────────────────────────────────────────────────────────────────
// Update room view from state
// ─────────────────────────────────────────────────────────────────────────────
function updateRoomUI() {
  // Room badge
  const badge = $('#room-id-badge');
  if (badge) badge.textContent = state.roomId;

  const copyInput = $('#room-code-input');
  if (copyInput) copyInput.value = state.roomId;

  // Role badge
  const roleBadge = $('#role-badge');
  if (roleBadge) {
    roleBadge.textContent = state.isHost ? 'Host 👑' : 'Peer';
    roleBadge.className = `role-badge ${state.isHost ? 'host' : 'peer'}`;
  }

  // Stream URL
  const urlEl = $('#stream-url-display');
  if (urlEl) urlEl.textContent = state.streamUrl || 'Recibiendo URL del host...';

  // Host URL Panel
  const hostPanel = $('#host-url-panel');
  if (hostPanel) {
    hostPanel.style.display = state.isHost ? 'block' : 'none';
  }

  // Playback controls enabled/disabled
  const ctrlButtons = $$('.playback-btn');
  ctrlButtons.forEach(btn => {
    btn.disabled = !state.isHost;
    btn.title = state.isHost ? '' : 'Solo el host puede controlar la reproducción';
  });

  // Position
  const posEl = $('#position-display');
  if (posEl) posEl.textContent = formatTime(state.position);

  // Pause/Play button icon
  const playBtn = $('#btn-play-pause');
  if (playBtn) {
    playBtn.innerHTML = state.paused ? '▶ Reproducir' : '⏸ Pausar';
  }

  renderPeers();
}

// ─────────────────────────────────────────────────────────────────────────────
// Enter room
// ─────────────────────────────────────────────────────────────────────────────
function enterRoom(roomId, isHost, streamUrl) {
  state.inRoom  = true;
  state.roomId  = roomId;
  state.isHost  = isHost;
  state.streamUrl = streamUrl || '';

  showView('room');
  updateRoomUI();

  // Poll playback state every 1s (fallback for events)
  if (state.positionInterval) clearInterval(state.positionInterval);
  state.positionInterval = setInterval(async () => {
    try {
      const ps = await GetPlaybackState();
      state.paused   = ps.paused;
      state.position = ps.position;
      updateRoomUI();
    } catch { /* mpv might not be running yet */ }
  }, 1000);

  // Sync peers state periodically
  if (state.peerInterval) clearInterval(state.peerInterval);
  state.peerInterval = setInterval(async () => {
    try {
      const rs = await GetRoomState();
      state.peers = rs.peers || [];
      state.selfId = rs.selfId;
      renderPeers();
    } catch {}
  }, 2000);
}

// ─────────────────────────────────────────────────────────────────────────────
// Wails event listeners
// ─────────────────────────────────────────────────────────────────────────────
function setupEvents() {
	EventsOn('room:state', (room) => {
	  state.isHost = room.isHost;
	  state.peers = room.peers || [];
	  state.selfId = room.selfId;
	  state.streamUrl = room.streamUrl;
	  updateRoomUI();
	});
  EventsOn('peer:joined', (peer) => {
    toast(`👤 Peer conectado: ${peer.id.slice(0,8)}...`, 'info');
    state.peers = [...state.peers.filter(p => p.id !== peer.id), peer];
    renderPeers();
  });

  EventsOn('peer:left', (peerId) => {
    toast(`👤 Peer desconectado: ${String(peerId).slice(0,8)}...`, 'info');
    state.peers = state.peers.filter(p => p.id !== peerId);
    renderPeers();
  });

  EventsOn('peer:position', (data) => {
    const peer = state.peers.find(p => p.id === data.peerId);
    if (peer) { peer.position = data.position; renderPeers(); }
  });

  EventsOn('playback:state', (ps) => {
    state.paused   = ps.paused;
    state.position = ps.position;
    updateRoomUI();
  });

  EventsOn('playback:position', (pos) => {
    state.position = pos;
    const posEl = $('#position-display');
    if (posEl) posEl.textContent = formatTime(pos);
  });

  EventsOn('stream:received', (url) => {
    state.streamUrl = url;
    toast('🎬 Stream recibido del host — lanzando mpv...', 'success');
    updateRoomUI();
  });

  EventsOn('room:left', () => {
    state.inRoom = false;
    state.peers = [];
    if (state.positionInterval) clearInterval(state.positionInterval);
    if (state.peerInterval) clearInterval(state.peerInterval);
    showView('landing');
    toast('Has salido de la sala', 'info');
  });

  EventsOn('control:transferred', (peerId) => {
    toast(`🎮 Control transferido a ${String(peerId).slice(0,8)}...`, 'success');
    state.isHost = false;
    updateRoomUI();
  });

  EventsOn('error', (msg) => {
    toast(`Error: ${msg}`, 'error', 6000);
  });
}

// ─────────────────────────────────────────────────────────────────────────────
// Build UI
// ─────────────────────────────────────────────────────────────────────────────
function buildUI() {
  const app = document.getElementById('app');
  app.innerHTML = `
    <!-- Toast container -->
    <div class="toast-container" id="toast-container"></div>

    <!-- ══════════════════ LANDING VIEW ══════════════════ -->
    <div class="view active" id="view-landing">
      <div class="logo">
        <div class="logo-icon">🎬</div>
        <div class="logo-text">
          <h1>Watch Party</h1>
          <p>Sincronización P2P de alta precisión</p>
        </div>
      </div>

      <div class="cards-row">
        <!-- Create room -->
        <div class="card">
          <div class="card-header">
            <div class="card-icon host">🏠</div>
            <span class="card-title">Crear sala</span>
          </div>
          <p class="card-desc">Crea una sala y comparte el código. Podrás poner el video una vez dentro.</p>

          <div class="form-group">
            <label>Contraseña de la sala</label>
            <input type="password" id="input-host-password" placeholder="Mínimo 4 caracteres" />
          </div>
          <button class="btn btn-primary" id="btn-create-room">
            🏠 Crear sala
          </button>
        </div>

        <div class="divider-v"></div>

        <!-- Join room -->
        <div class="card">
          <div class="card-header">
            <div class="card-icon join">🔗</div>
            <span class="card-title">Unirse a sala</span>
          </div>
          <p class="card-desc">Ingresa el código de sala que te compartió el host y la contraseña.</p>

          <div class="form-group">
            <label>Código de sala</label>
            <input type="text" id="input-room-code" placeholder="Ej: AB3X7K" maxlength="6"
              style="text-transform:uppercase;letter-spacing:3px;font-size:18px;font-weight:700;text-align:center;" />
          </div>
          <div class="form-group">
            <label>Contraseña</label>
            <input type="password" id="input-join-password" placeholder="Contraseña de la sala" />
          </div>
          <button class="btn btn-primary" id="btn-join-room">
            🔗 Unirse
          </button>

          <!-- Signaler settings -->
          <details style="margin-top:20px;">
            <summary style="font-size:12px;color:var(--text-muted);cursor:pointer;list-style:none;">⚙️ Configuración del signaler</summary>
            <div style="margin-top:12px;">
              <label>URL del servidor de señalización</label>
              <div class="copy-group">
                <input type="text" id="input-signaler-url" placeholder="wss://..." />
                <button class="btn btn-secondary" id="btn-save-signaler" style="width:auto;padding:10px 14px;">Guardar</button>
              </div>
            </div>
          </details>
        </div>
      </div>
    </div>

    <!-- ══════════════════ ROOM VIEW ══════════════════ -->
    <div class="view" id="view-room">
      <div class="room-layout">

        <!-- Top bar -->
        <header class="topbar">
          <span class="topbar-logo">🎬 Watch Party</span>
          <div class="room-badge">
            <span class="dot"></span>
            <span id="room-id-badge">---</span>
          </div>
          <div class="copy-group" style="flex:0;">
            <input type="text" id="room-code-input" readonly style="width:120px;text-align:center;font-weight:700;letter-spacing:2px;" />
            <button class="btn btn-secondary" style="width:auto;padding:10px 12px;" onclick="copyToClipboard(document.getElementById('room-code-input').value)">📋</button>
          </div>
          <div class="topbar-spacer"></div>
          <span class="role-badge host" id="role-badge">Host 👑</span>
          <button class="btn btn-danger" style="width:auto;" id="btn-leave">Salir</button>
        </header>

        <!-- Main content -->
        <main class="room-main">
          <div class="controls-panel">
            <div id="host-url-panel" style="display:none; margin-bottom: 24px; background: rgba(255,255,255,0.03); padding: 16px; border-radius: 8px; border: 1px solid rgba(255,255,255,0.1);">
              <h3 style="margin-bottom: 12px; font-size: 14px;">📡 Configurar Video</h3>
              <div class="form-group" style="margin-bottom: 12px;">
                <input type="text" id="input-room-stream-url" placeholder="" style="width: 100%;" />
              </div>
              <button class="btn btn-primary" id="btn-set-url" onclick="window._setStreamUrl()">▶ Iniciar Transmisión</button>
            </div>

            <h2>🎮 Controles de reproducción</h2>

            <div class="position-display">
              <div>
                <div class="label">Posición actual</div>
                <div class="time" id="position-display">0:00:00</div>
              </div>
            </div>

            <div class="playback-controls">
              <button class="btn btn-primary playback-btn" id="btn-play-pause" onclick="window._togglePlayPause()">
                ▶ Reproducir
              </button>
              <button class="btn btn-secondary playback-btn" id="btn-seek-back"
                onclick="window._seekRelative(-10)" title="Retroceder 10s">
                ⏪ 10s
              </button>
              <button class="btn btn-secondary playback-btn" id="btn-seek-fwd"
                onclick="window._seekRelative(+10)" title="Adelantar 10s">
                10s ⏩
              </button>
            </div>

            <div class="stream-url-box">
              <div class="url-label">Stream URL</div>
              <div class="url-text" id="stream-url-display">Cargando...</div>
            </div>
          </div>
        </main>

        <!-- Sidebar: peers -->
        <aside class="room-sidebar">
          <div class="sidebar-section">
            <h3>👥 Participantes (<span id="peer-count">0</span>)</h3>
            <div class="peers-list" id="peers-list">
              <div class="empty-peers">Esperando peers...</div>
            </div>
          </div>

          <div class="settings-section">
            <h3 style="font-size:12px;font-weight:600;text-transform:uppercase;letter-spacing:0.5px;color:var(--text-muted);margin-bottom:12px;">ℹ️ Info</h3>
            <div style="font-size:12px;color:var(--text-muted);line-height:1.7;">
              <p>• mpv se abre en una ventana separada</p>
              <p>• Cada usuario recibe la transmisión directamente</p>
              <p>• La sincronización es automática (±2s)</p>
              <p style="margin-top:8px;">Solo el host controla play/pause/seek</p>
            </div>
          </div>
        </aside>

      </div>
    </div>

    <!-- ══════════════════ INSTALLER OVERLAY ══════════════════ -->
    <div id="installer-overlay" style="display:none; position:fixed; inset:0; background:rgba(10,11,16,0.95); z-index:9999; flex-direction:column; align-items:center; justify-content:center; backdrop-filter:blur(10px);">
      <div class="spinner" style="width:40px; height:40px; margin-bottom:20px;"></div>
      <h2 style="font-size:18px; margin-bottom:8px;">Instalando Dependencias</h2>
      <p id="installer-status" style="color:var(--text-muted); font-size:14px;">Iniciando...</p>
    </div>
  `;

  // Expose copyToClipboard globally for onclick attrs
  window.copyToClipboard = copyToClipboard;

  // Expose SetStreamURL handler globally
  window._setStreamUrl = async () => {
    const url = $('#input-room-stream-url').value.trim();
    if (!url) {
      toast('Ingresa un link válido', 'error');
      return;
    }
    const btn = $('#btn-set-url');
    setLoading(btn, true);
    try {
      await SetStreamURL(url);
      state.streamUrl = url;
      toast('Transmisión iniciada', 'success');
      updateRoomUI();
    } catch (err) {
      toast(`Error al iniciar video: ${err}`, 'error');
    } finally {
      setLoading(btn, false);
    }
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// Wire up event handlers
// ─────────────────────────────────────────────────────────────────────────────
function wireHandlers() {
  // ── Create room ──
  $('#btn-create-room').addEventListener('click', async () => {
    const pass = $('#input-host-password').value;

    if (pass.length < 4) { toast('La contraseña debe tener al menos 4 caracteres', 'error'); return; }

    const btn = $('#btn-create-room');
    setLoading(btn, true);

    try {
      const roomId = await CreateRoom(pass);
      state.selfId = '';
      enterRoom(roomId, true, '');
      toast(`Sala creada: ${roomId} 🎉`, 'success');
    } catch (err) {
      toast(`Error al crear sala: ${err}`, 'error', 6000);
    } finally {
      setLoading(btn, false);
    }
  });

  // ── Join room ──
  $('#btn-join-room').addEventListener('click', async () => {
    const code = $('#input-room-code').value.trim().toUpperCase();
    const pass = $('#input-join-password').value;

    if (code.length < 4) { toast('Ingresa el código de sala', 'error'); return; }
    if (pass.length < 4) { toast('La contraseña debe tener al menos 4 caracteres', 'error'); return; }

    const btn = $('#btn-join-room');
    setLoading(btn, true);

    try {
      await JoinRoom(code, pass);
      enterRoom(code, false, '');
      toast(`Conectando a la sala ${code}...`, 'info');
    } catch (err) {
      toast(`Error al unirse: ${err}`, 'error', 6000);
    } finally {
      setLoading(btn, false);
    }
  });

  // ── Leave room ──
  $('#btn-leave')?.addEventListener('click', async () => {
    await LeaveRoom();
    state.inRoom = false;
    state.peers = [];
    if (state.positionInterval) clearInterval(state.positionInterval);
    if (state.peerInterval) clearInterval(state.peerInterval);
    showView('landing');
  });

  // ── Save signaler URL ──
  $('#btn-save-signaler')?.addEventListener('click', async () => {
    const url = $('#input-signaler-url').value.trim();
    if (!url) return;
    try {
      await SetSignalerURL(url);
      toast('URL del signaler guardada', 'success', 2000);
    } catch (err) {
      toast(`Error: ${err}`, 'error');
    }
  });

  // Load current signaler URL
  GetSignalerURL().then(url => {
    const el = $('#input-signaler-url');
    if (el) el.value = url;
  }).catch(() => {});

  // Auto-uppercase room code input
  $('#input-room-code')?.addEventListener('input', (e) => {
    e.target.value = e.target.value.toUpperCase().replace(/[^A-Z0-9]/g, '');
  });
}

// ─────────────────────────────────────────────────────────────────────────────
// Playback control helpers (used by onclick attrs)
// ─────────────────────────────────────────────────────────────────────────────
window._togglePlayPause = async () => {
  if (!state.isHost) return;
  try {
    if (state.paused) {
      await Play();
      state.paused = false;
    } else {
      await Pause();
      state.paused = true;
    }
    updateRoomUI();
  } catch (err) {
    toast(`Error: ${err}`, 'error');
  }
};

window._seekRelative = async (delta) => {
  if (!state.isHost) return;
  try {
    const newPos = Math.max(0, state.position + delta);
    await Seek(newPos);
    state.position = newPos;
    updateRoomUI();
  } catch (err) {
    toast(`Error: ${err}`, 'error');
  }
};

window._transferControl = async (peerId) => {
  try {
    await TransferControl(peerId);
    toast(`Control transferido a ${peerId.slice(0,8)}...`, 'success');
  } catch (err) {
    toast(`Error: ${err}`, 'error');
  }
};

// ─────────────────────────────────────────────────────────────────────────────
// Init
// ─────────────────────────────────────────────────────────────────────────────
document.addEventListener('DOMContentLoaded', async () => {
  buildUI();
  wireHandlers();
  setupEvents();
  
  // Setup installer events
  EventsOn('installer:progress', (status) => {
    const el = $('#installer-status');
    if (el) el.textContent = status;
  });

  const overlay = $('#installer-overlay');
  overlay.style.display = 'flex';

  try {
    await CheckAndInstallMPV();
    overlay.style.display = 'none';
    showView('landing');
  } catch (err) {
    $('#installer-status').textContent = `Error instalando mpv: ${err}`;
    $('#installer-status').style.color = 'var(--red-400)';
    overlay.querySelector('.spinner').style.display = 'none';
  }
});
