// The browser interface.
//
// Plain JavaScript on purpose: there is no build step anywhere in this project,
// and a file server's UI is not where a toolchain earns its keep. What it has to
// be is obvious — the people it is for do not mount SMB shares, so anything they
// cannot do here they cannot do at all.

'use strict';

const $ = (id) => document.getElementById(id);

const state = {
  user: null,
  path: '',      // path within the served tree, '' means the start screen
};

// --- API -------------------------------------------------------------------

async function api(method, url, body, opts = {}) {
  const init = { method, headers: {} };
  if (body !== undefined && body !== null) {
    if (body instanceof Blob || body instanceof File) {
      init.body = body;
    } else {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
  }
  const res = await fetch(url, init);
  if (res.status === 401 && !opts.allow401) {
    showLogin();
    throw new Error('로그인이 필요합니다.');
  }
  if (!res.ok) {
    let msg = `요청이 실패했습니다 (${res.status})`;
    try {
      const j = await res.json();
      if (j && j.error) msg = j.error;
    } catch (_) { /* not JSON */ }
    throw new Error(translate(msg, res.status));
  }
  if (res.status === 204) return null;
  const type = res.headers.get('Content-Type') || '';
  return type.includes('json') ? res.json() : res.text();
}

// The server answers with the kernel's verdict; these are the ones a person can
// actually do something about.
function translate(msg, status) {
  switch (status) {
    case 403: return '권한이 없습니다. 이 폴더는 다른 사람의 것이거나 소속되지 않은 팀입니다.';
    case 404: return '찾을 수 없습니다. 이미 지워졌거나 이름이 바뀌었을 수 있습니다.';
    case 409: return '같은 이름이 이미 있습니다.';
    case 507: return '저장 공간이 부족합니다.';
    case 503: return '지금 로그인을 확인할 수 없습니다. 관리자에게 알려주세요.';
    default: return msg;
  }
}

const filesURL = (p) => '/api/files/' + p.split('/').map(encodeURIComponent).join('/');

// --- helpers ---------------------------------------------------------------

function fmtSize(n) {
  if (!n) return '';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i === 0 ? n : n.toFixed(n < 10 ? 1 : 0)) + ' ' + u[i];
}

function fmtDate(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const now = new Date();
  const sameYear = d.getFullYear() === now.getFullYear();
  return d.toLocaleDateString('ko-KR', {
    year: sameYear ? undefined : 'numeric', month: 'numeric', day: 'numeric',
  }) + ' ' + d.toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' });
}

function iconFor(entry) {
  if (entry.dir) return entry.name === '.trash' ? '🗑️' : '📁';
  const ext = entry.name.split('.').pop().toLowerCase();
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'heic'].includes(ext)) return '🖼️';
  if (['mp4', 'mov', 'avi', 'mkv', 'webm'].includes(ext)) return '🎬';
  if (['mp3', 'wav', 'flac', 'm4a'].includes(ext)) return '🎵';
  if (['pdf'].includes(ext)) return '📕';
  if (['zip', 'tar', 'gz', '7z', 'rar'].includes(ext)) return '🗜️';
  if (['xlsx', 'xls', 'csv'].includes(ext)) return '📊';
  if (['docx', 'doc', 'hwp', 'txt', 'md'].includes(ext)) return '📄';
  return '📄';
}

function showError(msg) {
  const el = $('app-error');
  el.textContent = msg;
  el.hidden = !msg;
  if (msg) setTimeout(() => { if (el.textContent === msg) el.hidden = true; }, 8000);
}

// --- screens ---------------------------------------------------------------

function showLogin() {
  state.user = null;
  $('login').hidden = false;
  $('app').hidden = true;
}

async function showApp(user) {
  state.user = user;
  $('login').hidden = true;
  $('app').hidden = false;
  $('who').textContent = user;
  await navigate(pathFromLocation());
}

function pathFromLocation() {
  const p = decodeURIComponent(location.pathname).replace(/^\/+/, '');
  return p === '' || p.startsWith('api/') || p.startsWith('s/') ? '' : p;
}

async function navigate(p, push = false) {
  state.path = p;
  if (push) history.pushState({}, '', '/' + p.split('/').map(encodeURIComponent).join('/'));
  renderCrumbs();
  if (p === '') return renderStart();
  await renderListing();
}

// --- start screen ----------------------------------------------------------

function renderStart() {
  $('toolbar').hidden = true;
  const main = $('listing');
  main.replaceChildren();
  main.append(
    row({ name: '내 폴더', dir: true }, '🏠', () => navigate('homes/' + state.user, true), true),
    row({ name: '팀 폴더', dir: true }, '👥', () => navigate('teams', true), true),
  );
}

// --- listing ---------------------------------------------------------------

async function renderListing() {
  $('toolbar').hidden = false;
  const main = $('listing');
  main.replaceChildren(el('p', { class: 'empty' }, '불러오는 중…'));
  let data;
  try {
    data = await api('GET', filesURL(state.path));
  } catch (e) {
    main.replaceChildren(el('p', { class: 'empty error' }, e.message));
    return;
  }
  if (typeof data === 'string' || !data.entries) {
    // A file, not a directory: nothing to list, so just fetch it.
    location.href = filesURL(state.path);
    return;
  }

  main.replaceChildren();
  if (data.entries.length === 0) {
    main.append(el('p', { class: 'empty' }, '비어 있습니다.'));
    return;
  }
  // Folders first, then by name — the order people expect from a file manager.
  data.entries.sort((a, b) =>
    (a.dir === b.dir) ? a.name.localeCompare(b.name, 'ko') : (a.dir ? -1 : 1));

  for (const e of data.entries) {
    const child = state.path + '/' + e.name;
    main.append(row(e, iconFor(e),
      () => e.dir ? navigate(child, true) : (location.href = filesURL(child))));
  }
}

function row(entry, icon, onOpen, plain = false) {
  const actions = el('div', { class: 'actions' });
  if (!plain) {
    if (!entry.dir) {
      actions.append(button('🔗', '공유 링크 만들기', (ev) => {
        ev.stopPropagation();
        openShareDialog(state.path + '/' + entry.name);
      }));
    }
    if (entry.name !== '.trash') {
      actions.append(button('🗑️', '휴지통으로 보내기', async (ev) => {
        ev.stopPropagation();
        const inTrash = state.path.endsWith('/.trash');
        const msg = inTrash
          ? `"${entry.name}"을(를) 완전히 지웁니다. 되돌릴 수 없습니다.`
          : `"${entry.name}"을(를) 휴지통으로 보냅니다.`;
        if (!confirm(msg)) return;
        try {
          await api('DELETE', filesURL(state.path + '/' + entry.name));
          await renderListing();
        } catch (err) { showError(err.message); }
      }));
    }
  }

  const node = el('div', { class: 'row', tabindex: '0', role: 'button' },
    el('span', { class: 'icon' }, icon),
    el('span', { class: 'name' }, entry.name),
    el('span', { class: 'meta' }, entry.dir ? '' : fmtSize(entry.size)),
    el('span', { class: 'meta' }, fmtDate(entry.mod_time)),
    actions,
  );
  node.addEventListener('click', onOpen);
  node.addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); onOpen(ev); }
  });
  return node;
}

function renderCrumbs() {
  const nav = $('crumbs');
  nav.replaceChildren();
  if (state.path === '') { nav.append(el('span', { class: 'current' }, '처음')); return; }

  const parts = state.path.split('/');
  const labels = parts.map((p, i) => {
    if (i === 0 && p === 'homes') return '내 폴더';
    if (i === 0 && p === 'teams') return '팀 폴더';
    if (i === 1 && parts[0] === 'homes') return null; // the user's own name adds nothing
    if (p === '.trash') return '휴지통';
    return p;
  });

  nav.append(button('처음', '처음으로', () => navigate('', true)));
  for (let i = 0; i < parts.length; i++) {
    if (labels[i] === null) continue;
    nav.append(el('span', { class: 'sep' }, '›'));
    const sub = parts.slice(0, i + 1).join('/');
    if (i === parts.length - 1) {
      nav.append(el('span', { class: 'current' }, labels[i]));
    } else {
      nav.append(button(labels[i], '', () => navigate(sub, true)));
    }
  }
}

// --- upload ----------------------------------------------------------------

async function uploadFiles(files) {
  if (!files.length) return;
  if (!state.path || state.path === 'teams' || state.path === 'homes') {
    showError('파일은 폴더 안에만 올릴 수 있습니다. 폴더를 먼저 여세요.');
    return;
  }
  const bar = $('progress-bar'), text = $('progress-text');
  $('progress').hidden = false;

  let done = 0;
  for (const f of files) {
    text.textContent = `${f.name} (${done + 1}/${files.length})`;
    bar.style.width = `${(done / files.length) * 100}%`;
    try {
      await api('PUT', filesURL(state.path + '/' + f.name), f);
    } catch (e) {
      showError(`${f.name}: ${e.message}`);
    }
    done++;
  }
  bar.style.width = '100%';
  setTimeout(() => { $('progress').hidden = true; bar.style.width = '0'; }, 400);
  await renderListing();
}

// --- share links -----------------------------------------------------------

let shareTargetPath = null;

function openShareDialog(p) {
  shareTargetPath = p;
  $('share-target').textContent = p.split('/').pop();
  $('share-password').value = '';
  $('share-dialog').showModal();
}

async function createShare() {
  try {
    const link = await api('POST', '/api/shares', {
      path: shareTargetPath,
      password: $('share-password').value,
      ttl_hours: Number($('share-ttl').value),
    });
    await copy(link.url);
    showError('');
    alert('링크를 복사했습니다.\n\n' + link.url +
      '\n\n' + new Date(link.expires).toLocaleDateString('ko-KR') + '까지 열립니다.');
  } catch (e) {
    showError(e.message);
  }
}

async function copy(text) {
  try {
    await navigator.clipboard.writeText(text);
  } catch (_) {
    // Clipboard access needs a secure context; over plain HTTP the dialog still
    // shows the URL to copy by hand.
  }
}

async function showShares() {
  const box = $('shares-list');
  box.replaceChildren(el('p', { class: 'muted' }, '불러오는 중…'));
  $('shares-dialog').showModal();
  let data;
  try {
    data = await api('GET', '/api/shares');
  } catch (e) {
    box.replaceChildren(el('p', { class: 'error' }, e.message));
    return;
  }
  if (!data.links.length) {
    box.replaceChildren(el('p', { class: 'muted' }, '만든 링크가 없습니다.'));
    return;
  }
  box.replaceChildren(...data.links.map((l) => {
    const url = el('input', { value: l.url, readonly: 'readonly' });
    return el('div', { class: 'share-item' },
      el('div', {}, l.name + (l.protected ? ' 🔒' : '')),
      el('div', { class: 'muted small' },
        new Date(l.expires).toLocaleDateString('ko-KR') + '까지'),
      el('div', { class: 'share-url' },
        url,
        button('복사', '', () => copy(l.url)),
        button('취소', '링크 폐기', async () => {
          if (!confirm('이 링크를 폐기합니다. 받은 사람은 더 이상 열 수 없습니다.')) return;
          try { await api('DELETE', '/api/shares/' + l.token); await showShares(); }
          catch (e) { showError(e.message); }
        }),
      ));
  }));
}

// --- tiny DOM helpers ------------------------------------------------------

function el(tag, attrs, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) n.setAttribute(k, v);
  for (const c of children) if (c !== null && c !== undefined && c !== '') n.append(c);
  return n;
}

function button(label, title, onClick) {
  const b = el('button', title ? { title, 'aria-label': title } : {}, label);
  b.addEventListener('click', onClick);
  return b;
}

// --- wiring ----------------------------------------------------------------

$('login-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const f = new FormData(ev.target);
  const err = $('login-error');
  err.hidden = true;
  try {
    const out = await api('POST', '/api/login',
      { user: f.get('user'), password: f.get('password') }, { allow401: true });
    await showApp(out.user);
  } catch (e) {
    err.textContent = e.message;
    err.hidden = false;
  }
});

$('logout-btn').addEventListener('click', async () => {
  try { await api('POST', '/api/logout'); } catch (_) { /* leaving anyway */ }
  showLogin();
});

$('home-btn').addEventListener('click', () => navigate('', true));
$('upload-btn').addEventListener('click', () => $('file-input').click());
$('file-input').addEventListener('change', (ev) => {
  uploadFiles([...ev.target.files]);
  ev.target.value = '';
});

$('mkdir-btn').addEventListener('click', async () => {
  const name = prompt('새 폴더 이름');
  if (!name) return;
  try {
    await api('POST', '/api/dirs/' +
      (state.path + '/' + name).split('/').map(encodeURIComponent).join('/'));
    await renderListing();
  } catch (e) { showError(e.message); }
});

$('trash-btn').addEventListener('click', () => {
  // The trash lives at the root of the permission domain, which is the first two
  // path segments: homes/<user> or teams/<team>.
  const parts = state.path.split('/');
  if (parts.length < 2) {
    showError('휴지통은 내 폴더나 팀 폴더 안에서 볼 수 있습니다.');
    return;
  }
  navigate(parts.slice(0, 2).join('/') + '/.trash', true);
});

$('shares-btn').addEventListener('click', showShares);
$('shares-close').addEventListener('click', () => $('shares-dialog').close());
$('share-create').addEventListener('click', () => setTimeout(createShare, 0));

// Drag and drop, which is how most people will actually upload.
let dragDepth = 0;
document.addEventListener('dragenter', (ev) => {
  if (state.user && state.path) { ev.preventDefault(); dragDepth++; $('drop-hint').hidden = false; }
});
document.addEventListener('dragleave', () => {
  if (--dragDepth <= 0) { dragDepth = 0; $('drop-hint').hidden = true; }
});
document.addEventListener('dragover', (ev) => { if (state.user) ev.preventDefault(); });
document.addEventListener('drop', (ev) => {
  if (!state.user) return;
  ev.preventDefault();
  dragDepth = 0;
  $('drop-hint').hidden = true;
  uploadFiles([...ev.dataTransfer.files]);
});

window.addEventListener('popstate', () => navigate(pathFromLocation()));

// Start: ask who we are. A live session goes straight in.
(async () => {
  try {
    const me = await api('GET', '/api/whoami', null, { allow401: true });
    await showApp(me.user);
  } catch (_) {
    showLogin();
  }
})();
