// Minimal single-page chat. No deps: fetch streaming for live replies.
let sid = localStorage.getItem('sid') || (crypto.randomUUID?.() ?? Math.random().toString(36).slice(2));
localStorage.setItem('sid', sid);

const chat = document.getElementById('chat');
const text = document.getElementById('text');
const send = document.getElementById('send');

const add = (cls, html) => {
  const d = document.createElement('div');
  d.className = 'bubble ' + cls;
  d.textContent = html;
  chat.appendChild(d);
  chat.scrollTop = chat.scrollHeight;
  return d;
};

// Load greeting + any recent history for this session
fetch('/api/history?session=' + encodeURIComponent(sid))
  .then(r => r.json())
  .then(h => {
    document.getElementById('char-name').textContent = h.character.name;
    document.getElementById('greeting').textContent = h.greeting || '';
    (h.messages || []).forEach(m => add(m.role === 'user' ? 'user' : 'bot', m.content));
  });

const ask = async () => {
  const t = text.value.trim();
  if (!t) return;
  text.value = '';
  add('user', t);
  const reply = add('bot', '');
  const res = await fetch('/api/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ session: sid, text: t }),
  });
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
      if (d.delta != null) reply.textContent += d.delta;
      if (d.error) { reply.textContent = '⚠ ' + d.error; return; }
      chat.scrollTop = chat.scrollHeight;
    }
  }
};

send.onclick = ask;
text.addEventListener('keydown', e => { if (e.key === 'Enter') ask(); });