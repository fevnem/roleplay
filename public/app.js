// roleplay — frontend logic. Vanilla JS, zero deps, streaming chat via fetch/SSE.
(() => {
  'use strict';

  // ── Session ─────────────────────────────────────────
  let sid = localStorage.getItem('rp.sid');
  if (!sid) { sid = crypto.randomUUID?.() ?? Math.random().toString(36).slice(2); localStorage.setItem('rp.sid', sid); }

  const $ = (id) => document.getElementById(id);
  const els = {
    landing: $('landing'), cards: $('cards'),
    chat: $('chat'), thread: $('thread'),
    back: $('back'), chatAvatar: $('chat-avatar'), chatName: $('chat-name'),
    presenceLabel: $('presence-label'), presenceDot: $('presence-dot'), infoBtn: $('info-btn'),
    text: $('text'), send: $('send'),
    modal: $('info-modal'), modalAvatar: $('modal-avatar'), modalName: $('modal-name'),
    modalLang: $('modal-lang'), modalPersonality: $('modal-personality'), modalBackstory: $('modal-backstory'),
    modalClose: $('modal-close'), toast: $('toast'),
  };

  let catalog = [];       // all characters [{name,language,personality,greeting,avatar,accent,temperature}]
  let active = null;      // currently chatting character (public object)
  let busy = false;       // a reply is streaming

  // ── Theme a character ────────────────────────────────
  const THEMABLE = { '--accent': true, '--accent-soft': true };
  function theme(c) {
    const accent = c.accent || '#7c8bff';
    const root = document.documentElement.style;
    root.setProperty('--accent', accent);
    root.setProperty('--accent-soft', hexToRgba(accent, 0.16));
  }
  function hexToRgba(hex, a) {
    const m = hex.replace('#', '');
    const n = m.length === 3 ? m.split('').map(x => x + x).join('') : m;
    const r = parseInt(n.slice(0, 2), 16), g = parseInt(n.slice(2, 4), 16), b = parseInt(n.slice(4, 6), 16);
    return `rgba(${r},${g},${b},${a})`;
  }
  const esc = (s) => String(s).replace(/[&<>"']/g, c => ({ '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;' }[c]));

  // ── Toast ────────────────────────────────────────────
  let toastTimer;
  function toast(msg) {
    els.toast.textContent = msg;
    els.toast.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { els.toast.hidden = true; }, 4200);
  }

  // ── Landing: render character cards ─────────────────
  function renderCards() {
    els.cards.innerHTML = '';
    if (!catalog.length) { els.cards.innerHTML = '<p class="footnote">No characters found.</p>'; return; }
    for (const c of catalog) {
      const card = document.createElement('button');
      card.className = 'card';
      card.style.setProperty('--card-accent', c.accent || '#7c8bff');
      card.innerHTML =
        `<span class="card-avatar">${esc(c.avatar || '✨')}</span>` +
        `<span class="card-name">${esc(c.name)} <span class="badge">${esc(c.language)}</span></span>` +
        `<span class="card-pers">${esc(c.personality)}</span>` +
        `<span class="card-greet">“${esc(c.greeting)}”</span>`;
      card.onclick = () => openChat(c);
      els.cards.appendChild(card);
    }
  }

  // ── Navigation ───────────────────────────────────────
  function showLanding() {
    els.chat.hidden = true; els.landing.hidden = false;
    history.pushState({ view: 'landing' }, '');
  }
  function openChat(c) {
    active = c;
    els.landing.hidden = true; els.chat.hidden = false;
    els.thread.innerHTML = '';
    els.chatAvatar.textContent = c.avatar || '✨';
    els.chatName.textContent = c.name;
    els.presenceLabel.textContent = 'Online';
    theme(c);
    history.pushState({ view: 'chat' }, '');
    els.text.focus();
    loadHistory();
  }

  // ── Rendering messages ───────────────────────────────
  function bubble(obj, text, elClass) {
    const wrap = document.createElement('div');
    wrap.className = 'msg ' + (elClass || 'bot');
    const av = document.createElement('div');
    av.className = 'avatar'; av.textContent = obj.avatar || '✨';
    const bub = document.createElement('div');
    bub.className = 'bubble'; bub.textContent = text;
    wrap.appendChild(av); wrap.appendChild(bub);
    els.thread.appendChild(wrap);
    scrollBottom();
    return bub;
  }
  function typingBubble() {
    const wrap = document.createElement('div');
    wrap.className = 'msg bot typing';
    const av = document.createElement('div'); av.className = 'avatar'; av.textContent = active.avatar || '✨';
    const bub = document.createElement('div'); bub.className = 'bubble';
    bub.innerHTML = '<i></i><i></i><i></i>';
    wrap.appendChild(av); wrap.appendChild(bub);
    els.thread.appendChild(wrap);
    scrollBottom();
    return wrap;
  }
  function scrollBottom() {
    els.thread.scrollTop = els.thread.scrollHeight;
  }

  async function loadHistory() {
    try {
      const r = await fetch('/api/history?session=' + encodeURIComponent(sid));
      const h = await r.json();
      for (const m of (h.messages || [])) {
        bubble(m.role === 'user' ? { avatar: '🙂' } : active, m.content, m.role === 'user' ? 'user' : 'bot');
      }
    } catch { /* no history yet — fine */ }
  }

  // ── Sending + streaming ──────────────────────────────
  function setSendEnabled() {
    els.send.disabled = busy || !els.text.value.trim();
  }

  async function ask() {
    const text = els.text.value.trim();
    if (!text || busy) return;
    els.text.value = ''; setSendEnabled();
    bubble({ avatar: '🙂' }, text, 'user');

    const pending = typingBubble();
    busy = true; setSendEnabled();
    let emitted = false;

    try {
      const res = await fetch('/api/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ session: sid, text, character: active.name.toLowerCase() }),
      });
      if (!res.ok) throw new Error('HTTP ' + res.status);

      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = '', reply = '';
      const bub = pending.querySelector('.bubble');

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        let idx;
        while ((idx = buf.indexOf('\n\n')) >= 0) {
          const ev = buf.slice(0, idx); buf = buf.slice(idx + 2);
          const m = ev.match(/^data: (.*)$/m);
          if (!m) continue;
          const d = JSON.parse(m[1]);
          if (d.error != null) { throw new Error(d.error); }
          if (d.delta != null) {
            if (!emitted) { pending.classList.remove('typing'); bub.textContent = ''; emitted = true; }
            reply += d.delta;
            bub.textContent = reply;
            scrollBottom();
          }
        }
      }
      if (!emitted) { pending.remove(); toast('No reply received'); }
    } catch (e) {
      pending.remove();
      toast('⚠ ' + e.message);
    } finally {
      busy = false; setSendEnabled();
    }
  }

  // ── Character info modal ─────────────────────────────
  function showModal(c) {
    els.modalAvatar.textContent = c.avatar || '✨';
    els.modalName.textContent = c.name;
    els.modalLang.textContent = c.language;
    els.modalPersonality.textContent = (c.personality || '') + '.';
    // backstory isn't served by the API (kept private to preserve the character);
    // show a tasteful stand-in note instead of leaking the prompt.
    els.modalBackstory.textContent = 'In-character · keeps nothing about this chat outside this session.';
    theme(c);
    els.modal.hidden = false;
  }

  // ── Init ─────────────────────────────────────────────
  async function init() {
    try {
      const r = await fetch('/api/characters');
      const d = await r.json();
      catalog = d.characters || [];
    } catch { catalog = []; toast('Failed to load characters'); }
    renderCards();

    // Enter to chat, shift+enter newline
    els.send.onclick = ask;
    els.text.addEventListener('input', setSendEnabled);
    els.text.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); ask(); }
    });
    els.back.onclick = showLanding;
    els.infoBtn.onclick = () => active && showModal(active);
    els.modalClose.onclick = () => { els.modal.hidden = true; };
    els.modal.onclick = (e) => { if (e.target === els.modal) els.modal.hidden = true; };
    window.addEventListener('popstate', () => {
      if (location.pathname.endsWith('/')) { /* root */ }
    });

    setSendEnabled();
  }

  document.addEventListener('DOMContentLoaded', init);
})();