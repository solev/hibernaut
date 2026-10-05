const {
  Cam, clamp, facing, fit, lerp, open, poly, prism, proj, ringAt, rings, rrect, run,
  seg, tween, tdone, tset, tval, disposer, mk, pointer, put, register, solid,
} = HL;

const BASE = { x0: -68, y0: -52, x1: 68, y1: 52, z0: -7, z1: 0 };
const BOT = { x0: -23, y0: -23, x1: 23, y1: 23, z0: 0, h: 44 };
const COVERS = [
  { name: "host", x0: -48, y0: -38, x1: 48, y1: 38, r: 14, rim: 72, h: 34, inset: 2.2, lip: 2.8, knob: 7 },
  { name: "vm", x0: -39, y0: -31, x1: 39, y1: 31, r: 12, rim: 59, h: 30, inset: 2.0, lip: 2.5, knob: 6 },
  { name: "kernel", x0: -30, y0: -25, x1: 30, y1: 25, r: 10, rim: 49, h: 26, inset: 1.7, lip: 2.2, knob: 5.2 },
];

const ringBox = (c) => rings(c.x0, c.y0, c.x1, c.y1, c.r, c.inset);
const knobBox = (c) => rings(-c.knob, -c.knob * 0.62, c.knob, c.knob * 0.62, c.knob * 0.45, 0.85);
const shifted = (ring, dx, dy) => ring.map((q) => ({ ...q, u: q.u + dx, v: q.v + dy }));

function eye(P, x0, y1, z0, wake, left) {
  const L = BOT.x1 - BOT.x0;
  const cx = lerp(left ? 0.29 : 0.71, left ? 0.32 : 0.68, wake) * L;
  const cy = lerp(0.448, 0.50, wake) * L;
  const ew = lerp(0.256, 0.13, wake) * L, eh = lerp(0.068, 0.15, wake) * L;
  const er = Math.min(0.034 * L, ew / 2, eh / 2);
  const rr = rrect(cx - ew / 2, cy - eh / 2, cx + ew / 2, cy + eh / 2, er, 10);
  return poly(rr.map((q) => P(x0 + q.u, y1, z0 + q.v)));
}

function mount({ stage, svg, read }, value) {
  const bag = disposer();
  let lift = value, active = -1;
  const C = Cam(45, 0.5, 1.33);
  fit(C, [[BASE.x0, BASE.y0, BASE.z0], [BASE.x1, BASE.y1, BASE.z0], [BASE.x1, BASE.y0, 0], [BASE.x0, BASE.y1, 0],
    [-58, -64, COVERS[0].rim + COVERS[0].h + 58], [58, 52, 0], [BOT.x0, BOT.y0, 0], [BOT.x1, BOT.y1, BOT.h + 7]], 200, 158);
  const P = proj(C), front = facing(C);

  const g = mk("g", {}, svg);
  const [br, bi] = rings(BASE.x0, BASE.y0, BASE.x1, BASE.y1, 12, 2.2);
  put(solid(g), prism(P, front, br, bi, BASE.z0, BASE.z1));

  const guides = COVERS.map(() => mk("path", { class: "nf dash lo" }, g));
  const bot = solid(g);
  const eyes = [mk("path", { class: "nf" }, g), mk("path", { class: "nf" }, g)];
  const wake = tween(0), rise = tween(0);

  const covers = COVERS.map((c) => {
    const [ring, inner] = ringBox(c), [kr, ki] = knobBox(c), body = solid(g), knob = solid(g);
    const lip = mk("path", { class: "nf lo" }, body.g);
    return { c, ring, inner, kr, ki, z: tween(0), a: tween(0), body, knob, lip, last: "" };
  });
  covers.reverse().forEach((cv) => { g.append(cv.body.g); g.append(cv.knob.g); });

  function drawBot(now) {
    const w = tval(wake, now), bz = tval(rise, now), r = 7.2 + w * 0.8;
    const [ring, inner] = rings(BOT.x0, BOT.y0, BOT.x1, BOT.y1, r, 1.7);
    put(bot, prism(P, front, ring, inner, BOT.z0 + bz, BOT.h + bz));
    bot.sil.classList.toggle("hi", active === 3);
    eyes[0].setAttribute("d", eye(P, BOT.x0, BOT.y1, BOT.z0 + bz, w, true));
    eyes[1].setAttribute("d", eye(P, BOT.x0, BOT.y1, BOT.z0 + bz, w, false));
    eyes.forEach((e) => e.setAttribute("class", "nf" + (active === 3 ? " hi" : "")));
  }

  function coverPose(i, now) {
    const cv = covers.find((q) => q.c === COVERS[i]);
    return { cv, z: cv.c.rim + tval(cv.z, now), away: tval(cv.a, now) };
  }

  function drawGuide(cv, z, away) {
    const c = cv.c, y = -away;
    const pts = [[c.x0 + c.r, c.y0], [c.x1 - c.r, c.y0], [c.x1, c.y1 - c.r], [c.x0, c.y1 - c.r]];
    cv.guideD = pts.map((p) => seg(P(p[0], p[1] + y, z), P(p[0], p[1], 0.4))).join("");
  }

  function drawCover(i, now) {
    const { cv, z, away } = coverPose(i, now), c = cv.c, y = -away;
    const key = [i, z.toFixed(2), away.toFixed(2), active].join(",");
    if (key === cv.last) return;
    cv.last = key;
    const ring = shifted(cv.ring, 0, y), inner = shifted(cv.inner, 0, y);
    put(cv.body, prism(P, front, ring, inner, z, z + c.h));
    cv.lip.setAttribute("d", open(ringAt(P, run(inner, front), z + c.lip)));
    const kr = shifted(cv.kr, 0, y), ki = shifted(cv.ki, 0, y);
    put(cv.knob, prism(P, front, kr, ki, z + c.h, z + c.h + 4.5));
    const hi = active === i || (active < 0 && i === 0);
    cv.body.sil.classList.toggle("hi", hi);
    cv.knob.sil.classList.toggle("hi", hi);
    drawGuide(cv, z, away);
  }

  function draw(now = performance.now()) {
    drawBot(now);
    for (let i = 2; i >= 0; i--) drawCover(i, now);
    guides.forEach((el, i) => el.setAttribute("d", covers.find((q) => q.c === COVERS[i]).guideD || ""));
  }

  const centres = COVERS.map((c) => P(0, -4, c.rim + c.h));
  function inBot(p) {
    const pts = [P(BOT.x0, BOT.y1, 0), P(BOT.x1, BOT.y1, 0), P(BOT.x1, BOT.y1, BOT.h), P(BOT.x0, BOT.y1, BOT.h)];
    const xs = pts.map((q) => q[0]), ys = pts.map((q) => q[1]);
    return p[0] > Math.min(...xs) - 10 && p[0] < Math.max(...xs) + 10 && p[1] > Math.min(...ys) - 10 && p[1] < Math.max(...ys) + 10;
  }
  function hit(p) {
    if (inBot(p)) return 3;
    let best = -1, bd = 1e9;
    COVERS.forEach((c, i) => {
      const d = Math.hypot((p[0] - centres[i][0]) / (36 - i * 4), (p[1] - centres[i][1]) / 15);
      if (d < bd) { bd = d; best = i; }
    });
    return bd < 1.35 ? best : -1;
  }

  function targets(a) {
    return COVERS.map((_, i) => {
      if (a === 3) return { z: lift * (1.38 - i * 0.12), away: lift * (0.52 + (2 - i) * 0.08) };
      if (a < 0) return { z: 0, away: 0 };
      if (i < a) return { z: lift * (1.02 + (a - i) * 0.22), away: lift * (0.34 + (a - i) * 0.08) };
      if (i === a) return { z: lift * 0.32, away: lift * 0.08 };
      return { z: 0, away: 0 };
    });
  }

  function setActive(a) {
    if (a === active) return;
    const now = performance.now(), from = active < 0 ? a : active;
    active = a;
    targets(a).forEach((t, i) => {
      const cv = covers.find((q) => q.c === COVERS[i]), d = Math.abs(i - clamp(from, 0, 2)) * 45;
      tset(cv.z, t.z, now, d); tset(cv.a, t.away, now, d);
    });
    tset(wake, a === 3 ? 1 : 0, now, a === 3 ? 120 : 0);
    tset(rise, a === 3 ? 6 : 0, now, a === 3 ? 120 : 0);
    read.textContent = a < 0 ? "rest" : a === 3 ? "bot" : COVERS[a].name;
    B.wake();
  }

  const B = register(stage, (_dt, now) => {
    let moving = !tdone(wake, now) || !tdone(rise, now);
    for (const cv of covers) moving = moving || !tdone(cv.z, now) || !tdone(cv.a, now);
    draw(now);
    return moving;
  });
  bag.add(B.unregister);
  draw(); read.textContent = "rest";
  bag.add(pointer(stage, { move: (p) => setActive(hit(p)), leave: () => setActive(-1) }));
  bag.add(() => svg.replaceChildren());

  return { set: (v) => { const a = active; lift = v; active = 99; setActive(a); }, destroy: bag.dispose };
}

hairline({
  name: "nest",
  means: "Each machine is a real VM with its own kernel: lift covers one by one to find the bot inside.",
  rules: [1, 2, 4, 6, 8, 9],
  range: [16, 26, 38],
  mount,
});
