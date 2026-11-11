// TelegramWidget runtime for the debug chat (Widget SDK v1). The widget runs
// in a sandboxed iframe with no network; requests go to the parent chat page
// through postMessage, which forwards them to the bot as widget queries.
// Init is replaced by the server with the widget's state.
(function (init) {
  'use strict';
  const post = (m) => parent.postMessage(Object.assign({ tgw: 1 }, m), '*');
  const handlers = {};
  const events = ['themeChanged', 'activated', 'deactivated', 'chatDataChanged', 'publicDataChanged', 'contentSafeAreaChanged'];
  let chatData = init.chatData, publicData = init.publicData;
  let pending = null, seq = 0, tapUntil = 0, lastBackground = 0;

  class WidgetRequestError extends Error {
    constructor(code) { super(code); this.name = 'WidgetRequestError'; this.code = code; }
  }
  const reject = (code) => Promise.reject(new WidgetRequestError(code));
  const fire = (type) => (handlers[type] || []).slice().forEach((h) => {
    try { h.call(W); } catch (e) { console.error(e); }
  });
  const bytes = (s) => new TextEncoder().encode(s).length;
  // A tap unlocks one request (or link/copy action) for a short moment.
  const takeTap = () => { const ok = Date.now() < tapUntil; tapUntil = 0; return ok; };
  const checkURL = (url, telegram) => {
    let u;
    try { u = new URL(url); } catch (e) { throw new TypeError('invalid URL'); }
    const tg = /^(t\.me|telegram\.me|telegram\.dog)$/.test(u.hostname);
    if (typeof url !== 'string' || url.length > 256 || u.protocol !== 'https:' || u.username || u.password || tg !== telegram) {
      throw new TypeError('URL not allowed');
    }
  };

  const theme = Object.freeze({
    bg_color: '#ffffff', text_color: '#000000', hint_color: '#999999', link_color: '#2481cc',
    button_color: '#2481cc', button_text_color: '#ffffff', secondary_bg_color: '#efeff3',
  });
  for (const [k, v] of Object.entries(theme)) {
    document.documentElement.style.setProperty('--tg-theme-' + k.replace(/_/g, '-'), v);
  }
  const haptics = {
    impactOccurred() { return haptics; },
    notificationOccurred() { return haptics; },
    selectionChanged() { return haptics; },
  };
  const inset = Object.freeze({ top: 0, right: 0, bottom: 0, left: 0, cornerLeft: 0, cornerRight: 0 });

  const W = {
    isProfileAtLeast: (n) => typeof n === 'number' && 1 >= n,
    onEvent(type, h) {
      if (!events.includes(type) || typeof h !== 'function') return;
      const list = handlers[type] || (handlers[type] = []);
      if (list.length < 16 && !list.includes(h)) list.push(h);
    },
    offEvent(type, h) {
      const list = handlers[type];
      if (list && list.includes(h)) list.splice(list.indexOf(h), 1);
    },
    ready() { post({ type: 'ready' }); },
    openLink(url) { checkURL(url, false); if (takeTap()) post({ type: 'open', url }); },
    openTelegramLink(url) { checkURL(url, true); if (takeTap()) post({ type: 'open', url }); },
    copyText(text) {
      if (typeof text !== 'string' || text.length < 1 || text.length > 256) throw new TypeError('text must be 1-256 characters');
      if (takeTap()) post({ type: 'copy', text });
    },
    HapticFeedback: haptics,
    setResizeAnchor(edge) { if (edge !== 'top' && edge !== 'bottom') throw new TypeError('edge must be top or bottom'); },
    watchPublicData() {},
    unwatchPublicData() {},
    request(data, options) {
      if (typeof data !== 'string' || bytes(data) < 1 || bytes(data) > 64) throw new TypeError('data must be 1-64 bytes');
      if (options !== undefined && (options === null || typeof options !== 'object')) throw new TypeError('options must be an object');
      if (options && options.wallet) return reject('NOT_ALLOWED');
      if (!init.canQuery) return reject('NOT_ALLOWED');
      if (pending) return reject('BUSY');
      // Without a tap, the client asks at most once per 5 s while visible.
      if (!takeTap()) {
        if (Date.now() - lastBackground < 5000) return reject('RATE_LIMITED');
        lastBackground = Date.now();
      }
      return new Promise((resolve, rejectRequest) => {
        pending = { id: ++seq, resolve, reject: rejectRequest };
        post({ type: 'request', id: pending.id, data });
      });
    },
  };
  Object.defineProperties(W, {
    profile: { value: 1, enumerable: true },
    colorScheme: { value: 'light', enumerable: true },
    themeParams: { value: theme, enumerable: true },
    isActive: { get: () => !document.hidden, enumerable: true },
    contentSafeAreaInset: { value: inset, enumerable: true },
    chatData: { get: () => chatData, enumerable: true },
    publicData: { get: () => publicData, enumerable: true },
    isForward: { value: false, enumerable: true },
    chatType: { value: 'private', enumerable: true },
    viewer: { value: init.canQuery ? Object.freeze(init.viewer) : null, enumerable: true },
    isPreview: { value: false, enumerable: true },
    wallet: { value: null, enumerable: true },
  });
  window.TelegramWidget = Object.freeze(W);

  // Any press inside the widget counts as a tap for the next call.
  for (const type of ['pointerdown', 'click', 'keydown']) {
    window.addEventListener(type, () => { tapUntil = Date.now() + 1000; }, true);
  }
  document.addEventListener('visibilitychange', () => fire(document.hidden ? 'deactivated' : 'activated'));

  window.addEventListener('message', (e) => {
    const m = e.data;
    if (e.source !== parent || !m || !m.tgw) return;
    if (m.type === 'answer' && pending && pending.id === m.id) {
      const p = pending;
      pending = null;
      if (m.code) p.reject(new WidgetRequestError(m.code));
      else p.resolve(Object.freeze({ result: m.result === undefined ? null : m.result }));
    } else if (m.type === 'data') {
      const chatChanged = m.chatData !== chatData, publicChanged = m.publicData !== publicData;
      chatData = m.chatData;
      publicData = m.publicData;
      if (chatChanged) fire('chatDataChanged');
      if (publicChanged) fire('publicDataChanged');
    }
  });

  // The parent sizes the iframe to the content of a widget without a fixed height.
  const reportSize = () => {
    const b = document.body, s = getComputedStyle(b);
    post({ type: 'size', height: Math.ceil(b.offsetHeight + parseFloat(s.marginTop) + parseFloat(s.marginBottom)) });
  };
  document.addEventListener('DOMContentLoaded', () => {
    new ResizeObserver(reportSize).observe(document.body);
    reportSize();
  });
})(/*INIT*/);
