// Minimal single-page chat. No deps: fetch streaming for live replies.
let sid = localStorage.getItem('sid') || (crypto.randomUUID?.() ?? Math.random().toString(36).slice(2));
localStorage.setItem('sid', sid);

const chat = document.getElementById('chat');
const text = document.getElementById('text');
const sendBtn = document.getElementById('send');
const sel = document.getElementById('char');
const toast = document.getElementById('toast');
const charName = document.getElementById('char-name');

let greetingEl = null;
let currentChar = 'aarav';

const add = (cls, html) => {
  const d = document.createElement('div');
  d.className = 'bubble ' + cls;
  if (html != null) d.textContent = html;
  chat.appendChild(d);
  chat.scrollTop = chat.scrollHeight;
  return d;
};
const showToast = (m) => { toast.textContent = m; toast.hidden = false; setTimeout(() => toast.hidden = true, 4000); };
const setCharHeader = (name) => { charName.textContent = name; };

// load character catalog + current session context
Promise.all([
  fetch('/api/characters').then(r => r.json()),
  fetch('/api/history?session=' + encodeURIComponent(sid)).then(r => r.json()).catch(() => ({})),
]).then(([cat, h]) => {
  (cat.characters || []).forEach(c => {
    const o = document.createElement('option');
    o.value = c.name.toLowerCase();
    o.textContent = c.name + ' — ' + c.personality;
    sel.appendChild(o);
  });
  if (h.character) { sel.value = h.character; currentChar = h.character; }
  const chosen = (cat.characters || []).find(c => c.name.toLowerCase() === currentChar);
  if (chosen) { setCharHeader(chosen.name); greetingEl = add('bot', chosen.greeting); }
  else { greetingEl = add('bot', 'Hey! pick a character and say hi 👋'); }
  (h.messages || []).forEach(m => add(m.role === 'user' ? 'user' : 'bot', m.content));
}).catch(() => showToast('failed to load characters'));

// character switch shows that character's intro immediately
sel.addEventListener('change', () => {
  currentChar = sel.value;
  const o = sel.selectedOptions[0];
  const name = (o.textContent || '').split(' — ')[0];
  setCharHeader(name);
  fetch('/api/characters').then(r => r.json()).then(cat => {
    const c = (cat.characters || []).find(x => x.name.toLowerCase() === currentChar);
    if (greetingEl && c) greetingEl.textContent = c.greeting;
  });
});

const ask = async () => {
  const t = text.value.trim();
  if (!t) return;
  text.value = '';
  add('user', t);
  const reply = add('bot');
  reply.classList.add('typing');
  let got = false;
  try {
    const res = await fetch('/api/chat', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session: sid, text: t, character: currentChar }),
    });
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const reader = res.body.getReader();
    const dec = new TextDecoder();
    let buf = '';
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      let i;
      while ((i = buf.indexOf('\n\n')) >= 0) {
        const ev = buf.slice(0, i); buf = buf.slice(i + 2);
        const m = ev.match(/^data: (.*)$/m);
        if (!m) continue;
        const d = JSON.parse(m[1]);
        if (d.delta != null) {
          if (!got) { reply.classList.remove('typing'); reply.textContent = ''; got = true; }
          reply.textContent += d.delta;
        }
        if (d.error) { reply.classList.remove('typing'); reply.textContent = ''; showToast('⚠ ' + d.error); return; }
        chat.scrollTop = chat.scrollHeight;
      }
    }
    if (!got) { reply.classList.remove('typing'); reply.textContent = ''; showToast('no reply received'); }
  } catch (e) {
    reply.classList.remove('typing'); reply.textContent = '';
    showToast('⚠ ' + e.message);
  }
};

sendBtn.onclick = ask;
text.addEventListener('keydown', e => { if (e.key === 'Enter') ask(); });