const {
  Cam, clamp, facing, fit, hull, lerp, open, poly, prism, proj, ringAt, rings, rrect, run,
  disposer, mk, place, pointer, put, register, solid, spring, stepS,
} = HL;

const L = 27, LOW = 17, HIGH = 31, PL = { x0: -88, y0: -31, x1: 88, y1: 49, z0: -6, z1: 0 };
const BOT = [
  ["web", -56, 16, 0.18],
  ["api", -28, 6, 0.52],
  ["ai", 0, 2, 1],
  ["db", 28, 6, 0.32],
  ["ops", 56, 16, 0.72],
];
const AGENT = 2;

function mount({ stage, svg, read }, value) {
  const bag = disposer();
  const C = Cam(45, 0.5, 1.7);
  const fitPts = [[PL.x0, PL.y0, PL.z0], [PL.x1, PL.y1, PL.z0], [PL.x1, PL.y0, 0], [PL.x0, PL.y1, 0]];
  BOT.forEach((b) => fitPts.push([b[1] - L / 2, b[2] - L / 2, 0], [b[1] + L / 2, b[2] + L / 2, HIGH + 3]));
  fit(C, fitPts, 200, 166);
  const P = proj(C), front = facing(C);
  let timeout = value, over = -1, pulse = 0, hi = AGENT;

  const g = mk("g", {}, svg);
  const [pr, pi] = rings(PL.x0, PL.y0, PL.x1, PL.y1, 14, 2.2);
  put(solid(g), prism(P, front, pr, pi, PL.z0, PL.z1));

  function eyePath(x0, y1, wake, side, left) {
    const cx = lerp(left ? 0.29 : 0.71, left ? 0.32 : 0.68, wake) * side;
    const cy = lerp(0.448, 0.5, wake) * side;
    const ew = lerp(0.256, 0.13, wake) * side, eh = lerp(0.068, 0.15, wake) * side;
    const er = Math.min(0.034 * side, ew / 2, eh / 2);
    return poly(rrect(cx - ew / 2, cy - eh / 2, cx + ew / 2, cy + eh / 2, er, 10).map((q) => P(x0 + q.u, y1, q.v)));
  }

  const bots = BOT.map(([name, cx, cy, a], i) => {
    const x0 = cx - L / 2, y0 = cy - L / 2, x1 = cx + L / 2, y1 = cy + L / 2;
    const grp = mk("g", {}, g), [ring, inner] = rings(x0, y0, x1, y1, 4.8, 1.05);
    const body = solid(grp);
    const eyes = [mk("path", { class: "nf" }, grp), mk("path", { class: "nf" }, grp)];
    const dots = [0, 1, 2, 3].map(() => mk("circle", { r: 1.18, class: "dot off" }, grp));
    return { name, cx, cy, x0, y1, ring, inner, body, eyes, dots, a, w: spring(a, { eps: 0.003 }), key: "" };
  }).sort((a, b) => (a.cx + a.cy) - (b.cx + b.cy));

  const hit = (p) => {
    let best = -1, bd = 1e9;
    BOT.forEach((b, i) => {
      const c = P(b[1], b[2], HIGH), dx = (p[0] - c[0]) / 29, dy = (p[1] - c[1]) / 30;
      const d = dx * dx + dy * dy;
      if (d < 1.45 && d < bd) { bd = d; best = i; }
    });
    return best;
  };
  function drawBot(b, lit) {
    const wake = clamp(b.w.x, 0, 1), h = lerp(LOW, HIGH, wake), on = Math.ceil(b.a * 4 - 0.001);
    const key = `${wake.toFixed(3)}:${b.a.toFixed(3)}:${lit ? 1 : 0}:${on}`;
    if (key === b.key) return;
    b.key = key;
    put(b.body, prism(P, front, b.ring, b.inner, 0, h));
    b.body.sil.classList.toggle("hi", lit);
    b.eyes[0].setAttribute("d", eyePath(b.x0, b.y1, wake, L, true));
    b.eyes[1].setAttribute("d", eyePath(b.x0, b.y1, wake, L, false));
    b.eyes.forEach((e) => e.setAttribute("class", lit ? "nf hi" : wake < 0.08 ? "nf lo" : "nf"));
    b.dots.forEach((d, k) => {
      d.setAttribute("class", k < on ? lit ? "dot" : "dot m" : "dot off");
      place(d, P(b.cx + (k - 1.5) * 4.8, b.cy - 5.8, h + 0.25));
    });
  }

  function drawAll() {
    bots.forEach((b) => drawBot(b, BOT.findIndex((q) => q[0] === b.name) === hi));
  }

  const B = register(stage, (dt) => {
    const period = Math.min(1.5, Math.max(0.75, timeout * 0.55));
    pulse += dt;
    if (over >= 0) BOT[over][3] = 1;
    else if (pulse >= period) { BOT[AGENT][3] = 1; pulse = 0; }
    BOT.forEach((b, i) => {
      if (i !== over) b[3] = Math.max(0, b[3] - dt / timeout);
      const ob = bots.find((q) => q.name === b[0]);
      ob.a = b[3]; ob.w.t = b[3]; stepS(ob.w, dt);
    });
    drawAll();
    return true;
  });
  bag.add(B.unregister);

  function setOver(i) {
    over = i; hi = i >= 0 ? i : AGENT;
    if (i >= 0) { BOT[i][3] = 1; read.textContent = `${BOT[i][0]} busy`; }
    else read.textContent = "rest";
    B.wake(); drawAll();
  }

  drawAll(); read.textContent = "rest";
  bag.add(pointer(stage, { move: (p) => setOver(hit(p)), leave: () => setOver(-1) }));
  bag.add(() => svg.replaceChildren());

  return {
    set: (v) => { timeout = v; },
    destroy: bag.dispose,
  };
}

hairline({
  name: "idle",
  means: "Machines stay awake while working; idle ones drain and sleep while a waiting agent keeps itself awake.",
  rules: [1, 4, 5, 7, 8, 9],
  range: [1.5, 4, 9],
  mount,
});
