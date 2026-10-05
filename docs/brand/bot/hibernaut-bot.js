// <hib-bot state="asleep|wake|idle|listening|thinking|working|happy|fork|hibernate" tight follow>
// Body = currentColor, effects = --hib-accent. Geometry matches docs/brand/svg/hibernaut-mark.svg.
// `follow`: eyes track the pointer while idle or listening. `bot.stir()` nudges a sleeping bot.
// `paused`: freezes the animation in place.

const NS = 'http://www.w3.org/2000/svg';
const BODY_D =
  'M13.3 0h73.4A13.3 13.3 0 0 1 100 13.3v73.4A13.3 13.3 0 0 1 86.7 100H13.3A13.3 13.3 0 0 1 0 86.7V13.3A13.3 13.3 0 0 1 13.3 0Z';
const EYE_R = 3.4;

const SHAPES = {
  asleep: { lx: 29, rx: 71, y: 55.2, w: 25.6, h: 6.8 },
  awake: { lx: 32, rx: 68, y: 50, w: 13, h: 15 },
  think: { lx: 34, rx: 66, y: 49, w: 9, h: 9 },
  focus: { lx: 32, rx: 68, y: 53, w: 14, h: 9 },
  happy: { lx: 32, rx: 68, y: 49, w: 13, h: 15 },
  squeeze: { lx: 29, rx: 71, y: 55.2, w: 27, h: 2.5 },
};

const TRANSIENT = { wake: 'idle', happy: 'idle', hibernate: 'asleep' };
const FOLLOWS = new Set(['idle', 'listening']);

// One shared pointer for every bot with `follow`.
const pointer = { x: 0, y: 0, at: -Infinity };
let pointerBound = false;
function bindPointer() {
  if (pointerBound) return;
  pointerBound = true;
  const track = (e) => { pointer.x = e.clientX; pointer.y = e.clientY; pointer.at = performance.now(); };
  addEventListener('pointermove', track, { passive: true });
  addEventListener('pointerdown', track, { passive: true });
}

const rand = (a, b) => a + Math.random() * (b - a);
const pick = (xs) => xs[Math.floor(Math.random() * xs.length)];
const easeOut = (k) => 1 - Math.pow(1 - k, 3);

function el(tag, attrs = {}) {
  const n = document.createElementNS(NS, tag);
  for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
  return n;
}

// Exponential approach toward a target; speed is 1/seconds.
class V {
  constructor(v) { this.v = v; this.t = v; this.s = 10; }
  to(t, s) { this.t = t; if (s) this.s = s; }
  snap() { this.v = this.t; }
  step(dt) { this.v += (this.t - this.v) * (1 - Math.exp(-this.s * dt)); }
}

let uid = 0;

class Body {
  constructor(parent, shape) {
    const id = `hib-bot-mask-${++uid}`;
    this.g = el('g');
    const mask = el('mask', { id, maskUnits: 'userSpaceOnUse', x: -20, y: -20, width: 140, height: 140 });
    this.rl = el('rect', { fill: '#000' });
    this.rr = el('rect', { fill: '#000' });
    const arc = { d: 'M-8 3.5Q0-6 8 3.5', fill: 'none', stroke: '#000', 'stroke-width': 4.6, 'stroke-linecap': 'round' };
    this.al = el('path', arc);
    this.ar = el('path', arc);
    mask.append(el('path', { d: BODY_D, fill: '#fff' }), this.rl, this.rr, this.al, this.ar);
    this.g.append(mask, el('path', { d: BODY_D, fill: 'currentColor', mask: `url(#${id})` }));
    parent.append(this.g);

    for (const k of ['tx', 'ty', 'rot', 'gx', 'gy', 'dl', 'dr', 'arc']) this[k] = new V(0);
    for (const k of ['sx', 'sy', 'op', 'lid']) this[k] = new V(1);
    this.lidBase = 1;
    this.eye = {};
    for (const k of ['lx', 'rx', 'y', 'w', 'h']) this.eye[k] = new V(shape[k]);
  }

  vals() {
    return [this.tx, this.ty, this.rot, this.gx, this.gy, this.dl, this.dr, this.arc,
      this.sx, this.sy, this.op, this.lid, ...Object.values(this.eye)];
  }

  shape(name, s = 10) { const sh = SHAPES[name]; for (const k in sh) this.eye[k].to(sh[k], s); }
  gaze(x, y, s = 9) { this.gx.to(x, s); this.gy.to(y, s); }
  pose(sx, sy, ty = 0, s = 10) { this.sx.to(sx, s); this.sy.to(sy, s); this.ty.to(ty, s); }

  step(dt) {
    for (const v of this.vals()) v.step(dt);
    const { tx, ty, rot, sx, sy } = this;
    this.g.setAttribute('transform',
      `translate(${tx.v} ${ty.v}) translate(50 100) rotate(${rot.v}) scale(${sx.v} ${sy.v}) translate(-50 -100)`);
    this.g.style.opacity = Math.max(0, Math.min(1, this.op.v));
    const e = this.eye;
    const w = Math.max(0.4, e.w.v);
    const arc = Math.max(0, Math.min(1, this.arc.v));
    const h = Math.max(0.4, e.h.v * this.lid.v * (1 - arc));
    const rx = Math.min(EYE_R, w / 2, h / 2);
    for (const [r, cx, dy] of [[this.rl, e.lx.v, this.dl.v], [this.rr, e.rx.v, this.dr.v]]) {
      r.setAttribute('x', cx + this.gx.v - w / 2);
      r.setAttribute('y', e.y.v + this.gy.v + dy - h / 2);
      r.setAttribute('width', w);
      r.setAttribute('height', h);
      r.setAttribute('rx', rx);
    }
    for (const [a, cx] of [[this.al, e.lx.v], [this.ar, e.rx.v]]) {
      a.setAttribute('transform', `translate(${cx + this.gx.v} ${e.y.v + this.gy.v}) scale(${0.6 + 0.4 * arc})`);
      a.style.opacity = arc;
    }
  }
}

const STATES = {
  asleep: {
    enter(b, m) {
      m.shape('asleep', 5); m.gaze(0, 0, 5); m.lidBase = 1; m.lid.to(1, 6);
      m.dl.to(0, 6); m.dr.to(0, 6); m.rot.to(0, 5); m.tx.to(0, 5);
      b.nextZ = 0.6;
    },
    tick(b, m, t) {
      const br = (1 - Math.cos((2 * Math.PI * t) / 4.2)) / 2;
      m.pose(1 + 0.014 * br, 1 - 0.03 * br, 0, 4);
      if (!b.tight && t >= b.nextZ) { b.spawnZ(); b.nextZ = t + 1.5; }
    },
  },

  wake: {
    enter(b, m) {
      m.lidBase = 1; m.lid.to(1, 10); m.gaze(0, 0);
      m.shape('squeeze', 14); m.pose(1.05, 0.92, 0, 12);
      b.at(0.32, () => { m.shape('awake', 16); m.pose(0.95, 1.08, -10, 16); });
      b.at(0.58, () => m.pose(1.03, 0.97, 0, 12));
      b.at(0.78, () => m.pose(1, 1, 0, 10));
      b.at(1.0, () => b.blink(m));
      b.at(1.6, () => b.settle());
    },
  },

  idle: {
    enter(b, m) {
      m.shape('awake', 10); m.lidBase = 1; m.lid.to(1, 10); m.dl.to(0); m.dr.to(0);
      m.pose(1, 1, 0, 8);
      b.nextBlink = rand(1.2, 2.5); b.nextGlance = rand(0.8, 2);
    },
    tick(b, m, t) {
      if (t >= b.nextBlink) {
        b.blink(m);
        if (Math.random() < 0.25) b.at(0.22, () => b.blink(m));
        b.nextBlink = t + rand(2.2, 5);
      }
      if (t >= b.nextGlance) {
        const [x, y] = pick([[0, 0], [0, 0], [-7, 0], [7, 0], [-6, -4], [6, -4], [0, 3]]);
        m.gaze(x, y, 14); m.tx.to(x * 0.35, 6); m.rot.to(x * 0.25, 6);
        b.nextGlance = t + rand(1.4, 3.6);
      }
      m.ty.to(Math.sin(t * 1.6) * 0.8, 6);
    },
  },

  listening: {
    enter(b, m) {
      m.shape('awake', 12); m.gaze(0, 0); m.tx.to(0); m.rot.to(0); m.lidBase = 1; m.lid.to(1);
      b.amp = new V(0); b.nextSyl = 0;
    },
    tick(b, m, t, dt) {
      if (t >= b.nextSyl) {
        b.amp.to(Math.random() < 0.22 ? 0 : rand(0.25, 1), 20);
        b.nextSyl = t + rand(0.09, 0.26);
      }
      b.amp.step(dt);
      const a = b.amp.v;
      m.eye.h.to(7 + 17 * a, 30); m.eye.w.to(13 - 2 * a, 30);
      m.pose(1 - 0.015 * a, 1 + 0.035 * a, 0, 20);
    },
  },

  thinking: {
    enter(b, m) {
      m.shape('think', 10); m.lidBase = 1; m.lid.to(1);
      m.gaze(6, -9, 8); m.tx.to(1.5, 6); m.rot.to(2.5, 6);
      b.nextGlance = 2.6;
    },
    tick(b, m, t) {
      const p = (2 * Math.PI * t) / 1.1;
      m.dl.to(-3.2 * Math.max(0, Math.sin(p)), 20);
      m.dr.to(-3.2 * Math.max(0, Math.sin(p - 1.2)), 20);
      if (t >= b.nextGlance) {
        const left = m.gx.t > 0;
        m.gaze(left ? -6 : 6, -9, 6); m.rot.to(left ? -2.5 : 2.5, 5);
        b.nextGlance = t + 2.6;
      }
    },
  },

  working: {
    enter(b, m) {
      m.shape('focus', 10); m.dl.to(0); m.dr.to(0); m.rot.to(0); m.tx.to(0);
      m.lidBase = 1; m.lid.to(1);
      b.col = 0; b.nextSaccade = 0.2; b.nextBlink = rand(2, 4);
    },
    tick(b, m, t) {
      if (t >= b.nextSaccade) {
        b.col = (b.col + 1) % 5;
        const back = b.col === 0;
        m.gaze(-8 + b.col * 4, back ? 7 : 5, back ? 10 : 24);
        b.nextSaccade = t + (back ? 0.5 : rand(0.22, 0.4));
      }
      if (t >= b.nextBlink) { b.blink(m); b.nextBlink = t + rand(2.5, 5); }
      m.ty.to(-Math.abs(Math.sin(t * 11)) * 1.2, 30);
    },
  },

  happy: {
    enter(b, m) {
      m.shape('happy', 14); m.arc.to(1, 14); m.gaze(0, 0, 12); m.dl.to(0); m.dr.to(0); m.rot.to(0); m.tx.to(0);
      m.lidBase = 1; m.lid.to(1);
      const hop = (d) => {
        b.at(d, () => m.pose(1.05, 0.93, 0, 16));
        b.at(d + 0.12, () => m.pose(0.96, 1.06, -12, 14));
        b.at(d + 0.32, () => m.pose(1.03, 0.97, 0, 14));
      };
      hop(0); hop(0.46);
      b.at(0.95, () => m.pose(1, 1, 0, 10));
      b.at(1.35, () => m.arc.to(0, 12));
      b.at(1.6, () => b.settle());
    },
  },

  fork: {
    enter(b, m) {
      m.shape('awake', 10); m.lidBase = 1; m.lid.to(1); m.dl.to(0); m.dr.to(0); m.rot.to(0);
      m.pose(1.06, 0.92, 0, 12);
      b.at(0.28, () => {
        const c = b.spawnClone();
        m.pose(1, 1, 0, 10); m.tx.to(b.tight ? 0 : -40, 7); m.gaze(7, 0, 8);
        c.tx.to(b.tight ? 0 : 52, 7); c.sx.to(0.78, 7); c.sy.to(0.78, 7); c.op.to(1, 8);
        b.at(0.45, () => c.gaze(-7, 0, 8));
      });
      b.at(1.4, () => { b.blink(m); if (b.clone) b.blink(b.clone); });
      b.at(2.2, () => { m.gaze(0, 0); if (b.clone) b.clone.gaze(0, 0); });
      b.nextBlink = 3.5;
    },
    tick(b, m, t) {
      if (t >= b.nextBlink) {
        b.blink(m);
        if (b.clone) b.at(rand(0.05, 0.3), () => b.clone && b.blink(b.clone));
        b.nextBlink = t + rand(2.2, 4);
      }
    },
  },

  hibernate: {
    enter(b, m) {
      m.shape('awake', 8); m.dl.to(0); m.dr.to(0); m.rot.to(0); m.tx.to(0);
      m.gaze(0, 3, 4); m.lidBase = 0.55; m.lid.to(0.55, 3);
      b.at(0.9, () => m.lid.to(0.08, 5));
      b.at(1.35, () => m.lid.to(0.5, 4));
      b.at(1.75, () => { b.snapshot(); m.pose(1.03, 0.96, 0, 8); });
      b.at(2.0, () => { m.shape('asleep', 4); m.lidBase = 1; m.lid.to(1, 4); m.gaze(0, 0, 4); });
      b.at(2.6, () => m.pose(1, 1, 0, 6));
      b.at(3.2, () => b.settle());
    },
  },
};

class HibBot extends HTMLElement {
  static get observedAttributes() { return ['state']; }

  connectedCallback() {
    if (this.svg) return;
    this.tight = this.hasAttribute('tight');
    this.reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
    this.svg = el('svg', {
      viewBox: this.tight ? '-6 -14 112 116' : '-45 -55 190 175',
      role: 'img',
      'aria-label': 'Hibernaut',
    });
    this.svg.style.cssText = 'display:block;width:100%;height:100%;overflow:visible';
    this.follow = this.hasAttribute('follow') && !this.reduced;
    if (this.follow) bindPointer();
    // Body center as a fraction of the element box, for pointer tracking.
    this.anchor = this.tight ? [56 / 112, 64 / 116] : [95 / 190, 105 / 175];
    this.visible = true;
    this.io = new IntersectionObserver(([e]) => { this.visible = e.isIntersecting; });
    this.io.observe(this);
    this.back = el('g'); this.mid = el('g'); this.front = el('g');
    this.svg.append(this.back, this.mid, this.front);
    this.append(this.svg);
    this.main = new Body(this.mid, SHAPES.asleep);
    this.fx = [];
    this.queue = [];
    this.time = 0;
    this.go(this.getAttribute('state') || 'asleep');
    this.main.vals().forEach((v) => v.snap());
    this.last = performance.now();
    const loop = (now) => {
      this.raf = requestAnimationFrame(loop);
      const dt = Math.min(0.05, (now - this.last) / 1000);
      this.last = now;
      if (this.visible && !this.hasAttribute('paused')) this.step(dt);
    };
    this.raf = requestAnimationFrame(loop);
  }

  disconnectedCallback() { cancelAnimationFrame(this.raf); this.io?.disconnect(); this.svg?.remove(); this.svg = null; }

  attributeChangedCallback(_, __, v) {
    if (this.svg && v && v !== this.current) this.go(v);
  }

  get state() { return this.current; }
  set state(v) { this.setAttribute('state', v); }

  go(name) {
    const st = STATES[name];
    if (!st) return;
    this.current = name;
    this.epoch = (this.epoch || 0) + 1;
    if (this.getAttribute('state') !== name) this.setAttribute('state', name);
    this.queue = [];
    this.stateStart = this.time;
    if (name !== 'fork') this.mergeClone();
    this.main.arc.to(0, 12);
    st.enter(this, this.main);
    this.dispatchEvent(new CustomEvent('statechange', { detail: name, bubbles: true }));
    if (this.reduced) {
      const q = this.queue; this.queue = [];
      q.sort((a, b) => a.at - b.at).forEach((x) => x.fn());
      if (this.current === name) this.main.vals().forEach((v) => v.snap());
    }
  }

  settle() {
    const next = TRANSIENT[this.current];
    this.dispatchEvent(new CustomEvent('settled', { detail: this.current, bubbles: true }));
    if (next) this.go(next);
  }

  at(delay, fn) { this.queue.push({ at: this.time + delay, fn }); }

  blink(body) {
    body.lid.to(0.06, 45);
    this.at(0.09, () => body.lid.to(body.lidBase, 22));
  }

  stir() {
    if (this.current !== 'asleep' || this.reduced) return;
    const m = this.main;
    m.pose(1.05, 0.93, 0, 18);
    this.at(0.14, () => m.pose(0.98, 1.03, -2, 14));
    this.at(0.32, () => m.pose(1, 1, 0, 8));
    if (!this.tight) this.spawnZ();
  }

  track() {
    const now = performance.now();
    if (now - pointer.at > 3500) return;
    if (!this.rect || now - this.rectAt > 400) { this.rect = this.getBoundingClientRect(); this.rectAt = now; }
    const r = this.rect;
    const sat = (d) => d / (Math.abs(d) + 240);
    const gx = 9 * sat(pointer.x - (r.left + r.width * this.anchor[0]));
    const gy = 6.5 * sat(pointer.y - (r.top + r.height * this.anchor[1]));
    const m = this.main;
    m.gaze(gx, gy, 12);
    if (this.current === 'idle') { m.tx.to(gx * 0.3, 6); m.rot.to(gx * 0.3, 6); }
  }

  spawnClone() {
    this.mergeClone(true);
    const c = new Body(this.back, SHAPES.awake);
    c.op.v = 0;
    this.clone = c;
    return c;
  }

  mergeClone(instant) {
    const c = this.clone;
    if (!c) return;
    this.clone = null;
    this.main.tx.to(0, 7);
    if (instant || this.reduced) { c.g.remove(); return; }
    c.tx.to(0, 8); c.sx.to(0.6, 8); c.sy.to(0.6, 8); c.op.to(0, 6);
    this.fx.push({ life: 0.7, born: this.time, update: () => c.step(this.dt), done: () => c.g.remove() });
  }

  spawnZ() {
    this.zi = ((this.zi ?? -1) + 1) % 3;
    const size = [6, 8, 10][this.zi];
    const r = el('rect', { width: size, height: size, rx: Math.min(EYE_R, size / 3) });
    r.style.fill = 'var(--hib-accent, #f3c77c)';
    r.style.opacity = 0;
    this.front.append(r);
    const drift = rand(-2, 2);
    this.fx.push({
      life: 3.2, born: this.time, node: r,
      update: (k) => {
        const s = 0.6 + 0.6 * easeOut(k);
        const x = 96 + 14 * k + Math.sin(k * 6 + drift) * 3;
        const y = 2 - 46 * easeOut(k);
        r.setAttribute('transform',
          `translate(${x} ${y}) rotate(${-12 + 24 * k}) scale(${s}) translate(${-size / 2} ${-size / 2})`);
        r.style.opacity = Math.min(1, k / 0.15) * (1 - Math.max(0, (k - 0.5) / 0.5));
      },
    });
  }

  snapshot() {
    if (this.reduced) return;
    for (const i of [1, 2]) {
      const p = el('path', { d: BODY_D, fill: 'none', 'stroke-width': 3 });
      p.style.stroke = 'var(--hib-accent, #f3c77c)';
      p.style.opacity = 0;
      this.back.append(p);
      this.fx.push({
        life: 1.3, born: this.time + i * 0.08, node: p,
        update: (k) => {
          const o = -9 * i * easeOut(k);
          p.setAttribute('transform', `translate(${o} ${o})`);
          p.style.opacity = Math.sin(Math.PI * k) * (i === 1 ? 0.9 : 0.55);
        },
      });
    }
  }

  step(dt) {
    this.dt = dt;
    this.time += dt;
    const due = this.queue.filter((q) => q.at <= this.time);
    if (due.length) {
      this.queue = this.queue.filter((q) => q.at > this.time);
      const epoch = this.epoch;
      for (const q of due) if (this.epoch === epoch) q.fn();
    }
    const st = STATES[this.current];
    if (st.tick && !this.reduced) st.tick(this, this.main, this.time - this.stateStart, dt);
    if (this.follow && FOLLOWS.has(this.current)) this.track();
    this.main.step(dt);
    if (this.clone) this.clone.step(dt);
    this.fx = this.fx.filter((f) => {
      const k = (this.time - f.born) / f.life;
      if (k < 0) return true;
      if (k >= 1) { f.node?.remove(); f.done?.(); return false; }
      f.update(k);
      return true;
    });
  }
}

customElements.define('hib-bot', HibBot);
