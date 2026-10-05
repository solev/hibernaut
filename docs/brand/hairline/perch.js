const {
  Cam, clamp, facing, fit, hull, lerp, open, poly, prism, proj, ringAt, rings, rrect, run, unproj,
  disposer, mk, place, pointer, put, register, solid, spring, stepS,
} = HL;

const PL = { x0: -84, y0: -36, x1: 82, y1: 40, z0: -8, z1: 0 };
const BOT = { x0: -58, y0: -25, x1: 2, y1: 35, z0: 0, h0: 51, h1: 60 };
const BC = [(BOT.x0 + BOT.x1) / 2, (BOT.y0 + BOT.y1) / 2];
const FLOOR = 24;

const wakeAt = (p, r) => {
  const d = Math.hypot(p[0] - BC[0], p[1] - BC[1]);
  const u = clamp((r - d) / Math.max(1, r - FLOOR), 0, 1);
  return u * u * (3 - 2 * u);
};

function mount({ stage, svg, read }, value) {
  const bag = disposer();
  const C = Cam(45, 0.5, 1.78);
  fit(C, [[PL.x0, PL.y0, PL.z0], [PL.x1, PL.y1, PL.z0], [PL.x1, PL.y0, 0], [PL.x0, PL.y1, 0], [BOT.x0 - 3, BOT.y0 - 3, BOT.z0], [BOT.x1 + 3, BOT.y1 + 5, BOT.h1 + 3]], 200, 166);
  const P = proj(C), front = facing(C);
  let radius = value, over = null;
  const wake = spring(0, { eps: 0.003 }), gaze = spring(0, { eps: 0.003 });

  const g = mk("g", {}, svg);
  const [pr, pi] = rings(PL.x0, PL.y0, PL.x1, PL.y1, 12, 2.2);
  put(solid(g), prism(P, front, pr, pi, PL.z0, PL.z1));

  const slotRing = rrect(16, -22, 64, 28, 9, 8);
  mk("path", { d: poly(ringAt(P, slotRing, 0.35)), class: "nf dash lo" }, g);

  const bot = solid(g);
  const eyes = [mk("path", { class: "nf" }, g), mk("path", { class: "nf" }, g)];
  const dots = [-18, -10, -2, 6].map((x) => mk("circle", { r: 1.45, class: "dot off" }, g));
  let last = "";

  function eyePath(cx, cy, ew, eh, er, x0, y1, z0, lean, h) {
    const rr = rrect(cx - ew / 2, cy - eh / 2, cx + ew / 2, cy + eh / 2, er, 10);
    return poly(rr.map((q) => P(x0 + q.u, y1 + lean * (q.v / h), z0 + q.v)));
  }

  function draw() {
    const w = clamp(wake.x, 0, 1);
    const h = lerp(BOT.h0, BOT.h1, w), settle = (1 - w) * 2.2, lean = (1 - w) * 2.4;
    const x0 = BOT.x0 - settle, x1 = BOT.x1 + settle, y0 = BOT.y0 - settle * 0.45, y1 = BOT.y1 + settle * 0.45;
    const r = 10.5 + w * 1.5, base = rrect(x0, y0, x1, y1, r, 14);
    const top = rrect(x0, y0 + lean, x1, y1 + lean, r, 14);
    const inner = rrect(x0 + 2.1, y0 + lean + 2.1, x1 - 2.1, y1 + lean - 2.1, r - 2.1, 14);
    const active = wake.t > 0.5, key = [w.toFixed(3), gaze.x.toFixed(3), wake.t.toFixed(2), over ? 1 : 0].join(",");
    if (key === last) return;
    last = key;
    put(bot, {
      sil: poly(hull(ringAt(P, base, BOT.z0).concat(ringAt(P, top, BOT.z0 + h)))),
      crease: open(ringAt(P, run(inner, front), BOT.z0 + h)),
    });
    bot.sil.classList.toggle("hi", active);
    const L = x1 - x0, ew = lerp(0.256, 0.13, w) * L, eh = lerp(0.068, 0.15, w) * L;
    const er = Math.min(0.034 * L, ew / 2, eh / 2);
    const shift = clamp(gaze.x * 3.8, -4, 4);
    const cy = lerp(0.448, 0.50, w) * L;
    eyes[0].setAttribute("d", eyePath(lerp(0.29, 0.32, w) * L + shift, cy, ew, eh, er, x0, y1, BOT.z0, lean, h));
    eyes[1].setAttribute("d", eyePath(lerp(0.71, 0.68, w) * L + shift, cy, ew, eh, er, x0, y1, BOT.z0, lean, h));
    eyes.forEach((e) => e.setAttribute("class", "nf" + (active ? " hi" : "")));
    dots.forEach((d, i) => {
      d.setAttribute("class", active ? (i === 0 ? "dot m" : "dot off") : (i === 0 ? "dot" : "dot off"));
      place(d, P(-18 + i * 8, PL.y1 + 0.25, -3.6));
    });
  }

  const B = register(stage, (dt) => {
    const moving = stepS(wake, dt) || stepS(gaze, dt);
    draw();
    return moving;
  });
  bag.add(B.unregister);

  function retarget() {
    if (!over) {
      wake.t = 0; gaze.t = 0; read.textContent = "rest";
    } else {
      wake.t = wakeAt(over, radius);
      gaze.t = clamp((over[0] - BC[0]) / 44, -1, 1) * wake.t;
      read.textContent = wake.t > 0.66 ? "awake" : wake.t > 0.08 ? "waking" : "asleep";
    }
    draw();
    B.wake();
  }

  draw();
  bag.add(pointer(stage, {
    move: (p) => { over = unproj(C, p[0], p[1], 0); retarget(); },
    leave: () => { over = null; retarget(); },
  }));
  bag.add(() => svg.replaceChildren());

  return {
    set: (v) => { radius = v; if (over) retarget(); },
    destroy: bag.dispose,
  };
}

hairline({
  name: "perch",
  means: "The machine sleeps until you come near; it wakes, rises and looks your way.",
  rules: [1, 3, 4, 5, 8, 9],
  range: [40, 80, 140],
  mount,
});
