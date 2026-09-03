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
    presenceLabel: $('presence-label'), infoBtn: $('info-btn'),
    text: $('text'), send: $('send'),
    modal: $('info-modal'), modalAvatar: $('modal-avatar'), modalName: $('modal-name'),
    modalLang: $('modal-lang'), modalPersonality: $('modal-personality'), modalBackstory: $('modal-backstory'),
    modalClose: $('modal-close'), toast: $('toast'),
  };

  let catalog = [];
  let active = null;
  let busy = false;

  const FALLBACK_ACCENT = '#d4a24a';

  // Original anime portrait per character (name-slug → /img/<slug>.svg)
  function imgSrc(c) { return '/img/' + String(c.name || '').toLowerCase() + '.svg'; }
  function portrait(c) {
    const img = document.createElement('img');
    img.src = imgSrc(c);
    img.alt = c.name || '';
    return img;
  }

  function hexToRgba(hex, a) {
    const m = String(hex || '').replace('#', '');
    if (!m) return null;
    const n = m.length === 3 ? m.split('').map(x => x + x).join('') : m;
    const r = parseInt(n.slice(0, 2), 16), g = parseInt(n.slice(2, 4), 16), b = parseInt(n.slice(4, 6), 16);
    return (r + g + b) ? `rgba(${r},${g},${b},${a})` : null;
  }
  void hexToRgba; // reserved

  // Accent applied via CSS custom property (bounded hex from server data), never HTML.
  function theme(c) {
    const accent = (c && c.accent) || FALLBACK_ACCENT;
    document.documentElement.style.setProperty('--accent', accent);
  }

  // ── Toast ────────────────────────────────────────────
  let toastTimer;
  function toast(msg) {
    els.toast.textContent = msg;
    els.toast.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { els.toast.hidden = true; }, 4200);
  }

  // ── Landing: the cast — KEPT AS A GRID (per user) ────
  function renderCards() {
    els.cards.innerHTML = '';
    if (!catalog.length) {
      els.cards.innerHTML = '<p class="cast-empty">No characters are available yet. Check back soon.</p>';
      return;
    }
    const count = $('cast-count');
    if (count) count.textContent = String(catalog.length).padStart(2, '0');

    catalog.forEach((c, i) => {
      const card = document.createElement('button');
      card.type = 'button';
      card.className = 'char';
      card.style.setProperty('--c', c.accent || FALLBACK_ACCENT);
      card.setAttribute('aria-label', 'Start a chat with ' + c.name);

      const art = document.createElement('span'); art.className = 'char-art';
      const img = portrait(c); img.loading = 'lazy'; art.appendChild(img);

      const info = document.createElement('span'); info.className = 'char-info';
      const line = document.createElement('span'); line.className = 'char-line';
      const name = document.createElement('span'); name.className = 'char-name'; name.textContent = c.name;
      const lang = document.createElement('span'); lang.className = 'char-lang'; lang.textContent = c.language;
      line.append(name, lang);
      const pers = document.createElement('span'); pers.className = 'char-pers'; pers.textContent = c.personality;
      const greet = document.createElement('span'); greet.className = 'char-greet'; greet.textContent = '“' + (c.greeting || '') + '”';
      info.append(line, pers, greet);

      const open = document.createElement('span'); open.className = 'char-open';
      open.innerHTML = '<span>Open chat</span> <span class="co-arrow" aria-hidden="true">→</span>';

      card.append(art, info, open);
      card.onclick = () => openChat(c);
      els.cards.appendChild(card);
      observeReveal(card);
    });
  }

  // reveal-on-scroll, gated behind IntersectionObserver availability
  let revealObserver = null;
  function observeReveal(el) {
    if (!('IntersectionObserver' in window)) { return; }
    document.documentElement.classList.add('io');
    if (!revealObserver) {
      revealObserver = new IntersectionObserver((entries) => {
        for (const en of entries) {
          if (en.isIntersecting) {
            en.target.classList.add('in');
            revealObserver.unobserve(en.target);
          }
        }
      }, { threshold: 0.15, rootMargin: '0px 0px -6% 0px' });
    }
    revealObserver.observe(el);
  }

  // ── Navigation ───────────────────────────────────────
  function showLanding() {
    active = null;
    document.documentElement.style.setProperty('--accent', FALLBACK_ACCENT);
    els.chat.hidden = true; els.landing.hidden = false;
    window.history.pushState({ view: 'landing' }, '');
  }
  function openChat(c) { setChatView(c, true); }

  function setChatView(c, push) {
    active = c;
    theme(c);
    els.landing.hidden = true; els.chat.hidden = false;
    els.thread.innerHTML = '';
    els.chatAvatar.innerHTML = '';
    els.chatAvatar.appendChild(portrait(c));
    els.chatName.textContent = c.name;
    els.presenceLabel.textContent = 'Online';
    if (push) window.history.pushState({ view: 'chat', character: c.name.toLowerCase() }, '');
    els.text.focus();
    loadHistory();
  }

  // ── Rendering messages ───────────────────────────────
  function bubble(obj, text, elClass) {
    const wrap = document.createElement('div');
    wrap.className = 'msg ' + (elClass || 'bot');
    const av = document.createElement('div'); av.className = 'avatar';
    av.appendChild(portrait(active));
    const bub = document.createElement('div'); bub.className = 'bubble'; bub.textContent = text;
    wrap.appendChild(av); wrap.appendChild(bub);
    els.thread.appendChild(wrap);
    scrollBottom();
    return bub;
  }
  function typingBubble() {
    const wrap = document.createElement('div');
    wrap.className = 'msg bot typing';
    const av = document.createElement('div'); av.className = 'avatar';
    av.appendChild(portrait(active));
    const bub = document.createElement('div'); bub.className = 'bubble';
    bub.innerHTML = '<i></i><i></i><i></i>';
    wrap.appendChild(av); wrap.appendChild(bub);
    els.thread.appendChild(wrap);
    scrollBottom();
    return wrap;
  }
  function scrollBottom() { els.thread.scrollTop = els.thread.scrollHeight; }

  async function loadHistory() {
    try {
      const r = await fetch('/api/history?session=' + encodeURIComponent(sid));
      const h = await r.json();
      for (const m of (h.messages || [])) {
        bubble(null, m.content, m.role === 'user' ? 'user' : 'bot');
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
    bubble(null, text, 'user');

    const pending = typingBubble();
    busy = true; setSendEnabled();
    let emitted = false, caretEl = null;

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
            if (!emitted) {
              pending.classList.remove('typing');
              bub.textContent = '';
              caretEl = document.createElement('span'); caretEl.className = 'caret';
              bub.appendChild(caretEl);
              emitted = true;
            }
            reply += d.delta;
            caretEl.insertAdjacentText('beforebegin', d.delta);
            scrollBottom();
          }
        }
      }
      if (emitted && caretEl) caretEl.remove();
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
    els.modalAvatar.innerHTML = '';
    els.modalAvatar.appendChild(portrait(c));
    els.modalName.textContent = c.name;
    els.modalLang.textContent = c.language;
    els.modalPersonality.textContent = (c.personality || '') + '.';
    els.modalBackstory.textContent = 'In-character · nothing leaves this session.';
    theme(c);
    els.modal.hidden = false;
  }

  // ── Init ─────────────────────────────────────────────
  async function init() {
    els.cards.innerHTML = '';
    const li = document.createElement('div'); li.className = 'loader'; li.setAttribute('aria-hidden', 'true');
    els.cards.appendChild(li);
    try {
      const r = await fetch('/api/characters');
      const d = await r.json();
      catalog = d.characters || [];
    } catch { catalog = []; toast('Failed to load characters'); }
    renderCards();

    els.send.onclick = ask;
    els.text.addEventListener('input', setSendEnabled);
    els.text.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); ask(); }
    });
    els.back.onclick = () => showLanding();
    els.infoBtn.onclick = () => active && showModal(active);
    els.modalClose.onclick = () => { els.modal.hidden = true; };
    els.modal.onclick = (e) => { if (e.target === els.modal) els.modal.hidden = true; };
    window.addEventListener('popstate', (e) => {
      const view = e.state && e.state.view;
      if (view === 'chat') {
        const name = e.state.character;
        const c = catalog.find(x => x.name.toLowerCase() === name) || catalog[0];
        if (c) setChatView(c, false);
      } else {
        els.chat.hidden = true; els.landing.hidden = false; active = null;
      }
    });

    setSendEnabled();
  }

  document.addEventListener('DOMContentLoaded', init);
})();