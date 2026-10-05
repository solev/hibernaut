const {
  Cam, circ, facing, fit, lerp, open, poly, prism, proj, rrect, run, unproj,
  disposer, mk, pointer, put, register, solid, spring, stepS,
} = HL;

const N = 4, R = 54, STEP = Math.PI / 2, FRONT = Math.PI / 4;
const NAMES = ["base", "node", "py", "agent"];
const SIDE = 35, Z0 = 1, H = [38, 35, 42, 36], LIFT = 12;
const WRAP = (a, b) => b + Math.atan2(Math.sin(a - b), Math.cos(a - b));
const MOD = (i) => (i % N + N) % N;

function mount({ stage, svg, read }, value) {
  const bag = disposer();
  let coast = value, rot = FRONT, vel = 0, targetRot = FRONT, target = 0, hover = -1, over = false, last = null, settling = false;
  const C = Cam(45, 0.5, 1.64);
  fit(C, [[-84, -84, -12], [84, 84, -12], [84, -84, 0], [-84, 84, 0], [72, 72, 54], [-72, -72, 54]], 200, 166);
  const P = proj(C), front = facing(C);

  const g = mk("g", {}, svg);
  put(solid(g), prism(P, front, circ(54, 80), circ(50, 80), -12, -6));
  put(solid(g), prism(P, front, circ(76, 112), circ(70, 112), -6, 0));
  const hub = solid(g);
  put(hub, prism(P, front, circ(12, 48), circ(9, 48), 0, 4));

  const bots = [];
  for (let i = 0; i < N; i++) {
    const group = mk("g", {}, g), body = solid(group);
    const eyes = [mk("path", { class: "nf lo" }, group), mk("path", { class: "nf lo" }, group)];
    const dots = Array.from({ length: 4 }, () => mk("circle", { r: 1.45, class: "dot off" }, group));
    bots.push({ group, body, eyes, dots, lift: spring(0, { eps: .02 }), drawn: "" });
  }

  function detent(a) {
    const i = MOD(Math.round((FRONT - a) / STEP));
    return { i, a: WRAP(FRONT - i * STEP, a) };
  }

  function centre(i, a) {
    const t = a + i * STEP;
    return [Math.cos(t) * R, Math.sin(t) * R, t];
  }

  function eye(Pf, w) {
    const L = SIDE, ew = lerp(.256, .13, w) * L, eh = lerp(.068, .15, w) * L, er = Math.min(.034 * L, ew / 2, eh / 2);
    const cy = lerp(.448, .50, w) * L, xs = [lerp(.29, .32, w) * L, lerp(.71, .68, w) * L];
    return xs.map((cx) => poly(rrect(cx - ew / 2, cy - eh / 2, cx + ew / 2, cy + eh / 2, er, 8).map(Pf)));
  }

  function drawBot(i) {
    const b = bots[i], [cx, cy] = centre(i, rot), active = i === (hover >= 0 ? hover : target);
    const lift = b.lift.x, h = SIDE / 2, top = H[i] + lift;
    const ring = rrect(cx - h, cy - h, cx + h, cy + h, 6.2, 14);
    const inner = rrect(cx - h + 2, cy - h + 2, cx + h - 2, cy + h - 2, 4.2, 14);
    const key = [cx.toFixed(2), cy.toFixed(2), lift.toFixed(2), active, hover].join(",");
    if (key !== b.drawn) {
      b.drawn = key;
      put(b.body, prism(P, front, ring, inner, Z0 + lift, top));
      b.body.sil.classList.toggle("hi", active);
      const showFace = front({ nu: 0, nv: 1 });
      const Pf = (q) => P(cx - h + q.u, cy + h + .35, Z0 + lift + q.v);
      const ep = showFace ? eye(Pf, active ? 1 : 0) : ["", ""];
      b.eyes.forEach((e, k) => { e.setAttribute("d", ep[k]); e.setAttribute("class", active ? "nf hi" : "nf lo"); });
      const on = i + 1, spots = [[-5, -5], [5, -5], [-5, 5], [5, 5]];
      b.dots.forEach((d, k) => {
        const u = spots[k][0], v = spots[k][1];
        d.setAttribute("transform", `translate(${P(cx + u, cy + v, top + .45).join(" ")})`);
        d.setAttribute("class", k < on ? (active ? "dot" : "dot m") : "dot off");
      });
    }
    return cx + cy;
  }

  function order(depths) {
    depths.sort((a, b) => a[0] - b[0]);
    for (let i = 1; i < depths.length; i++) bots[depths[i - 1][1]].group.after(bots[depths[i][1]].group);
  }

  function draw() {
    const depths = [];
    for (let i = 0; i < N; i++) depths.push([drawBot(i), i]);
    order(depths);
  }

  function hoverAt(p) {
    let best = -1, bd = 34;
    for (let i = 0; i < N; i++) {
      const [x, y] = centre(i, targetRot), q = P(x, y, H[i] + 1), d = Math.hypot(q[0] - p[0], q[1] - p[1]);
      if (d < bd) { bd = d; best = i; }
    }
    return best;
  }

  function caption() {
    read.textContent = over ? NAMES[hover >= 0 ? hover : target] : "rest";
  }

  function setHover(p) {
    const h = hoverAt(p);
    const q = unproj(C, p[0], p[1], 0);
    over = h >= 0 || Math.hypot(q[0], q[1]) < 82;
    hover = over ? h : -1;
    bots.forEach((b, i) => { b.lift.t = over && hover === i ? LIFT : 0; });
    caption();
    draw();
    B.wake();
  }

  const B = register(stage, (dt) => {
    let moving = false;
    if (Math.abs(vel) > .001 && !settling) {
      rot += vel * dt;
      vel *= Math.exp(-dt / coast);
      if (Math.abs(vel) < .24) { const d = detent(rot); target = d.i; targetRot = d.a; settling = true; }
    }
    if (settling) {
      const d = WRAP(targetRot, rot) - rot;
      vel += (d * 95 - vel * 16) * dt;
      rot += vel * dt;
      if (Math.abs(d) < .002 && Math.abs(vel) < .01) { rot = targetRot; vel = 0; settling = false; }
    }
    bots.forEach((b) => { if (stepS(b.lift, dt)) moving = true; });
    if (over && hover < 0) caption();
    draw();
    return moving || Math.abs(vel) > .001 || settling;
  });
  bag.add(B.unregister);

  draw();
  bag.add(pointer(stage, {
    move: (p) => {
      setHover(p);
      if (over && last) {
        const dx = p[0] - last[0];
        if (Math.abs(dx) > .05) { vel += dx * .008; settling = false; B.wake(); }
      }
      last = over ? p : null;
    },
    leave: () => { over = false; hover = -1; last = null; bots.forEach((b) => { b.lift.t = 0; }); caption(); draw(); B.wake(); },
  }));
  bag.add(() => svg.replaceChildren());

  return { set: (v) => { coast = v; }, destroy: bag.dispose };
}

hairline({
  name: "carousel",
  means: "Saved machines on a turntable: flick it round, and the image that stops in front wakes up ready.",
  rules: [1, 4, 5, 7, 8, 9, 10],
  range: [.4, 1.2, 3],
  mount,
});
