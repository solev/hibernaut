// Tiny story runner for hero illustrations: cancellable, pausable waits, typing, bot helpers.
const STOP = Symbol('stop');
export const reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
export const motion = { paused: false };

export function setPaused(p) {
  motion.paused = p;
  for (const b of document.querySelectorAll('hib-bot')) b.toggleAttribute('paused', p);
}

export function story(fn, { loop = true, gap = 1600 } = {}) {
  let token = 0;
  async function start() {
    const my = ++token;
    const check = () => { if (my !== token) throw STOP; };
    const s = {
      check,
      wait(ms) {
        let left = reduced ? 0 : ms;
        return new Promise((res, rej) => {
          const tick = () => {
            if (my !== token) return rej(STOP);
            if (motion.paused) return setTimeout(tick, 120);
            if (left <= 0) return res();
            const step = Math.min(left, 120);
            left -= step;
            setTimeout(tick, step);
          };
          tick();
        });
      },
      async type(el, text, speed = 55) {
        el.textContent = '';
        for (const ch of text) {
          el.textContent += ch;
          await s.wait(speed + Math.random() * 40);
        }
      },
      bot(b, state) { check(); b.setAttribute('state', state); },
      settled(b) {
        if (reduced) return Promise.resolve();
        return new Promise((res) => b.addEventListener('settled', res, { once: true })).then(check);
      },
    };
    try {
      do {
        await fn(s);
        if (loop && !reduced) await s.wait(gap);
      } while (loop && !reduced && my === token);
    } catch (e) {
      if (e !== STOP) throw e;
    }
  }
  start();
  return { replay: start };
}

export const $ = (sel, root = document) => root.querySelector(sel);
export const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

if (window !== top) document.documentElement.classList.add('embed');
