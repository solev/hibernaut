/**
 * Checkpoint: big request tiles ride from an awake host bot, through one wall
 * gate, and leave as real site-bound secrets. Hovering slows the belt and lifts
 * the chosen tile so the swap can be read.
 */
const {
  Cam, clamp, facing, fit, prism, proj, rings, rrect, run,
  poly, open, seg, spring, stepS, mk, place, pointer, put, register, disposer, solid,
} = HL;

const SITES = ["github", "openai", "npm", "vercel"];
const PAT = [[0, 2, 3], [1, 2, 4], [0, 3, 5], [2, 4, 5]];
const TRACK = { x0: -34, x1: 118, y: 40, w: 16, z: 3.2 }, GATE = 18;
const BOT = { x0: -50, y0: 22, x1: -14, y1: 58, z1: 36 };
const N = SITES.length, SPEED = 0.052, TW = 38, TH = 20;

const topPath = (P, x, y, z) => poly(rrect(x - TW / 2, y - TH / 2, x + TW / 2, y + TH / 2, 4, 6).map((q) => P(q.u, q.v, z)));
const edgePath = (P, front, x, y, z) => open(run(rrect(x - TW / 2, y - TH / 2, x + TW / 2, y + TH / 2, 4, 6), front).map((q) => P(q.u, q.v, z - 2.1)));
const eyePath = (P, cx, cz, w, h, r) => poly(rrect(cx - w / 2, cz - h / 2, cx + w / 2, cz + h / 2, r, 5).map((q) => P(q.u, BOT.y1, q.v)));
const drawSolid = (parent, P, front, x0, y0, x1, y1, r, b, z0, z1) => {
  const [ring, inner] = rings(x0, y0, x1, y1, r, b), el = solid(parent);
  put(el, prism(P, front, ring, inner, z0, z1));
  return el;
};

function mount({ stage, svg, read }, value) {
  const bag = disposer();
  let slow = value, over = false, pick = -1, phase = 0, bandS = 0;
  const rate = spring(1, { k: 100, c: 18, m: 1, eps: 0.001 });
  const C = Cam(45, 0.5, 1.56);
  fit(C, [[-60, -32, -5], [126, 112, -5], [-60, 112, 64], [126, -32, 32]], 200, 166);
  const P = proj(C), front = facing(C), g = mk("g", {}, svg);

  drawSolid(g, P, front, -60, -32, 126, 112, 12, 2.1, -4, 0);
  const [belt, beltIn] = rings(TRACK.x0, TRACK.y - TRACK.w / 2, TRACK.x1, TRACK.y + TRACK.w / 2, 8, 1.5);
  const beltEl = solid(g); put(beltEl, prism(P, front, belt, beltIn, 0, 2.5));
  mk("path", { d: seg(P(TRACK.x0 + 12, TRACK.y, 3), P(TRACK.x1 - 13, TRACK.y, 3)), class: "nf lo dash" }, g);

  drawSolid(g, P, front, GATE - 7, -28, GATE + 7, 28, 5, 1.1, 0, 12);

  const tokens = [];
  for (let i = 0; i < N; i++) {
    const grp = mk("g", {}, g), tile = mk("path", { class: "nf lo" }, grp), edge = mk("path", { class: "nf lo" }, grp), dots = [];
    for (let k = 0; k < 6; k++) dots.push(mk("circle", { r: 1.7, class: "dot off" }, grp));
    tokens.push({ grp, tile, edge, dots, lift: spring(0, { k: 100, c: 18, m: 1, eps: 0.02 }), last: "" });
  }

  drawSolid(g, P, front, GATE - 7, 52, GATE + 7, 108, 5, 1.1, 0, 12);
  drawSolid(g, P, front, GATE - 7, 28, GATE + 7, 34, 3, 0.8, 0, 18);
  drawSolid(g, P, front, GATE - 7, 46, GATE + 7, 52, 3, 0.8, 0, 18);

  const [br, bi] = rings(BOT.x0, BOT.y0, BOT.x1, BOT.y1, 6, 1.4), bot = solid(g);
  put(bot, prism(P, front, br, bi, 0, BOT.z1));
  const L = BOT.x1 - BOT.x0, ew = 0.13 * L, eh = 0.15 * L, er = 0.034 * L, ev = 0.50 * L;
  const eyes = [
    mk("path", { d: eyePath(P, BOT.x0 + 0.32 * L, ev, ew, eh, er), class: "nf hi" }, g),
    mk("path", { d: eyePath(P, BOT.x0 + 0.68 * L, ev, ew, eh, er), class: "nf hi" }, g),
  ];

  const hitA = P(TRACK.x0, TRACK.y, TRACK.z), hitB = P(TRACK.x1, TRACK.y, TRACK.z);
  const vx = hitB[0] - hitA[0], vy = hitB[1] - hitA[1], vv = vx * vx + vy * vy;
  const hit = ([x, y]) => {
    const t = clamp(((x - hitA[0]) * vx + (y - hitA[1]) * vy) / vv, 0, 1);
    const dx = x - (hitA[0] + vx * t), dy = y - (hitA[1] + vy * t);
    return Math.hypot(dx, dy) < 22 ? t : -1;
  };
  const tokS = (i) => (phase + i / N) % 1;
  const choose = (s) => {
    let best = 0, bd = 9;
    for (let i = 0; i < N; i++) { const d = Math.abs(tokS(i) - s); if (d < bd) { bd = d; best = i; } }
    return best;
  };
  const retargetLift = () => tokens.forEach((t, i) => { t.lift.t = over && i === pick ? 18 : 0; });

  function drawToken(t, i) {
    const s = tokS(i), x = TRACK.x0 + s * (TRACK.x1 - TRACK.x0), z = TRACK.z + 0.55 + t.lift.x;
    const real = x >= GATE + 5, hot = i === pick && over;
    const d = topPath(P, x, TRACK.y, z), e = real ? edgePath(P, front, x, TRACK.y, z) : "";
    const cls = "nf " + (hot ? "hi" : real ? "sil" : "lo"), key = d + e + cls;
    if (key !== t.last) { t.tile.setAttribute("d", d); t.tile.setAttribute("class", cls); t.edge.setAttribute("d", e); t.edge.setAttribute("class", "nf " + (hot ? "hi" : real ? "sil" : "lo")); t.last = key; }
    for (let k = 0; k < 6; k++) {
      place(t.dots[k], P(x + ((k % 3) - 1) * 8.2, TRACK.y + (Math.floor(k / 3) - 0.5) * 6.2, z + 0.3));
      const on = real && PAT[i].includes(k);
      t.dots[k].setAttribute("class", on ? (hot ? "dot" : "dot m") : "dot off");
    }
  }

  const B = register(stage, (dt) => {
    stepS(rate, dt);
    phase = (phase + dt * SPEED * Math.max(0, rate.x)) % 1;
    let moving = true;
    tokens.forEach((t) => { if (stepS(t.lift, dt)) moving = true; });
    tokens.forEach(drawToken);
    return true || moving;
  });
  bag.add(B.unregister);

  function setHover(s) {
    over = s >= 0; rate.t = over ? slow : 1;
    if (over) { bandS = s; if (pick < 0) pick = choose(bandS); read.textContent = SITES[pick]; }
    else { pick = -1; read.textContent = "rest"; }
    retargetLift();
    eyes.forEach((e) => e.classList.toggle("hi", !over));
    B.wake();
  }

  bag.add(pointer(stage, {
    move: (p) => { const s = hit(p); if (s >= 0 && (!over || Math.abs(s - bandS) > 0.09)) pick = choose(s); setHover(s); },
    leave: () => setHover(-1),
  }));
  bag.add(() => svg.replaceChildren());

  return { set: (v) => { slow = v; if (over) rate.t = slow; }, destroy: bag.dispose };
}

hairline({
  name: "checkpoint",
  means: "Requests leave the machine holding placeholders; the host swaps in the real secret only past the gate.",
  rules: [1, 4, 7, 8, 10],
  range: [0, 0.15, 0.4],
  mount,
});
