import { balance, planckRGB, linear } from "../web/graycard.js";
// Synthetic: a card under T_real, the camera set to T_set: rendered
// (linear) = ill(T_real)/ill(T_set), scaled to mid gray, then to sRGB.
const toSRGB = (c) => 255 * (c <= 0.0031308 ? 12.92 * c : 1.055 * Math.pow(c, 1 / 2.4) - 0.055);
function card(real, set, gain = 1) {
  const a = planckRGB(real), s = planckRGB(set);
  let lin = a.map((v, i) => v / s[i]);
  const m = (lin[0] + lin[1] + lin[2]) / 3;
  lin = lin.map((v) => (v / m) * 0.18 * gain);
  return lin.map(toSRGB);
}
let fail = 0;
for (const [real, set] of [[3200, 5500], [5600, 3200], [4300, 4300], [6500, 5000], [2800, 6500], [8000, 5500]]) {
  const r = balance(card(real, set), set);
  const ok = !r.problem && Math.abs(r.kelvin - real) < 30 && Math.abs(r.cast) < 0.02 && r.warmer === (real < set);
  if (!ok) fail++;
  console.log(ok ? "ok  " : "FAIL", `real ${real} set ${set} →`, r.problem || `${Math.round(r.kelvin)} K, cast ${r.cast.toFixed(3)}, warmer ${r.warmer}`);
}
// A green cast is reported.
const c = card(4000, 5500); c[1] *= 1.1;
const g = balance(c, 5500);
console.log(g.cast > 0.05 ? "ok  " : "FAIL", "green cast", g.cast.toFixed(3)); if (!(g.cast > 0.05)) fail++;
// Clipped / dark.
for (const [name, rgb] of [["clipped", [255, 240, 230]], ["dark", [5, 6, 7]]]) {
  const r = balance(rgb, 5500); console.log(r.problem ? "ok  " : "FAIL", name, r.problem); if (!r.problem) fail++;
}
process.exit(fail ? 1 : 0);
