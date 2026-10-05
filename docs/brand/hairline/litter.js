const {
  Cam, facing, fit, prism, proj, rings, rrect, poly,
  tdone, tset, tval, tween, disposer, mk, place, pointer, put, register, solid,
} = HL;

const N = 5, L = 28, H = 24, PL = [-12, -10, 144, 94], ORG = [2, 0];
const REST = Array.from({ length: N }, (_, i) => [20 + i * 7, 10 + i * 4]);
const ROW = Array.from({ length: N }, (_, i) => [28 + i * 20, 14 + i * 8]);
const STEP = [4, 22], R = 5.2, B = 1.2;
const CODE = [1, 0, 1, 1, 0, 1];

const lerp = (a, b, t) => a + (b - a) * t;
const eyeBox = (P, x, y, z, wake, hi) => {
  const w = lerp(L * 0.256, L * 0.13, wake), h = lerp(L * 0.068, L * 0.15, wake);
  const v = lerp(L * 0.448, L * 0.5, wake), cy = y + L, r = Math.min(L * 0.034, w / 2, h / 2);
  const centers = [lerp(0.29, 0.32, wake) * L, lerp(0.71, 0.68, wake) * L];
  return centers.map((u) => poly(rrect(u - w / 2, v - h / 2, u + w / 2, v + h / 2, r, 5).map((q) => P(x + q.u, cy, z + q.v))));
};

function bot(P, front, x, y, z, wake) {
  const [ring, inner] = rings(x, y, x + L, y + L, R, B);
  const body = prism(P, front, ring, inner, z, z + H);
  const eyes = eyeBox(P, x, y, z, wake, false);
  const dots = [];
  for (let k = 0; k < 6; k++) {
    const u = x + L * 0.5 + ((k % 3) - 1) * 4.1, v = y + L * 0.5 + (Math.floor(k / 3) - 0.5) * 4.1;
    dots.push([P(u, v, z + H + 0.25), CODE[k] ? "dot m" : "dot off"]);
  }
  return { body, eyes, dots };
}

function mount({ stage, svg, read }, value) {
  const bag = disposer();
  let stag = value, active = -1;
  const C = Cam(45, 0.5, 1.58);
  fit(C, [[PL[0], PL[1], -4], [PL[2], PL[3], -4], [PL[2] + L, PL[3] + L, H + 12], [ORG[0], ORG[1], H], [ROW[4][0] + STEP[0] + L, ROW[4][1] + STEP[1] + L, H + 16]], 200, 166);
  const P = proj(C), front = facing(C);
  const g = mk("g", {}, svg);
  const [pr, pi] = rings(PL[0], PL[1], PL[2], PL[3], 12, 2.2);
  put(solid(g), prism(P, front, pr, pi, -4, 0));

  function makeBot(isOriginal) {
    const grp = mk("g", {}, g), el = solid(grp);
    const eyes = [mk("path", { class: "nf" }, grp), mk("path", { class: "nf" }, grp)];
    const dots = CODE.map((on) => mk("circle", { r: 1.05, class: on ? "dot m" : "dot off" }, grp));
    return { grp, el, eyes, dots, spread: tween(0), pick: tween(0), drawn: "", isOriginal };
  }

  const original = makeBot(true), forks = REST.map(() => makeBot(false));
  const centers = REST.map(([x, y]) => P(x + L / 2, y + L / 2, H));

  function drawOne(b, x, y, z, wake, hi) {
    const key = [x, y, z, wake].map((n) => n.toFixed(2)).join(",") + hi;
    if (key === b.drawn) return;
    b.drawn = key;
    const q = bot(P, front, x, y, z, wake);
    put(b.el, q.body);
    b.el.sil.classList.toggle("hi", hi);
    b.eyes.forEach((e, k) => { e.setAttribute("d", q.eyes[k]); e.classList.toggle("hi", hi); });
    b.dots.forEach((d, k) => { place(d, q.dots[k][0]); d.setAttribute("class", q.dots[k][1]); });
  }

  function draw(now) {
    const order = [{ b: original, x: ORG[0], y: ORG[1], z: 0, wake: 1, hi: active < 0 }];
    forks.forEach((f, i) => {
      const s = tval(f.spread, now), p = tval(f.pick, now), r = REST[i], row = ROW[i];
      const x = lerp(r[0], row[0], s) + STEP[0] * p, y = lerp(r[1], row[1], s) + STEP[1] * p;
      order.push({ b: f, x, y, z: 8.5 * p, wake: lerp(0.1, 1, p), hi: i === active });
    });
    order.sort((a, b) => a.x + a.y - b.x - b.y).forEach((q) => { g.appendChild(q.b.grp); drawOne(q.b, q.x, q.y, q.z, q.wake, q.hi); });
  }

  const B = register(stage, (_dt, now) => {
    let moving = false;
    forks.forEach((f) => { if (!tdone(f.spread, now) || !tdone(f.pick, now)) moving = true; });
    draw(now);
    return moving;
  });
  bag.add(B.unregister);

  function hit(p) {
    let best = -1, bd = 34;
    centers.forEach((c, i) => {
      const d = Math.hypot(p[0] - c[0], p[1] - c[1]);
      if (d < bd) { bd = d; best = i; }
    });
    return best;
  }

  function setActive(a) {
    if (a === active) return;
    const now = performance.now(), from = a >= 0 ? a : active;
    active = a;
    forks.forEach((f, i) => {
      const delay = Math.abs(i - from) * stag;
      tset(f.spread, a < 0 ? 0 : 1, now, delay);
      tset(f.pick, i === a ? 1 : 0, now, delay);
    });
    read.textContent = a < 0 ? "rest" : `fork ${a + 2}`;
    B.wake();
  }

  draw(performance.now());
  read.textContent = "rest";
  bag.add(pointer(stage, { move: (p) => setActive(hit(p)), leave: () => setActive(-1) }));
  bag.add(() => svg.replaceChildren());

  return { set: (v) => { stag = v; }, destroy: bag.dispose };
}

hairline({
  name: "litter",
  means: "One machine copied live: the fork under the pointer steps out with the same state while the others make room.",
  rules: [1, 2, 4, 8],
  range: [0, 45, 90],
  mount,
});
